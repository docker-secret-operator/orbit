package rollout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireComposeOrSkip skips the test if no real `docker compose` is
// available on the test machine, rather than failing the build. This
// package has no existing precedent for a live-Docker-requiring test, but
// `docker compose config` is a static, no-daemon-interaction operation (no
// image pull, no container, no network) — a much smaller cost/flakiness
// profile than a true container-based integration test, and it's the only
// way to genuinely prove resolveComposeProject's behavior rather than
// reimplementing Compose's own resolution rules in a fake.
func requireComposeOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available:", err)
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available:", err)
	}
}

func writeMinimalComposeFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "docker-compose.yml")
	content := "services:\n  web:\n    image: alpine:latest\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// These tests exercise the real resolveComposeProject against a real
// `docker compose config` — no containers are ever created. This is
// deliberate: ADR-0007's Implementation Plan Part 2 established that
// `docker compose config` resolves the project name from the compose
// file's own directory, invocation env, and -f path alone, without
// querying running containers — which is exactly what makes it usable for
// the zero-container bootstrap case (initial deployment, scaled-to-zero,
// recreated deployment: none of these have a running container to inspect,
// but the compose file itself always exists). Every test below is
// simultaneously a bootstrap-scenario proof: none of them ever start a
// container, and resolution still succeeds.

func TestResolveComposeProject_DirectoryDerived(t *testing.T) {
	requireComposeOrSkip(t)

	dir := t.TempDir()
	composeFile := writeMinimalComposeFile(t, dir)

	project, err := resolveComposeProject(context.Background(), composeFile)
	if err != nil {
		t.Fatalf("resolveComposeProject error = %v", err)
	}

	want := strings.ToLower(filepath.Base(dir))
	if project != want {
		t.Errorf("project = %q, want directory-derived %q", project, want)
	}
}

func TestResolveComposeProject_HonorsInheritedComposeProjectNameEnv(t *testing.T) {
	requireComposeOrSkip(t)

	dir := t.TempDir()
	composeFile := writeMinimalComposeFile(t, dir)

	// Orbit's CLI has no explicit -p/--project flag of its own (confirmed
	// by ADR-0007's Implementation Boundary Review §4) — the closest real,
	// already-supported override is whatever COMPOSE_PROJECT_NAME the
	// invoking environment passively provides, which resolveComposeProject
	// must pass through unmodified (never overridden, never re-derived).
	t.Setenv("COMPOSE_PROJECT_NAME", "envtest-project")

	project, err := resolveComposeProject(context.Background(), composeFile)
	if err != nil {
		t.Fatalf("resolveComposeProject error = %v", err)
	}
	if project != "envtest-project" {
		t.Errorf("project = %q, want %q", project, "envtest-project")
	}
}

func TestResolveComposeProject_HonorsDotEnvComposeProjectName(t *testing.T) {
	requireComposeOrSkip(t)

	dir := t.TempDir()
	composeFile := writeMinimalComposeFile(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMPOSE_PROJECT_NAME=dotenv-project\n"), 0600); err != nil {
		t.Fatal(err)
	}

	project, err := resolveComposeProject(context.Background(), composeFile)
	if err != nil {
		t.Fatalf("resolveComposeProject error = %v", err)
	}
	if project != "dotenv-project" {
		t.Errorf("project = %q, want %q", project, "dotenv-project")
	}
}

func TestResolveComposeProject_MissingComposeFile_ReturnsClearError(t *testing.T) {
	requireComposeOrSkip(t)

	_, err := resolveComposeProject(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent compose file, got nil")
	}
	if !strings.Contains(err.Error(), "resolve compose project") {
		t.Errorf("error should clearly name the failed operation, got: %v", err)
	}
}
