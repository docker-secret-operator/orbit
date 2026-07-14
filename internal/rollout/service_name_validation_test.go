package rollout

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// explodingRuntime fails the test if any method is called — proving
// validation short-circuits runWithDeps before any exec.Command call is
// ever reachable, regardless of which Runtime implementation is wired in.
type explodingRuntime struct{ t *testing.T }

func (e explodingRuntime) Pull(context.Context, string, string) error {
	e.t.Fatal("Pull should never be called for an invalid service name")
	return nil
}
func (e explodingRuntime) ServiceReplicaCount(context.Context, string) (int, error) {
	e.t.Fatal("ServiceReplicaCount should never be called for an invalid service name")
	return 0, nil
}
func (e explodingRuntime) ScaleService(context.Context, string, string, int) error {
	e.t.Fatal("ScaleService should never be called for an invalid service name")
	return nil
}
func (e explodingRuntime) WaitForNewContainer(context.Context, Options, *zap.Logger) (string, string, error) {
	e.t.Fatal("WaitForNewContainer should never be called for an invalid service name")
	return "", "", nil
}
func (e explodingRuntime) FindOldContainer(context.Context, string, string) (string, error) {
	e.t.Fatal("FindOldContainer should never be called for an invalid service name")
	return "", nil
}
func (e explodingRuntime) ContainerAddr(context.Context, string) (string, error) {
	e.t.Fatal("ContainerAddr should never be called for an invalid service name")
	return "", nil
}
func (e explodingRuntime) RemoveContainer(context.Context, string) error {
	e.t.Fatal("RemoveContainer should never be called for an invalid service name")
	return nil
}
func (e explodingRuntime) VerifyStable(context.Context, string, time.Duration) error {
	e.t.Fatal("VerifyStable should never be called for an invalid service name")
	return nil
}

// TestRunWithDeps_RejectsServiceNameLookingLikeAFlag closes the go-live
// audit's finding M5: internal/rollout shells out to `docker compose` with
// the service name as the final, unguarded positional argument
// (scaleService, composeRun for Pull). A compose file service key starting
// with "-" would be parsed by docker compose's own flag parser as a flag on
// the up/pull subcommand instead of a service name — e.g. a service
// literally named "--force-recreate" could cause docker compose to recreate
// unrelated containers in the same project. runWithDeps must reject an
// unsafe service name before any Runtime method — and therefore any
// exec.Command call — is ever reached.
func TestRunWithDeps_RejectsServiceNameLookingLikeAFlag(t *testing.T) {
	opts := Options{
		ComposeFile: "docker-rollout-compose.yml",
		Service:     "--force-recreate",
	}
	opts.defaults()

	deps := runDeps{runtime: explodingRuntime{t: t}}

	err := runWithDeps(context.Background(), opts, zap.NewNop(), deps)
	if err == nil {
		t.Fatal("runWithDeps should reject a service name that looks like a CLI flag")
	}
	if !strings.Contains(err.Error(), "--force-recreate") {
		t.Errorf("error should name the rejected service, got: %v", err)
	}
}
