package template

import (
	"strings"
	"testing"
)

func ctx() *Context {
	return NewContext(map[string]string{
		"port":            "443",
		"sni":             "www.microsoft.com",
		"reality.private": "PRIV",
		"reality.public":  "PUB",
		"node.address":    "203.0.113.9",
		"user.uuid":       "a1cdea5c-6224-45d4-8f21-edfabb9d2c80",
	}, []string{"reality.private"})
}

func TestRenderSubstitutes(t *testing.T) {
	got, err := ctx().Render(`{"port": {{port}}, "sni": "{{sni}}", "key": "{{reality.private}}"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"port": 443, "sni": "www.microsoft.com", "key": "PRIV"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRenderAllowsInnerWhitespace(t *testing.T) {
	got, err := ctx().Render("{{ sni }}|{{sni}}|{{  sni  }}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "www.microsoft.com|www.microsoft.com|www.microsoft.com" {
		t.Errorf("got %q", got)
	}
}

func TestRenderUndefinedIsAnError(t *testing.T) {
	_, err := ctx().Render("{{nope}}")
	if err == nil {
		t.Fatal("expected an error for an undefined variable")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the variable, got: %v", err)
	}
}

// A half-rendered config is more dangerous than a refused one: it can still be
// valid JSON and still pass `xray -test`.
func TestRenderReturnsNothingOnError(t *testing.T) {
	got, err := ctx().Render(`{"a":"{{sni}}","b":"{{nope}}"}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got != "" {
		t.Errorf("expected empty output on error, got %q", got)
	}
}

func TestClientContextRefusesSecrets(t *testing.T) {
	_, err := ctx().ForClient().Render(`"key": "{{reality.private}}"`)
	if err == nil {
		t.Fatal("expected a client template referencing a secret to fail")
	}
	if !strings.Contains(err.Error(), "reality.private") {
		t.Errorf("error should name the secret, got: %v", err)
	}
}

func TestClientContextDropsSecretValues(t *testing.T) {
	// Defence in depth: the secret must not merely be refused at render time,
	// it must be absent from the client context entirely.
	c := ctx().ForClient()
	if _, present := c.vars["reality.private"]; present {
		t.Error("secret value is still present in the client context")
	}
	for _, n := range c.Names() {
		if n == "reality.private" {
			t.Error("secret is still listed in the client context")
		}
	}
}

func TestClientContextAllowsPublicHalf(t *testing.T) {
	got, err := ctx().ForClient().Render(`"pbk": "{{reality.public}}"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"pbk": "PUB"` {
		t.Errorf("got %q", got)
	}
}

func TestWithLayersOverBase(t *testing.T) {
	got, err := ctx().With(map[string]string{"user.uuid": "OVERRIDDEN"}).Render("{{user.uuid}}|{{sni}}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "OVERRIDDEN|www.microsoft.com" {
		t.Errorf("got %q", got)
	}
}

func TestWithKeepsSecrecyAndClientMode(t *testing.T) {
	c := ctx().ForClient().With(map[string]string{"extra": "x"})
	if _, err := c.Render("{{reality.private}}"); err == nil {
		t.Error("client mode must survive With()")
	}
}

func TestMergePrecedence(t *testing.T) {
	got := Merge(
		map[string]string{"a": "global", "b": "global"},
		map[string]string{"b": "profile", "c": "profile"},
		map[string]string{"c": "node", "d": "node"},
		map[string]string{"d": "user"},
	)
	want := map[string]string{"a": "global", "b": "profile", "c": "node", "d": "user"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
}

func TestRefs(t *testing.T) {
	got := Refs(`{{a}} {{ b }} {{a}} literal {{c.d}}`)
	want := []string{"a", "b", "c.d"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	errs := ctx().ForClient().Validate(`{{nope}} {{reality.private}} {{alsoNope}}`)
	if len(errs) != 3 {
		t.Fatalf("expected 3 problems, got %d: %v", len(errs), errs)
	}
}

func TestQualifyKeys(t *testing.T) {
	got := QualifyKeys("reality", map[string]string{"private": "p", "public": "P"})
	if got["reality.private"] != "p" || got["reality.public"] != "P" {
		t.Errorf("got %v", got)
	}
}

func TestIsValidName(t *testing.T) {
	for _, s := range []string{"a", "node.address", "reality.private", "a_b.c1"} {
		if !IsValidName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", ".a", "a.", "a-b", "a b", "a$"} {
		if IsValidName(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

// Templates are text, not JSON — a fragment may legitimately be a URI line.
func TestRenderNonJSONTemplate(t *testing.T) {
	got, err := ctx().ForClient().Render(
		`vless://{{user.uuid}}@{{node.address}}:{{port}}?sni={{sni}}&pbk={{reality.public}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "vless://a1cdea5c-6224-45d4-8f21-edfabb9d2c80@203.0.113.9:443?sni=www.microsoft.com&pbk=PUB"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
