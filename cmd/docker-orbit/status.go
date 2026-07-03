package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// statusCmd answers "what is happening?" and "is everything healthy?" — the
// two questions Phase 2.1's Product Philosophy assigns to this command.
// Every field it prints comes from GET /status (internal/api.StatusReport),
// which is computed live from the running proxy's registry, recorded
// recovery/rollout state, and a real TCP probe per backend — nothing here
// is cached or invented client-side.
func statusCmd(log *zap.Logger) *cobra.Command {
	var controlAddr, project string
	var jsonOut, watch bool
	var watchInterval time.Duration

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what the running deployment is doing right now",
		Long: `Queries the Orbit proxy's control API and reports its current state:
active generation, deployment phase, proxy health, backend health, and
recovery state. Every field is discovered live from the running proxy —
nothing is cached between invocations.

Example:
  docker orbit status
  docker orbit status --json
  docker orbit status --watch
  docker orbit status --control-addr http://localhost:9901
  docker orbit status --project myapp    # verifies the queried proxy reports service "myapp"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := output.New(cmd.OutOrStdout(), jsonOut)

			if !watch {
				return runStatusOnce(p, controlAddr, project)
			}
			return runStatusWatch(cmd, p, controlAddr, project, watchInterval)
		},
	}
	cmd.Flags().StringVar(&controlAddr, "control-addr", "http://localhost:9900", "Proxy control API address")
	cmd.Flags().StringVar(&project, "project", "", "Verify the queried proxy reports this service/project name (does not perform project→port discovery)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&watch, "watch", false, "Continuously poll and redraw status until interrupted (Ctrl-C)")
	cmd.Flags().DurationVar(&watchInterval, "interval", 2*time.Second, "Poll interval for --watch")
	return cmd
}

// fetchStatus performs the real HTTP round-trip to GET /status and decodes
// the response. It returns a *clierr.Error on any failure so callers don't
// need to translate raw net/http errors themselves.
func fetchStatus(controlAddr string) (api.StatusReport, error) {
	raw, err := doGet(controlAddr + "/status")
	if err != nil {
		return api.StatusReport{}, clierr.Wrap(err, output.ExitUnavailable,
			"Orbit proxy unreachable",
			fmt.Sprintf("Check the proxy is running: docker ps --filter name=docker-rollout-proxy, and that --control-addr (%s) is correct", controlAddr))
	}

	var report api.StatusReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return api.StatusReport{}, clierr.Wrap(err, output.ExitError,
			"proxy returned an unreadable status response",
			"This usually means a version mismatch between the CLI and proxy — rebuild both from the same commit")
	}
	return report, nil
}

func runStatusOnce(p *output.Printer, controlAddr, project string) error {
	report, err := fetchStatus(controlAddr)
	if err != nil {
		return renderCLIErr(p, err)
	}
	if project != "" && report.Service != project {
		e := clierr.NewWithCode(output.ExitConfig,
			fmt.Sprintf("proxy at %s reports service %q, not %q", controlAddr, report.Service, project),
			"", "Pass the --control-addr for the correct service, or omit --project to skip this check")
		return renderCLIErr(p, e)
	}

	if p.IsJSON() {
		return p.JSON(report)
	}
	p.Human(func(w io.Writer) { renderStatusHuman(w, report) })

	if report.Recovery.Degraded || report.ProxyStatus == "failed" || report.ProxyStatus == "degraded" {
		os.Exit(output.ExitDegraded)
	}
	return nil
}

func runStatusWatch(cmd *cobra.Command, p *output.Printer, controlAddr, project string, interval time.Duration) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		report, err := fetchStatus(controlAddr)
		if err != nil {
			_ = renderCLIErr(p, err) // keep watching — a transient failure shouldn't kill --watch
		} else if p.IsJSON() {
			_ = p.JSON(report)
		} else {
			p.Human(func(w io.Writer) {
				_, _ = fmt.Fprint(w, "\033[H\033[2J") // clear screen between redraws
				renderStatusHuman(w, report)
				if project != "" && report.Service != project {
					_, _ = fmt.Fprintf(w, "\n⚠ expected service %q, got %q\n", project, report.Service)
				}
			})
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// renderStatusHuman writes to w best-effort — a failure to write to the
// terminal isn't actionable and is deliberately not propagated, matching
// the existing convention elsewhere in this CLI (e.g. generateCmd's
// os.Stderr writes). Return values are explicitly discarded via `_, _ =`
// rather than ignored implicitly, so this is a visible choice, not an
// oversight.
func renderStatusHuman(w io.Writer, r api.StatusReport) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Service:\t%s\n", nonEmpty(r.Service, "(unset)"))
	_, _ = fmt.Fprintf(tw, "Runtime version:\t%s\n", r.RuntimeVersion)
	_, _ = fmt.Fprintf(tw, "Proxy status:\t%s\n", r.ProxyStatus)
	_, _ = fmt.Fprintf(tw, "Current generation:\t%s\n", nonEmpty(r.CurrentGeneration, "(none)"))
	_, _ = fmt.Fprintf(tw, "Previous generation:\t%s\n", nonEmpty(r.PreviousGeneration, "(none)"))
	_, _ = fmt.Fprintf(tw, "Deployment state:\t%s\n", r.DeploymentState)
	_, _ = fmt.Fprintf(tw, "Healthy backends:\t%d\n", len(r.HealthyBackends))
	_, _ = fmt.Fprintf(tw, "Unhealthy backends:\t%d\n", len(r.UnhealthyBackends))
	_, _ = fmt.Fprintf(tw, "Active traffic target:\t%s\n", nonEmptyList(r.ActiveTrafficTarget))
	_, _ = fmt.Fprintf(tw, "Recovery — total / failed:\t%d / %d\n", r.Recovery.RecoveryCount, r.Recovery.RecoveryFailureCount)
	_, _ = fmt.Fprintf(tw, "Recovery — degraded:\t%v\n", r.Recovery.Degraded)
	_ = tw.Flush()

	if len(r.UnhealthyBackends) > 0 {
		_, _ = fmt.Fprintln(w, "\nUnhealthy backends:")
		for _, b := range r.UnhealthyBackends {
			_, _ = fmt.Fprintf(w, "  %s  %s%s\n", b.ID, b.Addr, drainSuffix(b.Draining))
		}
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func nonEmptyList(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += ", " + s
	}
	return out
}

func drainSuffix(draining bool) string {
	if draining {
		return " (draining)"
	}
	return ""
}

// renderCLIErr prints e via clierr.Print and returns nil so Cobra doesn't
// double-print the error — CLI error rendering is intentionally centralized
// in internal/cli/clierr, not left to Cobra's default "Error: ..." format.
// The caller is responsible for exiting with e.ExitCode when appropriate
// (RunE's return value only controls Cobra's own exit-code-1 behavior).
func renderCLIErr(p *output.Printer, err error) error {
	e, ok := err.(*clierr.Error)
	if !ok {
		e = clierr.Wrap(err, output.ExitError, "unexpected error", "Please file a bug report with the command you ran")
	}
	_ = clierr.Print(p, e)
	os.Exit(e.ExitCode)
	return nil
}
