// Package clierr defines Orbit's standardized CLI error shape: what failed,
// why, and what to do about it. Per Phase 2.1's Product Philosophy, every
// command answers "what should I do next" — that applies to failures too.
// No command should return a bare error like "connection refused"; wrap it
// through this package instead.
package clierr

import (
	"fmt"
	"io"

	"github.com/docker-secret-operator/orbit/internal/cli/output"
)

// Error is a CLI-facing error with a required remediation hint. It
// implements the standard error interface so it composes with %w/errors.Is,
// but callers should construct it with New/Wrap rather than a bare struct
// literal so the ExitCode always gets set deliberately.
type Error struct {
	// What failed, in plain language — no Go type names or internal
	// function names. Shown first, always present.
	What string

	// Why it failed, if known — the underlying cause. May be empty if
	// there's nothing more specific to say than What.
	Why string

	// Action is the recommended next step for the operator. Required —
	// New/Wrap panic if it's empty, because an error with no remediation
	// hint is exactly what this package exists to prevent.
	Action string

	// ExitCode is the process exit code this error should produce.
	// Defaults to output.ExitError if unset via New.
	ExitCode int

	// cause is the wrapped underlying error, if any (for errors.Is/As).
	cause error
}

// New creates a clierr.Error with output.ExitError. Use NewWithCode for a
// different exit code (e.g. output.ExitUnavailable for a Docker connection
// failure).
func New(what, why, action string) *Error {
	return NewWithCode(output.ExitError, what, why, action)
}

// NewWithCode creates a clierr.Error with an explicit exit code.
func NewWithCode(exitCode int, what, why, action string) *Error {
	if action == "" {
		panic("clierr: Action is required — every CLI error must say what to do next")
	}
	return &Error{What: what, Why: why, Action: action, ExitCode: exitCode}
}

// Wrap attaches CLI-facing context to an underlying error (e.g. a raw
// "connection refused" from net.Dial) without discarding it — cause is
// preserved for errors.Is/As and for --json output's "cause" field, but
// never shown as a raw stack trace in human-readable output.
func Wrap(cause error, exitCode int, what, action string) *Error {
	if action == "" {
		panic("clierr: Action is required — every CLI error must say what to do next")
	}
	why := ""
	if cause != nil {
		why = cause.Error()
	}
	return &Error{What: what, Why: why, Action: action, ExitCode: exitCode, cause: cause}
}

// Error implements the error interface. Format matches the human-readable
// rendering used at the top level (main.go), so a clierr.Error printed via
// fmt.Println or via the standard CLI error path look the same.
func (e *Error) Error() string {
	s := "✗ " + e.What
	if e.Why != "" {
		s += ": " + e.Why
	}
	return s
}

// Unwrap allows errors.Is/errors.As to see through to the original cause.
func (e *Error) Unwrap() error { return e.cause }

// JSON returns a stable, machine-readable representation for --json mode.
// Field names are part of Orbit's Stable API Policy once released — do not
// rename without a major version bump.
func (e *Error) JSON() map[string]interface{} {
	m := map[string]interface{}{
		"error":     e.What,
		"action":    e.Action,
		"exit_code": e.ExitCode,
	}
	if e.Why != "" {
		m["reason"] = e.Why
	}
	return m
}

// Print writes the error through p in the correct mode: JSON via p.JSON,
// or human-readable "what / why / action" via p.Human. Commands should call
// this exactly once for a top-level failure, then os.Exit(e.ExitCode).
func Print(p *output.Printer, e *Error) error {
	if p.IsJSON() {
		return p.JSON(e.JSON())
	}
	p.Human(func(w io.Writer) {
		// Best-effort: a failure to write the error message to the
		// terminal isn't itself actionable, so the write error is
		// explicitly discarded rather than compounding the original error.
		_, _ = fmt.Fprintf(w, "✗ %s\n", e.What)
		if e.Why != "" {
			_, _ = fmt.Fprintf(w, "  reason: %s\n", e.Why)
		}
		_, _ = fmt.Fprintf(w, "  → %s\n", e.Action)
	})
	return nil
}
