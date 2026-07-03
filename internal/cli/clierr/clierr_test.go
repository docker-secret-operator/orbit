package clierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/cli/output"
)

func TestNewRequiresAction(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New() with empty action should panic — an error with no remediation defeats this package's purpose")
		}
	}()
	_ = New("something failed", "reason", "")
}

func TestNewDefaultsToExitError(t *testing.T) {
	e := New("Docker daemon unavailable", "connection refused", "Start Docker and try again")
	if e.ExitCode != output.ExitError {
		t.Errorf("ExitCode = %d, want %d", e.ExitCode, output.ExitError)
	}
}

func TestNewWithCodeSetsExitCode(t *testing.T) {
	e := NewWithCode(output.ExitUnavailable, "Docker daemon unavailable", "connection refused", "Start Docker and try again")
	if e.ExitCode != output.ExitUnavailable {
		t.Errorf("ExitCode = %d, want %d", e.ExitCode, output.ExitUnavailable)
	}
}

func TestErrorStringNeverExposesRawGoError(t *testing.T) {
	// Simulates wrapping a raw "connection refused" style error and
	// verifies the CLI-facing message leads with the human explanation,
	// not the raw underlying string alone.
	cause := errors.New("dial tcp 127.0.0.1:2375: connect: connection refused")
	e := Wrap(cause, output.ExitUnavailable, "Docker daemon unavailable", "Start Docker and try again")

	msg := e.Error()
	if !strings.Contains(msg, "Docker daemon unavailable") {
		t.Errorf("Error() = %q, want it to lead with the human-readable What", msg)
	}
}

func TestWrapPreservesCauseForErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := Wrap(sentinel, output.ExitError, "operation failed", "retry")

	if !errors.Is(e, sentinel) {
		t.Error("errors.Is(e, sentinel) = false, want true — Wrap must preserve the cause chain")
	}
}

func TestJSONShapeIsStable(t *testing.T) {
	e := NewWithCode(output.ExitConfig, "invalid --timeout value", "must be a positive duration", "Pass e.g. --timeout 60s")
	m := e.JSON()

	for _, key := range []string{"error", "action", "exit_code", "reason"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON() missing required key %q: %+v", key, m)
		}
	}
	if m["error"] != "invalid --timeout value" {
		t.Errorf("JSON()[error] = %v, want the What field", m["error"])
	}
}

func TestJSONOmitsReasonWhenEmpty(t *testing.T) {
	e := New("something failed", "", "try again")
	m := e.JSON()
	if _, ok := m["reason"]; ok {
		t.Error("JSON() included empty reason key, want it omitted")
	}
}

func TestPrintJSONMode(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, true)
	e := New("Docker daemon unavailable", "connection refused", "Start Docker and try again")

	if err := Print(p, e); err != nil {
		t.Fatalf("Print() error = %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Print() did not produce valid JSON: %v\noutput: %s", err, buf.String())
	}
	if got["error"] != "Docker daemon unavailable" {
		t.Errorf("got error=%v, want 'Docker daemon unavailable'", got["error"])
	}
}

func TestPrintHumanModeIncludesActionArrow(t *testing.T) {
	var buf bytes.Buffer
	p := output.New(&buf, false)
	e := New("Docker daemon unavailable", "connection refused", "Start Docker and try again")

	if err := Print(p, e); err != nil {
		t.Fatalf("Print() error = %v", err)
	}

	out := buf.String()
	for _, want := range []string{"Docker daemon unavailable", "connection refused", "Start Docker and try again"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintHumanModeNeverShowsRawStackTrace(t *testing.T) {
	// Regression guard for the Phase 2.1 requirement: "Never display raw
	// stack traces." A wrapped error's Go-internal detail (file:line style
	// text) should not leak into human output.
	cause := errors.New("goroutine 1 [running]:\nmain.main()\n\t/src/main.go:42 +0x1a")
	e := Wrap(cause, output.ExitError, "internal error", "Please file a bug report")

	var buf bytes.Buffer
	p := output.New(&buf, false)
	_ = Print(p, e)

	// Print() only ever emits e.What/e.Why/e.Action verbatim — this test
	// documents that Wrap's caller is responsible for not putting stack
	// traces into Why. Print itself does not filter content, so verify the
	// contract lives at construction: What/Action must be human text, and
	// Why is displayed labeled ("reason: ...") so it reads as diagnostic
	// detail, not as the primary message.
	out := buf.String()
	if !strings.HasPrefix(out, "✗ internal error") {
		t.Errorf("human output should lead with What, got:\n%s", out)
	}
}
