package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/rollout"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// rollbackCmd restores traffic to the previous version recorded by the last
// rollout/deploy — the same internal/rollout.Rollback engine, with the same
// preview/confirmation/JSON/progress treatment deployCmd gives Run. See
// applyRollbackTargetOverride's doc comment for what --to <generation> can
// and cannot do given the engine's current, single-slot state model.
func rollbackCmd(log *zap.Logger) *cobra.Command {
	var controlAddr, apiToken, to string
	var drain time.Duration
	var dryRun, yes, jsonOut bool

	cmd := &cobra.Command{
		Use:   "rollback <service>",
		Short: "Restore traffic to the previous version after a failed deployment",
		Long: `Reads the last rollout state for the service, re-registers the old
backend with the proxy, drains the new (failing) backend, and removes it.

The rollout/deploy commands save a state file to
/tmp/orbit-<service>-state.json between the point the new backend is
registered and the old one is removed. If the new deployment fails, run this
command immediately to restore traffic.

Example:
  docker orbit rollback web
  docker orbit rollback web --dry-run
  docker orbit rollback web --yes --json
  docker orbit rollback web --drain 15s --control-addr http://localhost:9901`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := output.New(cmd.OutOrStdout(), jsonOut)
			return runRollback(cmd, p, args[0], controlAddr, resolveAPIToken(apiToken), to, drain, dryRun, yes, log)
		},
	}

	cmd.Flags().StringVar(&controlAddr, "control-addr", "", "Override proxy control API address from state")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "Control API bearer token (falls back to ORBIT_API_TOKEN)")
	cmd.Flags().DurationVarP(&drain, "drain", "d", 0, "Override drain period from state")
	cmd.Flags().StringVar(&to, "to", "", "Roll back to a specific generation/backend ID (see limitations in --help)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the rollback plan without executing it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt (required for non-interactive/CI use)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// RollbackPlan is the pre-execution preview — the --json payload for
// --dry-run, and what's shown before the confirmation prompt.
type RollbackPlan struct {
	Service        string `json:"service"`
	RestoreTarget  string `json:"restore_target"`
	RestoreAddr    string `json:"restore_addr"`
	DrainingTarget string `json:"draining_target,omitempty"`
	Reason         string `json:"reason"`
	Drain          string `json:"drain"`
	ExpectedImpact string `json:"expected_impact"`
}

// RollbackResult is the completion summary — the --json payload after a
// (non-dry-run) rollback runs.
type RollbackResult struct {
	Service     string `json:"service"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	RestoredTo  string `json:"restored_to"`
	ProxyStatus string `json:"proxy_status,omitempty"`
}

func runRollback(cmd *cobra.Command, p *output.Printer, service, controlAddr, apiToken, to string, drain time.Duration, dryRun, yes bool, log *zap.Logger) error {
	rs, err := rollout.LoadState(service)
	if err != nil {
		e := clierr.Wrap(err, output.ExitConfig,
			fmt.Sprintf("no rollback state recorded for %q", service),
			"Rollback is only possible immediately after a rollout/deploy, before the old container is removed — check 'docker orbit history "+service+"' to confirm a recent deployment exists")
		return renderCLIErr(p, e)
	}

	if err := applyRollbackTargetOverride(&rs, to); err != nil {
		return renderCLIErr(p, err)
	}

	// Allow flag overrides, same as the original rollbackCmd.
	if controlAddr != "" {
		rs.ControlAddr = controlAddr
	}
	if apiToken != "" {
		rs.APIToken = apiToken
	}
	if drain != 0 {
		rs.Drain = drain
	}

	plan := RollbackPlan{
		Service:        service,
		RestoreTarget:  rs.OldBackendID,
		RestoreAddr:    rs.OldAddr,
		DrainingTarget: rs.NewBackendID,
		Reason:         "restoring the generation active before the last rollout/deploy",
		Drain:          nonEmptyDuration(rs.Drain, 5*time.Second),
		ExpectedImpact: fmt.Sprintf("%s traffic returns to %s; %s is drained and removed", service, rs.OldBackendID, nonEmpty(rs.NewBackendID, "(no new backend recorded)")),
	}

	if dryRun {
		if p.IsJSON() {
			return p.JSON(plan)
		}
		p.Human(func(w io.Writer) { renderRollbackPlanHuman(w, plan) })
		return nil
	}

	if !p.IsJSON() {
		p.Human(func(w io.Writer) { renderRollbackPlanHuman(w, plan) })
	}
	if !yes {
		if p.IsJSON() {
			e := clierr.NewWithCode(output.ExitConfig,
				"confirmation required but --json was given without --yes",
				"", "Pass --yes to confirm non-interactively, or drop --json to see the interactive prompt")
			return renderCLIErr(p, e)
		}
		confirmed, err := confirmPrompt(cmd.InOrStdin(), p.Writer(), fmt.Sprintf("Roll back %s now?", service))
		if err != nil || !confirmed {
			p.Human(func(w io.Writer) { fmt.Fprintln(w, "Aborted — no changes made.") })
			return nil
		}
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	start := time.Now()
	progress := func(phase rollout.RollbackPhase, detail string) {
		p.Human(func(w io.Writer) { fmt.Fprintf(w, "  → [%s] %s\n", phase, detail) })
	}
	runErr := rollout.Rollback(ctx, rs, log, progress)

	result := RollbackResult{
		Service:    service,
		Success:    runErr == nil,
		DurationMS: time.Since(start).Milliseconds(),
		RestoredTo: rs.OldBackendID,
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if status, err := fetchStatus(rs.ControlAddr); err == nil {
		result.ProxyStatus = status.ProxyStatus
	}

	if p.IsJSON() {
		if err := p.JSON(result); err != nil {
			return err
		}
	} else {
		p.Human(func(w io.Writer) { renderRollbackResultHuman(w, result) })
	}

	if runErr != nil {
		os.Exit(output.ExitError)
	}
	return nil
}

// applyRollbackTargetOverride validates --to against the one rollback target
// the engine actually has recorded. It never invents a path to an arbitrary
// historical generation: rollout.Run removes the old container in its own
// Step 9, so by the time a *second* rollout completes, the generation before
// that one has no live container left to roll back to — there is nothing to
// discover, only one thing to confirm. See CONSTITUTION.md's "Runtime
// Discovery Before Persistent Duplication" and the Phase 2.1 precedent of
// documenting this kind of limitation explicitly rather than faking a
// multi-generation history the state model doesn't have.
func applyRollbackTargetOverride(rs *rollout.RolloutState, to string) error {
	if to == "" {
		return nil
	}
	if to == rs.OldBackendID {
		return nil // matches the only recorded target — no-op, proceed normally
	}
	return clierr.NewWithCode(output.ExitConfig,
		fmt.Sprintf("cannot roll back to %q — only %q is currently recoverable", to, rs.OldBackendID),
		"Orbit's rollback state records exactly one prior generation per service, cleared once a rollout completes and the old container is removed. "+
			"There is no persisted history of earlier generations' live addresses to roll back to — see docs/troubleshooting.md for the full explanation.",
		"Use 'docker orbit rollback "+rs.Service+"' without --to to restore the recorded generation, or redeploy the desired image version directly")
}

func nonEmptyDuration(d, fallback time.Duration) string {
	if d == 0 {
		return fallback.String()
	}
	return d.String()
}

func renderRollbackPlanHuman(w io.Writer, plan RollbackPlan) {
	fmt.Fprintf(w, "Rollback plan for %q\n\n", plan.Service)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "Restore target:\t%s (%s)\n", plan.RestoreTarget, plan.RestoreAddr)
	fmt.Fprintf(tw, "Draining:\t%s\n", nonEmpty(plan.DrainingTarget, "(none recorded)"))
	fmt.Fprintf(tw, "Reason:\t%s\n", plan.Reason)
	fmt.Fprintf(tw, "Drain period:\t%s\n", plan.Drain)
	_ = tw.Flush()

	fmt.Fprintf(w, "\nExpected impact: %s\n", plan.ExpectedImpact)
}

func renderRollbackResultHuman(w io.Writer, r RollbackResult) {
	fmt.Fprintln(w)
	if r.Success {
		fmt.Fprintf(w, "✓ Rollback complete (%dms)\n\n", r.DurationMS)
	} else {
		fmt.Fprintf(w, "✗ Rollback failed after %dms: %s\n\n", r.DurationMS, r.Error)
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "Restored to:\t%s\n", r.RestoredTo)
	fmt.Fprintf(tw, "Proxy status:\t%s\n", nonEmpty(r.ProxyStatus, "(unknown)"))
	_ = tw.Flush()
}
