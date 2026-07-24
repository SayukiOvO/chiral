// Package template renders Xray config fragments and client subscription
// fragments from `{{variable}}` templates against the variable pool.
//
// The engine deliberately does ONE thing: substitute `{{name}}`. There are no
// conditionals or loops — iterating nodes × users, assembling the clients
// array, and stitching a subscription together are done in Go by the caller.
// That keeps templates unable to express control flow, which makes them
// impossible to "program" and trivial to reason about.
package template

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ref matches `{{ name }}`; names are [a-zA-Z0-9_.] with optional inner space.
var ref = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// Context is a fully-resolved, flat set of variables for one render. Callers
// build it by merging the four scopes in precedence order (user > node >
// profile > global) — see Merge.
type Context struct {
	vars map[string]string
	// secret names must never reach a client-visible render.
	secret map[string]struct{}
	// client marks a client-side render, where referencing a secret variable
	// is a hard error rather than a substitution.
	client bool
}

// NewContext builds a render context. secret lists variable names whose values
// must not escape to clients (private keys, seeds).
func NewContext(vars map[string]string, secret []string) *Context {
	c := &Context{vars: make(map[string]string, len(vars)), secret: make(map[string]struct{}, len(secret))}
	for k, v := range vars {
		c.vars[k] = v
	}
	for _, s := range secret {
		c.secret[s] = struct{}{}
	}
	return c
}

// ForClient returns a copy of the context restricted to client rendering:
// referencing a secret variable fails instead of leaking it. The secret values
// are dropped from the copy entirely, so a bug elsewhere cannot read them.
func (c *Context) ForClient() *Context {
	out := &Context{
		vars:   make(map[string]string, len(c.vars)),
		secret: c.secret,
		client: true,
	}
	for k, v := range c.vars {
		if _, isSecret := c.secret[k]; !isSecret {
			out.vars[k] = v
		}
	}
	return out
}

// With returns a copy with additional variables applied on top (higher
// precedence), e.g. layering per-user values over a profile context.
func (c *Context) With(vars map[string]string) *Context {
	out := &Context{vars: make(map[string]string, len(c.vars)+len(vars)), secret: c.secret, client: c.client}
	for k, v := range c.vars {
		out.vars[k] = v
	}
	for k, v := range vars {
		out.vars[k] = v
	}
	return out
}

// Names returns the variable names available in this context, sorted. Useful
// for editor autocomplete and for showing operators what they may reference.
func (c *Context) Names() []string {
	out := make([]string, 0, len(c.vars))
	for k := range c.vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Render substitutes every `{{name}}` in tmpl.
//
// It fails on the first problem rather than emitting a partially-resolved
// config: an unknown variable, or (in a client context) a reference to a
// secret. A silently half-rendered config is far more dangerous than a
// refused one — `xray -test` does not catch a config that is syntactically
// valid but semantically wrong.
func (c *Context) Render(tmpl string) (string, error) {
	var firstErr error
	out := ref.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := ref.FindStringSubmatch(match)[1]
		if _, isSecret := c.secret[name]; isSecret && c.client {
			if firstErr == nil {
				firstErr = fmt.Errorf("template references secret variable %q in a client template; secret variables never leave the panel", name)
			}
			return match
		}
		v, ok := c.vars[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("template references undefined variable %q", name)
			}
			return match
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// Refs lists the distinct variable names a template references, in order of
// first appearance. Used to validate a template before saving it.
func Refs(tmpl string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, m := range ref.FindAllStringSubmatch(tmpl, -1) {
		if _, dup := seen[m[1]]; dup {
			continue
		}
		seen[m[1]] = struct{}{}
		out = append(out, m[1])
	}
	return out
}

// Validate reports every problem with a template against this context, so the
// editor can show them all at once instead of one per save.
func (c *Context) Validate(tmpl string) []error {
	var errs []error
	for _, name := range Refs(tmpl) {
		if _, isSecret := c.secret[name]; isSecret && c.client {
			errs = append(errs, fmt.Errorf("secret variable %q cannot be used in a client template", name))
			continue
		}
		if _, ok := c.vars[name]; !ok {
			errs = append(errs, fmt.Errorf("undefined variable %q", name))
		}
	}
	return errs
}

// Merge flattens the four scopes into one map using the precedence
// user > node > profile > global, matching docs/template-system.md §1.
func Merge(global, profile, node, user map[string]string) map[string]string {
	out := make(map[string]string)
	for _, layer := range []map[string]string{global, profile, node, user} {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

// QualifyKeys prefixes each key with "prefix." — used to expose a generated
// key group as e.g. reality.private / reality.public.
func QualifyKeys(prefix string, m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[prefix+"."+k] = v
	}
	return out
}

// IsValidName reports whether s is usable as a variable name in a template.
func IsValidName(s string) bool {
	if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
