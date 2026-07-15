package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/compose"
	"github.com/docker-secret-operator/orbit/internal/history"
	dockerclient "github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// CheckStatus classifies a single doctor check's outcome.
type CheckStatus string

const (
	StatusPass CheckStatus = "PASS"
	StatusWarn CheckStatus = "WARNING"
	StatusFail CheckStatus = "ERROR"
	// StatusSkip marks a check that didn't run because an earlier check it
	// depends on already failed — distinct from StatusWarn so one root cause
	// (e.g. a missing compose file) doesn't inflate the warning count for
	// every check downstream of it.
	StatusSkip CheckStatus = "SKIPPED"
)

// Check is one doctor diagnostic result. Detail is a plain-language
// explanation; Remediation is required whenever Status is not PASS — see
// Phase 2.1's Product Philosophy: every command should say what to do next.
type Check struct {
	Name        string      `json:"name"`
	Status      CheckStatus `json:"status"`
	Detail      string      `json:"detail"`
	Remediation string      `json:"remediation,omitempty"`
}

// DoctorReport is the full output of `docker orbit doctor`.
type DoctorReport struct {
	Timestamp time.Time `json:"timestamp"`
	Checks    []Check   `json:"checks"`
	Summary   struct {
		Pass    int `json:"pass"`
		Warning int `json:"warning"`
		Error   int `json:"error"`
		Skipped int `json:"skipped"`
	} `json:"summary"`
}

// doctorCmd runs a comprehensive, real health audit — every check here
// performs an actual probe (Docker ping, HTTP request, filesystem access)
// against the real system. No check is simulated; a check that can't run
// (e.g. no compose file in the working directory) reports WARNING with an
// honest explanation, not a fabricated PASS.
func doctorCmd(log *zap.Logger) *cobra.Command {
	var controlAddr, composeFile, project string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run a comprehensive health check of the Orbit installation",
		Long: `Doctor verifies the pieces Orbit depends on: the Docker Engine, the
compose file, the running proxy (if any), state directory permissions, and
this binary's plugin installation. Every check reports PASS, WARNING, or
ERROR with a concrete next step for anything that isn't PASS — never a raw
stack trace.

Example:
  docker orbit doctor
  docker orbit doctor --json
  docker orbit doctor --control-addr http://localhost:9901
  docker orbit doctor --file docker-compose.prod.yml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := output.New(cmd.OutOrStdout(), jsonOut)
			report := runDoctorChecks(cmd.Context(), controlAddr, composeFile, resolveProject(project))

			if p.IsJSON() {
				return p.JSON(report)
			}
			p.Human(func(w io.Writer) { renderDoctorHuman(w, report) })

			if report.Summary.Error > 0 {
				os.Exit(output.ExitDegraded)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&controlAddr, "control-addr", "http://localhost:9900", "Proxy control API address")
	cmd.Flags().StringVar(&composeFile, "file", "docker-compose.yml", "Compose file to validate")
	cmd.Flags().StringVar(&project, "project", "", "Service/project name (default: $ORBIT_PROXY_INSTANCE, else \"default\")")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runDoctorChecks(ctx context.Context, controlAddr, composeFile, service string) DoctorReport {
	report := DoctorReport{Timestamp: time.Now().UTC()}

	checks := []Check{
		checkDockerEngine(ctx),
		checkComposeAvailable(ctx),
		checkComposeFile(composeFile),
		checkControlAddr(controlAddr),
		checkProxyReachable(controlAddr),
		checkProxyReady(controlAddr),
		checkPortsAvailable(composeFile),
		checkRecoveryState(controlAddr),
		checkStateDirWritable(),
		checkPluginInstalled(),
	}

	for _, c := range checks {
		report.Checks = append(report.Checks, c)
		switch c.Status {
		case StatusPass:
			report.Summary.Pass++
		case StatusWarn:
			report.Summary.Warning++
		case StatusFail:
			report.Summary.Error++
		case StatusSkip:
			report.Summary.Skipped++
		}
	}
	return report
}

// ── Individual checks ─────────────────────────────────────────────────────────

func checkDockerEngine(ctx context.Context) Check {
	cl, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return Check{Name: "Docker Engine reachable", Status: StatusFail,
			Detail:      "could not create Docker client: " + err.Error(),
			Remediation: "Check DOCKER_HOST is set correctly, or that /var/run/docker.sock exists and is accessible"}
	}
	defer cl.Close() //nolint:errcheck // best-effort cleanup of a short-lived diagnostic client

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := cl.Ping(pingCtx); err != nil {
		return Check{Name: "Docker Engine reachable", Status: StatusFail,
			Detail:      "Docker daemon did not respond: " + err.Error(),
			Remediation: "Start Docker (systemctl start docker, or open Docker Desktop) and try again"}
	}
	return Check{Name: "Docker Engine reachable", Status: StatusPass, Detail: "Docker daemon responded to ping"}
}

// checkComposeAvailable verifies the `docker compose` (v2) CLI plugin is
// installed and runnable — distinct from checkComposeFile, which validates a
// specific compose *file*, not the tooling needed to actually run it. Orbit
// requires Compose v2 (the `docker compose` subcommand); the legacy
// standalone `docker-compose` (v1) binary is not sufficient.
func checkComposeAvailable(ctx context.Context) Check {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cmdCtx, "docker", "compose", "version").Output() //nolint:gosec // fixed argv, no user input
	if err != nil {
		return Check{Name: "Docker Compose available", Status: StatusFail,
			Detail:      "`docker compose version` failed: " + err.Error(),
			Remediation: "Install Docker Compose v2 (bundled with current Docker Desktop, or `apt/dnf install docker-compose-plugin` on Linux) — the legacy standalone docker-compose (v1) is not supported"}
	}
	return Check{Name: "Docker Compose available", Status: StatusPass,
		Detail: strings.TrimSpace(string(out))}
}

// checkPortsAvailable verifies the host ports the compose file declares are
// not already occupied by something other than Orbit's own proxy. A bound
// port is reported WARNING rather than ERROR: it's the expected, healthy
// state once a service is deployed (the proxy legitimately holds the port
// forever, per CONSTITUTION.md's zero-downtime guarantee) as much as it's a
// sign of conflict before first deploy — this check can't distinguish the
// two from the host side alone, so it says so rather than guessing.
func checkPortsAvailable(composeFilePath string) Check {
	cf, err := compose.ParseFile(composeFilePath)
	if err != nil {
		return Check{Name: "Required ports available", Status: StatusSkip,
			Detail:      "skipped — depends on a valid compose file (see 'Compose file' check above)",
			Remediation: "Resolve the compose file issue above, then re-run doctor"}
	}

	var busy []string
	for _, svc := range cf.Services {
		for _, p := range svc.Ports {
			host, _, err := compose.ParsePort(p)
			if err != nil {
				continue // malformed port strings are Compose file's job to flag, not this check's
			}
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", host))
			if err != nil {
				busy = append(busy, fmt.Sprintf("%d", host))
				continue
			}
			_ = ln.Close()
		}
	}

	if len(busy) > 0 {
		return Check{Name: "Required ports available", Status: StatusWarn,
			Detail:      "port(s) already in use: " + strings.Join(busy, ", "),
			Remediation: "Expected if Orbit's proxy is already running for this service (docker ps --filter name=docker-rollout-proxy) — otherwise, stop whatever else is bound to these ports before deploying"}
	}
	return Check{Name: "Required ports available", Status: StatusPass, Detail: "all declared host ports are free"}
}

func checkComposeFile(path string) Check {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Check{Name: "Compose file", Status: StatusWarn,
			Detail:      fmt.Sprintf("%s not found in the current directory", path),
			Remediation: "Run doctor from your project directory, or pass --file <path>"}
	}
	if _, err := compose.ParseFile(path); err != nil {
		return Check{Name: "Compose file", Status: StatusFail,
			Detail:      "failed to parse " + path + ": " + err.Error(),
			Remediation: "Check the file is valid YAML and follows the Compose spec"}
	}
	return Check{Name: "Compose file", Status: StatusPass, Detail: path + " parses correctly"}
}

func checkControlAddr(addr string) Check {
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Check{Name: "Orbit configuration valid", Status: StatusFail,
			Detail:      fmt.Sprintf("--control-addr %q is not a valid http(s) URL", addr),
			Remediation: "Pass a URL like http://localhost:9900"}
	}
	if token := os.Getenv("ORBIT_API_TOKEN"); token != "" && len(token) < 8 {
		return Check{Name: "Orbit configuration valid", Status: StatusWarn,
			Detail:      "ORBIT_API_TOKEN is set but shorter than 8 characters",
			Remediation: "Use a longer, random token for meaningful control-API security"}
	}
	return Check{Name: "Orbit configuration valid", Status: StatusPass, Detail: "control address and environment look valid"}
}

func checkProxyReachable(controlAddr string) Check {
	if _, err := doGet(controlAddr + "/health/live"); err != nil {
		return Check{Name: "Proxy reachable", Status: StatusWarn,
			Detail:      "no response from " + controlAddr + ": " + err.Error(),
			Remediation: "If you expect a proxy to be running: docker ps --filter name=docker-rollout-proxy. If not, this is expected before your first `docker orbit generate` + deploy"}
	}
	return Check{Name: "Proxy reachable", Status: StatusPass, Detail: "proxy responded at " + controlAddr}
}

func checkProxyReady(controlAddr string) Check {
	raw, err := doGet(controlAddr + "/health/ready")
	if err != nil {
		return Check{Name: "Proxy healthy", Status: StatusWarn,
			Detail:      "readiness endpoint unreachable — see 'Proxy reachable' check",
			Remediation: "Resolve proxy connectivity first"}
	}
	var resp struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return Check{Name: "Proxy healthy", Status: StatusWarn, Detail: "readiness response was not valid JSON",
			Remediation: "Check for a CLI/proxy version mismatch"}
	}
	if resp.Status != "ready" {
		return Check{Name: "Proxy healthy", Status: StatusWarn,
			Detail:      fmt.Sprintf("proxy state is %q (%s)", resp.State, resp.Reason),
			Remediation: "Run 'docker orbit status' for detail; wait for recovery to finish or investigate backend health"}
	}
	return Check{Name: "Proxy healthy", Status: StatusPass, Detail: "readiness endpoint reports ready"}
}

func checkRecoveryState(controlAddr string) Check {
	report, err := fetchStatus(controlAddr)
	if err != nil {
		return Check{Name: "Recovery state consistent", Status: StatusWarn,
			Detail:      "could not fetch status — see 'Proxy reachable' check",
			Remediation: "Resolve proxy connectivity first"}
	}
	if report.Recovery.Degraded {
		return Check{Name: "Recovery state consistent", Status: StatusFail,
			Detail:      "proxy reports degraded recovery state",
			Remediation: "Run 'docker orbit status' for detail; check container health and Docker Engine connectivity from inside the proxy container"}
	}
	if report.Recovery.RecoveryFailureCount > 0 && report.Recovery.RecoveryCount > 0 &&
		report.Recovery.RecoveryFailureCount == report.Recovery.RecoveryCount {
		return Check{Name: "Recovery state consistent", Status: StatusWarn,
			Detail:      "every recorded recovery attempt has failed",
			Remediation: "Investigate proxy logs (docker logs <proxy-container>) for the underlying cause"}
	}
	return Check{Name: "Recovery state consistent", Status: StatusPass, Detail: "no degraded or failing recovery state detected"}
}

func checkStateDirWritable() Check {
	dir := history.Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Check{Name: "State directory writable", Status: StatusFail,
			Detail:      "cannot create " + dir + ": " + err.Error(),
			Remediation: "Check filesystem permissions, or set ORBIT_STATE_DIR to a writable path"}
	}
	probe := filepath.Join(dir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0600); err != nil {
		return Check{Name: "State directory writable", Status: StatusFail,
			Detail:      "cannot write to " + dir + ": " + err.Error(),
			Remediation: "Check filesystem permissions, or set ORBIT_STATE_DIR to a writable path"}
	}
	os.Remove(probe) //nolint:errcheck
	return Check{Name: "State directory writable", Status: StatusPass, Detail: dir + " is writable"}
}

func checkPluginInstalled() Check {
	self, err := os.Executable()
	if err != nil {
		return Check{Name: "Plugin installation", Status: StatusWarn,
			Detail:      "could not determine this binary's path: " + err.Error(),
			Remediation: "This is informational only — not required for CLI use"}
	}
	if _, err := exec.LookPath("docker-orbit"); err == nil {
		return Check{Name: "Plugin installation", Status: StatusPass, Detail: "docker-orbit found on PATH (" + self + ")"}
	}
	for _, dir := range []string{"/usr/local/lib/docker/cli-plugins", filepath.Join(os.Getenv("HOME"), ".docker", "cli-plugins")} {
		if _, err := os.Stat(filepath.Join(dir, "docker-orbit")); err == nil {
			return Check{Name: "Plugin installation", Status: StatusPass, Detail: "installed at " + filepath.Join(dir, "docker-orbit")}
		}
	}
	return Check{Name: "Plugin installation", Status: StatusWarn,
		Detail:      "docker-orbit not found on PATH or in a known Docker CLI plugins directory",
		Remediation: "Run: sudo make install-plugin (or copy this binary to ~/.docker/cli-plugins/docker-orbit) to use 'docker orbit' instead of the standalone binary"}
}

// ── Rendering ──────────────────────────────────────────────────────────────────

// renderDoctorHuman writes to w best-effort — see renderStatusHuman's doc
// comment in status.go for why write errors are explicitly discarded here.
func renderDoctorHuman(w io.Writer, r DoctorReport) {
	color := colorEnabled(w)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range r.Checks {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", colorGlyph(c.Status, color), c.Name, c.Detail)
		if c.Remediation != "" {
			remediation := "→ " + c.Remediation
			if color {
				remediation = ansiDim + remediation + ansiReset
			}
			_, _ = fmt.Fprintf(tw, "\t\t  %s\n", remediation)
		}
	}
	_ = tw.Flush()

	summary := fmt.Sprintf("%d passed, %d warning, %d error", r.Summary.Pass, r.Summary.Warning, r.Summary.Error)
	if r.Summary.Skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", r.Summary.Skipped)
	}
	if color {
		switch {
		case r.Summary.Error > 0:
			summary = ansiRed + summary + ansiReset
		case r.Summary.Warning > 0:
			summary = ansiYellow + summary + ansiReset
		default:
			summary = ansiGreen + summary + ansiReset
		}
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", summary)
}

func statusGlyph(s CheckStatus) string {
	switch s {
	case StatusPass:
		return "✓ PASS"
	case StatusWarn:
		return "⚠ WARN"
	case StatusFail:
		return "✗ FAIL"
	case StatusSkip:
		return "○ SKIP"
	default:
		return string(s)
	}
}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[0;32m"
	ansiYellow = "\033[1;33m"
	ansiRed    = "\033[0;31m"
	ansiDim    = "\033[2m"
)

// colorGlyph wraps statusGlyph's plain text in color only when color is
// true — kept separate from statusGlyph itself since that function is
// shared with deploy/rollback preflight rendering, which this change
// intentionally leaves untouched.
func colorGlyph(s CheckStatus, color bool) string {
	glyph := statusGlyph(s)
	if !color {
		return glyph
	}
	switch s {
	case StatusPass:
		return ansiGreen + glyph + ansiReset
	case StatusWarn:
		return ansiYellow + glyph + ansiReset
	case StatusFail:
		return ansiRed + glyph + ansiReset
	case StatusSkip:
		return ansiDim + glyph + ansiReset
	default:
		return glyph
	}
}

// colorEnabled reports whether w is a real terminal, so output can be
// colorized without ever leaking ANSI escapes into piped/redirected output,
// log files, or test buffers (a bytes.Buffer is never an *os.File, so this
// always returns false for the golden-file tests, independent of whatever
// terminal happens to be running `go test`). Honors NO_COLOR per
// https://no-color.org.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
