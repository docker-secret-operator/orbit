package rollout

import "testing"

// dockerPSFilterArgs is the one place every raw `docker ps` discovery call
// in this package (findOldContainer, serviceReplicaCount,
// inspectNewestHealthy) builds its filter arguments — this test proves the
// project dimension is always present alongside the pre-existing service
// dimension, without needing a real Docker daemon.
func TestDockerPSFilterArgs_IncludesProjectAndService(t *testing.T) {
	args := dockerPSFilterArgs("proj-a", "web")

	wantProject := "label=com.docker.compose.project=proj-a"
	wantService := "label=com.docker.compose.service=web"

	if !containsArg(args, wantProject) {
		t.Errorf("filter args %v missing project filter %q", args, wantProject)
	}
	if !containsArg(args, wantService) {
		t.Errorf("filter args %v missing service filter %q", args, wantService)
	}
}

func containsArg(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
