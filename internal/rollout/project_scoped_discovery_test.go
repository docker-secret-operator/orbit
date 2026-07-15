package rollout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestServiceReplicaCount_And_FindOldContainer_ScopeToOwnProject is the
// required regression test proving Rollout's raw discovery calls never
// cross Compose projects: two real Compose projects, each with a service
// literally named "web" (identical service label on both), only the
// project label distinguishing them. serviceReplicaCount and
// findOldContainer must each see only their own project's container.
//
// Real Docker, real containers — not a simulation. Skipped if Docker/Compose
// isn't available, matching this file's siblings.
func TestServiceReplicaCount_And_FindOldContainer_ScopeToOwnProject(t *testing.T) {
	requireComposeOrSkip(t)

	ctx := context.Background()

	projA := setupComposeProject(t, "rollout-scope-a", "web")
	projB := setupComposeProject(t, "rollout-scope-b", "web")

	// Both projects run a container for the identical service name "web" —
	// exactly ADR-0007's required "two projects, same service" scenario.
	countA, err := serviceReplicaCount(ctx, projA.project, "web")
	if err != nil {
		t.Fatalf("serviceReplicaCount(project A): %v", err)
	}
	if countA != 1 {
		t.Errorf("serviceReplicaCount(project A) = %d, want 1 (must not count project B's container)", countA)
	}

	countB, err := serviceReplicaCount(ctx, projB.project, "web")
	if err != nil {
		t.Fatalf("serviceReplicaCount(project B): %v", err)
	}
	if countB != 1 {
		t.Errorf("serviceReplicaCount(project B) = %d, want 1 (must not count project A's container)", countB)
	}

	// findOldContainer scoped to project A must resolve to project A's own
	// container, never project B's — even passing an obviously-foreign
	// "newID" so the only candidate left, if scoping failed, would be
	// project B's container.
	oldA, err := findOldContainer(ctx, projA.project, "web", "not-a-real-container-id")
	if err != nil {
		t.Fatalf("findOldContainer(project A): %v", err)
	}
	if oldA != projA.containerID {
		t.Errorf("findOldContainer(project A) = %q, want project A's own container %q (cross-project leak if this is project B's: %q)",
			oldA, projA.containerID, projB.containerID)
	}
}

// TestServiceReplicaCount_ZeroContainers_ReturnsZeroNotError proves the
// scaled-to-zero / initial-deployment bootstrap case: a project/service
// combination with no running containers must resolve to a replica count
// of 0, never an error — Run's Step 2 (scale +1) depends on this to
// compute the correct target replica count on a service's very first
// rollout, before any container for it has ever existed.
func TestServiceReplicaCount_ZeroContainers_ReturnsZeroNotError(t *testing.T) {
	requireComposeOrSkip(t)

	count, err := serviceReplicaCount(context.Background(), "orbit-pr-b-nonexistent-project", "web")
	if err != nil {
		t.Fatalf("serviceReplicaCount error = %v, want nil for a project/service with zero containers", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

type composeProjectFixture struct {
	project     string
	containerID string
}

// setupComposeProject brings up a minimal one-service Compose project
// labeled exactly as Orbit's generator would, and tears it down via
// t.Cleanup. project is an explicit COMPOSE_PROJECT_NAME (not directory-
// derived) so two fixtures in the same test run never collide.
func setupComposeProject(t *testing.T, project, service string) composeProjectFixture {
	t.Helper()

	dir := t.TempDir()
	composeFile := filepath.Join(dir, "docker-compose.yml")
	content := "services:\n  " + service + ":\n    image: alpine:latest\n    command: [\"sleep\", \"300\"]\n"
	if err := os.WriteFile(composeFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "compose", "-p", project, "-f", composeFile, "up", "-d")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose up (project %s): %v\n%s", project, err, out)
	}
	t.Cleanup(func() {
		downCmd := exec.Command("docker", "compose", "-p", project, "-f", composeFile, "down")
		_ = downCmd.Run()
	})

	idOut, err := exec.Command("docker", "ps",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service="+service,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		t.Fatalf("docker ps (project %s): %v", project, err)
	}
	containerID := string(idOut)
	for len(containerID) > 0 && (containerID[len(containerID)-1] == '\n' || containerID[len(containerID)-1] == '\r') {
		containerID = containerID[:len(containerID)-1]
	}
	if containerID == "" {
		t.Fatalf("no container found for project %s after compose up", project)
	}

	return composeProjectFixture{project: project, containerID: containerID}
}
