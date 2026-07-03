package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

type sample struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestJSONModeEncodesStruct(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, true)

	if err := p.JSON(sample{Name: "web", Count: 3}); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}

	var got sample
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if got != (sample{Name: "web", Count: 3}) {
		t.Errorf("got %+v, want {web 3}", got)
	}
}

func TestJSONOutputIsStableAcrossCalls(t *testing.T) {
	v := sample{Name: "web", Count: 3}

	var buf1, buf2 bytes.Buffer
	if err := New(&buf1, true).JSON(v); err != nil {
		t.Fatal(err)
	}
	if err := New(&buf2, true).JSON(v); err != nil {
		t.Fatal(err)
	}

	if buf1.String() != buf2.String() {
		t.Errorf("same input produced different JSON output:\n%s\nvs\n%s", buf1.String(), buf2.String())
	}
}

func TestJSONModeSuppressesHuman(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, true)

	called := false
	p.Human(func(w io.Writer) {
		called = true
	})

	if called {
		t.Error("Human() render function was called in JSON mode, want no-op")
	}
	if buf.Len() != 0 {
		t.Errorf("buffer should be empty in JSON mode with only Human() called, got %q", buf.String())
	}
}

func TestHumanModeSuppressesJSON_ButJSONStillCallableExplicitly(t *testing.T) {
	// Human() is a no-op in JSON mode and vice versa is NOT enforced by
	// Human — callers decide which to call based on IsJSON(). This test
	// documents that Human() runs its closure in human mode.
	var buf bytes.Buffer
	p := New(&buf, false)

	p.Human(func(w io.Writer) {
		_, _ = fmt.Fprint(w, "hello")
	})

	if buf.String() != "hello" {
		t.Errorf("Human() did not write in human mode, got %q", buf.String())
	}
}

func TestIsJSON(t *testing.T) {
	if !New(&bytes.Buffer{}, true).IsJSON() {
		t.Error("IsJSON() = false, want true")
	}
	if New(&bytes.Buffer{}, false).IsJSON() {
		t.Error("IsJSON() = true, want false")
	}
}

func TestJSONEncodeErrorIsWrapped(t *testing.T) {
	p := New(&bytes.Buffer{}, true)
	// channels are not JSON-marshalable — forces an encode error.
	err := p.JSON(make(chan int))
	if err == nil {
		t.Fatal("expected error encoding unmarshalable value, got nil")
	}
	if !strings.Contains(err.Error(), "output: encode JSON") {
		t.Errorf("error not wrapped with package context: %v", err)
	}
}

func TestExitCodesAreDistinct(t *testing.T) {
	codes := []int{ExitOK, ExitError, ExitConfig, ExitUnavailable, ExitDegraded}
	seen := make(map[int]bool)
	for _, c := range codes {
		if seen[c] {
			t.Errorf("exit code %d used more than once", c)
		}
		seen[c] = true
	}
}
