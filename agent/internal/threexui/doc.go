// Package threexui is the node-local HTTP boundary to a 3x-ui panel.
//
// It deliberately exposes only the operations Chiral has proved a particular
// panel supports. Authentication is by 3x-ui's full-admin Bearer token; callers
// must therefore treat a Client as secret-bearing state and must not format or
// log its internals.
//
// HTTP methods do not establish read-only semantics. The SHADOW Observer reads
// only status, OpenAPI and persisted outbound counters; assembling configuration
// or reading templates can trigger upstream maintenance and is candidate-only.
package threexui
