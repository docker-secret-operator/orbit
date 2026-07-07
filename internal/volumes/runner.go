package volumes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// commandRunner abstracts external process execution so it can be faked in tests.
type commandRunner interface {
	Run(ctx context.Context, stdout io.Writer, name string, args ...string) error
}

// execCommandRunner runs real OS commands.
type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
