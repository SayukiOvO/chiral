// equivalence produces private, offline evidence from an already-created
// SQLite backup. It is deliberately separate from Core startup and migration.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/equivalence"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, diagnostic io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(diagnostic, "expected export or compare")
		return 1
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	// flag errors may quote arbitrary arguments. Keep diagnostics fixed and
	// never accept a secret-key argument; only CHIRAL_SECRET_KEY is read.
	fs.SetOutput(io.Discard)
	if args[0] == "export" {
		db := fs.String("db", "", "completed SQLite backup")
		dir := fs.String("out", "", "new private output directory")
		publicURL := fs.String("public-url", "", "exact public subscription base URL")
		at := fs.String("at", "", "fixed RFC3339 observation time")
		revision := fs.String("revision", "", "renderer build revision")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *db == "" || *dir == "" {
			fmt.Fprintln(diagnostic, "export requires --db, --out, --public-url, --at and --revision")
			return 1
		}
		when, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			fmt.Fprintln(diagnostic, "a fixed RFC3339 observation time is required")
			return 1
		}
		box, err := secret.NewBox(os.Getenv("CHIRAL_SECRET_KEY"))
		if err != nil {
			fmt.Fprintln(diagnostic, "invalid secret-key configuration")
			return 1
		}
		st, err := store.OpenSnapshot(*db, box)
		if err != nil {
			fmt.Fprintln(diagnostic, "cannot open a compatible read-only SQLite snapshot")
			return 1
		}
		bundle, exportErr := equivalence.Export(st, equivalence.Options{At: when, PublicURL: *publicURL, Revision: *revision})
		closeErr := st.Close()
		if exportErr != nil || closeErr != nil {
			fmt.Fprintln(diagnostic, "offline evidence export failed")
			return 1
		}
		if err := equivalence.Save(*dir, bundle); err != nil {
			fmt.Fprintln(diagnostic, "cannot save new private evidence bundle")
			return 1
		}
		summary := struct {
			Saved    bool `json:"saved"`
			Complete bool `json:"complete"`
			Users    int  `json:"users"`
			Nodes    int  `json:"nodes"`
			Records  int  `json:"records"`
		}{true, bundle.Complete(), bundle.Users, bundle.Nodes, len(bundle.Records)}
		if json.NewEncoder(out).Encode(summary) != nil {
			return 1
		}
		if !summary.Complete {
			return 2
		}
		return 0
	}
	if args[0] == "compare" {
		before := fs.String("before", "", "baseline bundle.json")
		after := fs.String("after", "", "candidate bundle.json")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *before == "" || *after == "" {
			fmt.Fprintln(diagnostic, "compare requires --before and --after")
			return 1
		}
		a, err := equivalence.Load(*before)
		if err != nil {
			fmt.Fprintln(diagnostic, "cannot read baseline evidence")
			return 1
		}
		b, err := equivalence.Load(*after)
		if err != nil {
			fmt.Fprintln(diagnostic, "cannot read candidate evidence")
			return 1
		}
		result := equivalence.Compare(a, b)
		if json.NewEncoder(out).Encode(result) != nil {
			return 1
		}
		if !result.Passed {
			return 2
		}
		return 0
	}
	fmt.Fprintln(diagnostic, "expected export or compare")
	return 1
}
