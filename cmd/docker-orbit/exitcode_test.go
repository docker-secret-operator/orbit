package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"go.uber.org/zap"
)

// TestHelperProcess_CLI is not a real test: it's the subprocess entrypoint
// used by runCLISubprocess below. renderCLIErr (deploy.go, rollback.go,
// recover.go, status.go) calls os.Exit directly, which would kill the whole
// `go test` binary if invoked in-process — the standard Go pattern is to
// re-exec the test binary itself with a guard env var and read back its real
// exit code, matching the "subprocess-based tests" this package's other test
// files (deploy_test.go, status_test.go, doctor_test.go) already refer to by
// name (exitcode_test.go) without this file having existed until now.
func TestHelperProcess_CLI(t *testing.T) {
	if os.Getenv("ORBIT_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	root := buildRoot(zap.NewNop())
	root.SetArgs(args)
	_ = root.Execute() // errors already printed via renderCLIErr/cobra; exit code is what we check
}

// runCLISubprocess runs `docker-orbit <args...>` as a real subprocess and
// returns its combined stdout, stderr, and exit code.
func runCLISubprocess(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestHelperProcess_CLI", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...) //nolint:gosec
	cmd.Env = append(os.Environ(), "ORBIT_TEST_HELPER=1")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("subprocess failed to start: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}

func TestDeploy_MissingComposeFile_ExitsConfig(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runCLISubprocess(t, "deploy", "web",
		"--file", filepath.Join(dir, "does-not-exist.yml"), "--json")
	if code != output.ExitConfig {
		t.Errorf("exit code = %d, want ExitConfig (%d)\nstdout: %s\nstderr: %s", code, output.ExitConfig, stdout, stderr)
	}
}

func TestDeploy_ServiceNotInComposeFile_ExitsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-rollout-compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  other:\n    image: x:1\n    ports: [\"3000:3000\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLISubprocess(t, "deploy", "web", "--file", path, "--json")
	if code != output.ExitConfig {
		t.Errorf("exit code = %d, want ExitConfig (%d)\nstdout: %s\nstderr: %s", code, output.ExitConfig, stdout, stderr)
	}
	if !strings.Contains(stdout, "web") {
		t.Errorf("stdout should mention the missing service: %s", stdout)
	}
}

func TestRollback_NoStateRecorded_ExitsConfig(t *testing.T) {
	// A service name unlikely to have a real /tmp/orbit-<service>-state.json
	// left over from any other test or real rollout on this machine.
	stdout, stderr, code := runCLISubprocess(t, "rollback", "no-such-service-exitcode-test", "--json")
	if code != output.ExitConfig {
		t.Errorf("exit code = %d, want ExitConfig (%d)\nstdout: %s\nstderr: %s", code, output.ExitConfig, stdout, stderr)
	}
	if !strings.Contains(stdout, "no rollback state") {
		t.Errorf("stdout should explain no state was recorded: %s", stdout)
	}
}

func TestRecover_ProxyUnreachable_ExitsUnavailable(t *testing.T) {
	stdout, stderr, code := runCLISubprocess(t, "recover",
		"--control-addr", "http://127.0.0.1:1", "--timeout", "2s", "--json")
	if code != output.ExitUnavailable {
		t.Errorf("exit code = %d, want ExitUnavailable (%d)\nstdout: %s\nstderr: %s", code, output.ExitUnavailable, stdout, stderr)
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Errorf("stdout should explain the proxy is unreachable: %s", stdout)
	}
}

func TestDeploy_UnreachableProxy_ExitsUnavailable(t *testing.T) {
	// A valid compose file + service passes the compose-parse checks, so this
	// exercises the preflight-failure path specifically: with no real proxy
	// listening at the given control-addr, "Proxy reachable" cannot PASS, and
	// deployPreflightPassed treats that as a hard block for deploy (unlike a
	// general `doctor` run, where it's only a WARNING).
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-rollout-compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  web:\n    image: x:1\n    ports: [\"3000:3000\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLISubprocess(t, "deploy", "web",
		"--file", path, "--control-addr", "http://127.0.0.1:1", "--json")
	if code != output.ExitUnavailable {
		t.Errorf("exit code = %d, want ExitUnavailable (%d)\nstdout: %s\nstderr: %s", code, output.ExitUnavailable, stdout, stderr)
	}
}
