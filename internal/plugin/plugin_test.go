package plugin

import (
	"os"
	"reflect"
	"testing"
)

// withArgs replaces os.Args for the duration of a test and restores it after.
func withArgs(t *testing.T, args []string) {
	t.Helper()
	orig := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = orig })
}

// TestStripPluginArgs_DockerGlobalFlagsBeforePluginName is the regression test
// for the real bug found during Phase 3.1 distribution validation: Docker
// invokes a CLI plugin as `docker-orbit [docker global flags...] orbit <args>`,
// injecting its own global flags (e.g. --config, --context, -H) BEFORE the
// plugin-name token. Stripping only argv[1]=="orbit" broke on any global flag,
// so `docker --context x orbit deploy web` failed with `unknown command
// "orbit"`. The strip must remove everything up to and including "orbit".
func TestStripPluginArgs_DockerGlobalFlagsBeforePluginName(t *testing.T) {
	t.Setenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND", "docker")
	withArgs(t, []string{"docker-orbit", "--config", "/home/u/.docker", "orbit", "deploy", "web"})

	StripPluginArgs()

	want := []string{"docker-orbit", "deploy", "web"}
	if !reflect.DeepEqual(os.Args, want) {
		t.Errorf("os.Args = %v, want %v", os.Args, want)
	}
}

func TestStripPluginArgs_PlainPluginInvocation(t *testing.T) {
	t.Setenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND", "docker")
	withArgs(t, []string{"docker-orbit", "orbit", "status"})

	StripPluginArgs()

	want := []string{"docker-orbit", "status"}
	if !reflect.DeepEqual(os.Args, want) {
		t.Errorf("os.Args = %v, want %v", os.Args, want)
	}
}

// TestStripPluginArgs_StandaloneNeverStrips guards the standalone binary: it is
// also named docker-orbit, so plugin mode must be detected by the env var, not
// the name. A standalone user may legitimately pass "orbit" as an argument
// (e.g. deploying a service literally named "orbit"); it must not be stripped.
func TestStripPluginArgs_StandaloneNeverStrips(t *testing.T) {
	os.Unsetenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND")
	withArgs(t, []string{"docker-orbit", "deploy", "orbit"}) // service named "orbit"

	StripPluginArgs()

	want := []string{"docker-orbit", "deploy", "orbit"}
	if !reflect.DeepEqual(os.Args, want) {
		t.Errorf("standalone args must be untouched: os.Args = %v, want %v", os.Args, want)
	}
}

func TestStripPluginArgs_StandalonePlain(t *testing.T) {
	os.Unsetenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND")
	withArgs(t, []string{"docker-orbit", "doctor"})

	StripPluginArgs()

	want := []string{"docker-orbit", "doctor"}
	if !reflect.DeepEqual(os.Args, want) {
		t.Errorf("os.Args = %v, want %v", os.Args, want)
	}
}

func TestHandleMetadataRequest(t *testing.T) {
	withArgs(t, []string{"docker-orbit", "docker-cli-plugin-metadata"})
	if !HandleMetadataRequest("1.2.3") {
		t.Error("expected metadata request to be handled for the probe subcommand")
	}

	withArgs(t, []string{"docker-orbit", "version"})
	if HandleMetadataRequest("1.2.3") {
		t.Error("non-probe subcommand must not be treated as a metadata request")
	}
}
