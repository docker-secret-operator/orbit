// Package output provides the shared rendering layer every Orbit CLI command
// uses: consistent human-readable formatting, stable JSON output, and
// standardized exit codes. No command should implement its own ad hoc
// printing or exit-code logic — see CONSTITUTION.md's "Explicit Behavior
// Over Magic" and "Small, Focused Components" principles.
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Exit codes are stable across releases per CONSTITUTION.md's Stable API
// Policy (CLI commands and arguments). Scripts and CI pipelines may depend
// on these — do not renumber them.
const (
	ExitOK          = 0 // Command completed successfully.
	ExitError       = 1 // Generic failure (see the error's own message for detail).
	ExitConfig      = 2 // Configuration or argument problem — user input error.
	ExitUnavailable = 3 // A required dependency (Docker, the proxy) is unreachable.
	ExitDegraded    = 4 // Command succeeded but found a WARNING/ERROR condition
	// (used by `status` and `doctor` when the system is reachable
	// but not fully healthy — distinct from ExitError, which means
	// the command itself failed to run).
)

// Printer renders command results consistently across human and JSON modes.
// Every new command should construct exactly one Printer and route all
// output through it — see internal/cli/output/output_test.go for the
// contract this type guarantees.
type Printer struct {
	w    io.Writer
	json bool
}

// New returns a Printer writing to w. When json is true, JSON renders and
// Human is a no-op; when false, the reverse.
func New(w io.Writer, json bool) *Printer {
	return &Printer{w: w, json: json}
}

// IsJSON reports whether this Printer is in JSON mode.
func (p *Printer) IsJSON() bool { return p.json }

// JSON encodes v as indented JSON terminated with a newline. Field order is
// determined by v's struct definition (or, for maps, alphabetically by
// Go's encoding/json) — both are deterministic, which is what "stable JSON
// output" means here: the same underlying data always serializes to
// byte-identical output, not that the output never changes between calls.
func (p *Printer) JSON(v interface{}) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("output: encode JSON: %w", err)
	}
	return nil
}

// Human calls render only when the Printer is in human-readable mode. Pass
// a closure that writes formatted text to the given writer. In JSON mode
// this is a no-op, so callers can write:
//
//	p.Human(func(w io.Writer) { fmt.Fprintln(w, "...") })
//	if p.IsJSON() { return p.JSON(result) }
func (p *Printer) Human(render func(w io.Writer)) {
	if p.json {
		return
	}
	render(p.w)
}

// Writer exposes the underlying writer for commands that need direct access
// (e.g. tabwriter-based tables) while still respecting JSON mode via IsJSON.
func (p *Printer) Writer() io.Writer { return p.w }
