// Package migrations embeds the SQLite schema migration scripts, applied in
// filename order by the store at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
