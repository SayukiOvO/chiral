// 3xui-audit inventories known configuration hazards offline. It never opens
// a database or provider connection and never authorizes node takeover.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/SayukiOvO/chiral/agent/internal/threexui"
)

const maxInputBytes = 4 << 20

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, input io.Reader, out, diagnostic io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(diagnostic, "read one local configuration from standard input; no arguments are accepted")
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(input, maxInputBytes+1))
	if err != nil {
		fmt.Fprintln(diagnostic, "cannot read configuration input")
		return 1
	}
	result, err := threexui.AssessConfigCompatibility(data)
	if err != nil {
		// Do not expose parser diagnostics, source content or input paths.
		fmt.Fprintln(diagnostic, "cannot assess configuration: invalid, ambiguous, unsupported inventory shape or oversized input")
		return 1
	}
	if json.NewEncoder(out).Encode(result) != nil {
		fmt.Fprintln(diagnostic, "cannot write assessment")
		return 1
	}
	// A complete offline inventory is not runtime acceptance. Exit 2 makes
	// that distinction explicit even when there are no known findings.
	return 2
}
