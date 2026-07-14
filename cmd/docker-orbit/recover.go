package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// recoverCmd triggers a real, on-demand recovery pass and reports what it
// found and did. It inspects current state first (GET /status), then
// executes the identical recovery logic the proxy runs at startup (POST
// /recover, wired in cmd/docker-orbit/main.go's runProxy via
// ControlServer.SetRecoveryTrigger — see executeRecovery there for the one
// implementation both paths share). Recovery is deterministic: if authority
// cannot be established, this command reports that plainly and exits
// non-zero rather than guessing or claiming success.
func recoverCmd(log *zap.Logger) *cobra.Command {
	var controlAddr, apiToken, project string
	var timeout time.Duration
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Trigger a deterministic recovery pass and report the outcome",
		Long: `Inspects the running proxy's current state, then triggers a real recovery
pass: rediscovering live backends from Docker, determining which generation
should hold traffic (from persisted authority state — never guessed), and
reconciling the proxy's backend registry to match.

This is the same recovery Orbit performs automatically at proxy startup,
available on demand — useful after manually restarting containers, or to
confirm recovery would succeed before relying on it.

If authority cannot be established (no persisted state and no healthy
generation to infer from), this command reports that plainly and exits
non-zero. It never guesses.

Example:
  docker orbit recover
  docker orbit recover --json
  docker orbit recover --control-addr http://localhost:9901`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := output.New(cmd.OutOrStdout(), jsonOut)
			return runRecover(cmd, p, controlAddr, resolveAPIToken(apiToken), project, timeout, log)
		},
	}

	cmd.Flags().StringVar(&controlAddr, "control-addr", "http://localhost:9900", "Proxy control API address")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "Control API bearer token (falls back to ORBIT_API_TOKEN)")
	cmd.Flags().StringVar(&project, "project", "", "Verify the queried proxy reports this service/project name")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Maximum time to wait for the recovery pass (Docker discovery + health checks can take longer than a typical status query)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// RecoverSummary is the full --json payload for `docker orbit recover`:
// the state observed beforehand, the recovery outcome, and the state
// observed after.
type RecoverSummary struct {
	Service           string              `json:"service"`
	BeforeState       string              `json:"before_state"`
	InterruptedDeploy bool                `json:"interrupted_deployment_detected"`
	Outcome           api.RecoveryOutcome `json:"outcome"`
	AfterState        string              `json:"after_state,omitempty"`
	HealthyBackends   int                 `json:"healthy_backends_after"`
	UnhealthyBackends int                 `json:"unhealthy_backends_after"`
}

func runRecover(cmd *cobra.Command, p *output.Printer, controlAddr, apiToken, project string, timeout time.Duration, log *zap.Logger) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	ctx, timeoutCancel := context.WithTimeout(ctx, timeout)
	defer timeoutCancel()

	// ── Inspect state before recovering ───────────────────────────────────
	before, err := fetchStatus(controlAddr)
	if err != nil {
		return renderCLIErr(p, err)
	}
	if project != "" && before.Service != project {
		e := clierr.NewWithCode(output.ExitConfig,
			fmt.Sprintf("proxy at %s reports service %q, not %q", controlAddr, before.Service, project),
			"", "Pass the --control-addr for the correct service, or omit --project to skip this check")
		return renderCLIErr(p, e)
	}

	interrupted := before.DeploymentState != "" && before.DeploymentState != "idle" && before.DeploymentState != "completed"

	if !p.IsJSON() {
		p.Human(func(w io.Writer) {
			fmt.Fprintf(w, "Inspecting %q...\n", before.Service)
			fmt.Fprintf(w, "  Current state: %s (proxy: %s)\n", nonEmpty(before.DeploymentState, "idle"), before.ProxyStatus)
			if interrupted {
				fmt.Fprintln(w, "  ⚠ Deployment phase is not idle/completed — this looks like an interrupted deployment.")
			}
			fmt.Fprintln(w, "\nTriggering recovery...")
		})
	}

	// ── Trigger the real recovery pass ────────────────────────────────────
	outcome, err := postRecover(ctx, controlAddr, apiToken)
	if err != nil {
		return renderCLIErr(p, err)
	}

	summary := RecoverSummary{
		Service:           before.Service,
		BeforeState:       nonEmpty(before.DeploymentState, "idle"),
		InterruptedDeploy: interrupted,
		Outcome:           outcome,
	}
	if after, err := fetchStatus(controlAddr); err == nil {
		summary.AfterState = after.ProxyStatus
		summary.HealthyBackends = len(after.HealthyBackends)
		summary.UnhealthyBackends = len(after.UnhealthyBackends)
	}

	if p.IsJSON() {
		if err := p.JSON(summary); err != nil {
			return err
		}
	} else {
		p.Human(func(w io.Writer) { renderRecoverSummaryHuman(w, summary) })
	}

	if outcome.Action == "degraded" {
		os.Exit(output.ExitDegraded)
	}
	return nil
}

// postRecover calls POST /recover and decodes the resulting RecoveryOutcome.
// Uses a dedicated client (not the package's short-timeout httpClient)
// because a real recovery pass — Docker discovery plus a health probe per
// backend — can legitimately take longer than a status query.
func postRecover(ctx context.Context, controlAddr, apiToken string) (api.RecoveryOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlAddr+"/recover", nil)
	if err != nil {
		return api.RecoveryOutcome{}, clierr.Wrap(err, output.ExitError, "could not build recovery request", "This is likely a bug — please file a report")
	}
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return api.RecoveryOutcome{}, clierr.Wrap(err, output.ExitUnavailable,
			"Orbit proxy unreachable",
			fmt.Sprintf("Check the proxy is running: docker ps --filter name=docker-rollout-proxy, and that --control-addr (%s) is correct", controlAddr))
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var outcome api.RecoveryOutcome
		if err := json.Unmarshal(body, &outcome); err != nil {
			return api.RecoveryOutcome{}, clierr.Wrap(err, output.ExitError,
				"proxy returned an unreadable recovery response",
				"This usually means a version mismatch between the CLI and proxy — rebuild both from the same commit")
		}
		return outcome, nil
	case http.StatusConflict:
		return api.RecoveryOutcome{}, clierr.NewWithCode(output.ExitError,
			"a recovery pass is already in progress on this proxy",
			"", "Wait for it to finish, then check 'docker orbit status' — running two recovery passes concurrently is not safe and Orbit refuses to do it")
	case http.StatusUnauthorized, http.StatusForbidden:
		return api.RecoveryOutcome{}, clierr.NewWithCode(output.ExitConfig,
			"recovery request rejected: authentication failed",
			"", "Pass the correct --api-token, or set ORBIT_API_TOKEN")
	case http.StatusServiceUnavailable:
		return api.RecoveryOutcome{}, clierr.NewWithCode(output.ExitUnavailable,
			"this proxy build does not support on-demand recovery",
			"", "Rebuild the proxy image from the current codebase — POST /recover requires Phase 2.2 or later")
	default:
		return api.RecoveryOutcome{}, clierr.NewWithCode(output.ExitError,
			fmt.Sprintf("recovery request failed: unexpected status %d", resp.StatusCode),
			strings.TrimSpace(string(body)),
			"Check the proxy logs (docker logs <proxy-container>) for the underlying cause")
	}
}

func renderRecoverSummaryHuman(w io.Writer, s RecoverSummary) {
	o := s.Outcome
	fmt.Fprintln(w)

	switch o.Action {
	case "degraded":
		fmt.Fprintln(w, "✗ Recovery could not establish an authoritative generation.")
		fmt.Fprintf(w, "  Reason: %s\n", nonEmpty(o.FailedReason, o.Reason))
		fmt.Fprintln(w, "\n  This is not a guess-and-hope situation: Orbit found no persisted authority")
		fmt.Fprintln(w, "  state and no healthy generation to infer one from, and stopped rather than")
		fmt.Fprintln(w, "  pick one arbitrarily. Check container health (docker ps, docker logs) and")
		fmt.Fprintln(w, "  re-run once at least one generation is healthy.")
		return
	case "inferred_fallback":
		fmt.Fprintln(w, "⚠ Recovery succeeded, but had no persisted authority to confirm against.")
		fmt.Fprintf(w, "  Inferred generation: %s (%s)\n", o.AuthoritativeGeneration, o.Reason)
	default:
		fmt.Fprintln(w, "✓ Recovery complete.")
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "Authoritative generation:\t%s\n", nonEmpty(o.AuthoritativeGeneration, "(none)"))
	fmt.Fprintf(tw, "Action taken:\t%s\n", o.Action)
	fmt.Fprintf(tw, "Backends restored:\t%d\n", o.BackendsRestored)
	fmt.Fprintf(tw, "Proxy status:\t%s\n", o.ProxyStatus)
	if s.AfterState != "" {
		fmt.Fprintf(tw, "Healthy backends:\t%d\n", s.HealthyBackends)
		fmt.Fprintf(tw, "Unhealthy backends:\t%d\n", s.UnhealthyBackends)
	}
	_ = tw.Flush()
}
