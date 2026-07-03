// Package plugin implements the Docker CLI plugin interface for Orbit.
//
// Docker CLI plugins are binaries placed in ~/.docker/cli-plugins/ and named
// docker-<name>. When Docker invokes the plugin it sets argv[0] to the binary
// name and prepends the subcommand arguments. The plugin must:
//
//  1. Respond to "docker-cli-plugin-metadata" with plugin metadata JSON.
//  2. Respond to all other subcommands as defined by the Cobra command tree.
//
// Orbit ships a single binary, docker-orbit, that serves both as
// the standalone CLI and as the `docker orbit` plugin. The mode is detected
// via argv[0] or the DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND environment
// variable.
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Metadata is the JSON structure required by the Docker CLI plugin contract.
// See: https://github.com/docker/cli/blob/master/cli-plugins/plugin/plugin.go
type Metadata struct {
	SchemaVersion    string `json:"SchemaVersion"`
	Vendor           string `json:"Vendor"`
	Version          string `json:"Version"`
	ShortDescription string `json:"ShortDescription"`
	URL              string `json:"URL"`
}

// DefaultMetadata returns the Orbit plugin metadata.
func DefaultMetadata(version string) Metadata {
	return Metadata{
		SchemaVersion:    "0.1.0",
		Vendor:           "Orbit",
		Version:          version,
		ShortDescription: "Zero-downtime deployments for Docker Compose",
		URL:              "https://github.com/docker-secret-operator/orbit",
	}
}

// IsDockerPluginMode returns true when the binary is invoked as a Docker CLI
// plugin (argv[0] == "docker-orbit" or the Docker CLI plugin env is set).
func IsDockerPluginMode() bool {
	base := filepath.Base(os.Args[0])
	return base == "docker-orbit" ||
		os.Getenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND") != ""
}

// HandleMetadataRequest checks whether the first argument is the special
// Docker CLI metadata probe ("docker-cli-plugin-metadata") and, if so,
// prints the metadata JSON and returns true. The caller should exit(0)
// after this returns true.
func HandleMetadataRequest(version string) bool {
	if len(os.Args) < 2 || os.Args[1] != "docker-cli-plugin-metadata" {
		return false
	}
	m := DefaultMetadata(version)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		fmt.Fprintf(os.Stderr, "docker-orbit plugin: encode metadata: %v\n", err)
		os.Exit(1)
	}
	return true
}

// StripPluginArgs removes the Docker-injected prefix when Orbit runs as a
// `docker orbit ...` plugin, leaving Cobra only the real subcommand arguments.
//
// Docker invokes the plugin as:
//
//	docker-orbit [docker global flags...] orbit <args...>
//
// e.g. `docker --context prod orbit deploy web` reaches the binary as
// argv = [docker-orbit, --context, prod, orbit, deploy, web]. Everything from
// argv[1] up to and including the plugin-name token ("orbit") belongs to
// Docker, not to Orbit; only <args...> may reach Cobra. Stripping merely
// argv[1]=="orbit" (the previous behavior) broke whenever Docker forwarded any
// global flag before the plugin name.
//
// Plugin mode is detected via DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND, which
// Docker sets when it invokes a plugin. argv[0] is deliberately NOT used: the
// standalone binary is also named docker-orbit, and a standalone user may pass
// "orbit" as an ordinary argument (e.g. a service named "orbit"), which must
// never be stripped.
func StripPluginArgs() {
	if os.Getenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND") == "" {
		return // standalone invocation — leave argv untouched
	}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "orbit" {
			os.Args = append(os.Args[:1], os.Args[i+1:]...)
			return
		}
	}
}
