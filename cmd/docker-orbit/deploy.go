package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/compose"
	"github.com/docker-secret-operator/orbit/internal/rollout"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// deployCmd is Orbit's primary, richly-featured deployment command — the
// same internal/rollout.Run engine `docker orbit rollout` calls, wrapped
// with the pre-flight safety checks, plan preview, progress reporting, and
// confirmation flow the standalone `rollout` command has never had. `rollout`
// remains available unchanged for backward compatibility and scripts already
// depending on its exact behavior; `deploy` is not a redesign of the engine,
// only a richer front end for it (see CONSTITUTION.md's "Backward
// Compatibility Whenever Practical").
func deployCmd(log *zap.Logger) *cobra.Command {
	var opts rollout.Options
	var project string
	var dryRun, yes, jsonOut, forceUnlock bool

	cmd := &cobra.Command{
		Use:   "deploy <service>",
		Short: "Safely deploy a new version of a service (plan, confirm, execute)",
		Long: `Deploys a new version of the named service using the same zero-downtime
engine as 'docker orbit rollout', with production safety built in:

  1. Pre-flight checks (Docker reachable, compose file valid, proxy healthy,
     recovery state consistent) — the same checks 'docker orbit doctor' runs.
  2. A deployment plan preview (current generation, backend health, what will
     happen) shown before anything changes.
  3. A confirmation prompt, unless --yes or --dry-run is given.
  4. Live progress reporting through each phase as it happens.
  5. A completion summary with the resulting generation and health state.

Use --dry-run to see the plan without deploying anything.

Example:
  docker orbit deploy web
  docker orbit deploy web --dry-run
  docker orbit deploy web --yes --json          # non-interactive, for CI
  docker orbit deploy web --project myapp       # verify the target proxy first`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Service = args[0]
			p := output.New(cmd.OutOrStdout(), jsonOut)
			return runDeploy(cmd, p, opts, project, dryRun, yes, forceUnlock, log)
		},
	}

	cmd.Flags().StringVarP(&opts.ComposeFile, "file", "f", "docker-rollout-compose.yml", "docker-rollout compose file")
	cmd.Flags().StringVar(&project, "project", "", "Verify the queried proxy reports this service/project name")
	cmd.Flags().BoolVar(&opts.Pull, "pull", false, "Pull latest image before deploying")
	cmd.Flags().DurationVarP(&opts.Timeout, "timeout", "t", 60*time.Second, "Healthcheck timeout")
	cmd.Flags().DurationVarP(&opts.Drain, "drain", "d", 5*time.Second, "Drain period before removing old container")
	cmd.Flags().StringVar(&opts.ControlAddr, "control-addr", "http://localhost:9900", "Proxy control API address")
	cmd.Flags().StringVar(&opts.APIToken, "api-token", os.Getenv("ORBIT_API_TOKEN"), "Control API bearer token")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the deployment plan without executing it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt (required for non-interactive/CI use)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&forceUnlock, "force-unlock", false, "Force unlock if a previous process died (ONLY use after verifying it's gone)")
	return cmd
}

// DeployPlan is the pre-execution preview shown for both --dry-run and the
// confirmation prompt, and the full --json payload for --dry-run. Field
// names are part of Orbit's Stable API Policy once released.
type DeployPlan struct {
	Service           string   `json:"service"`
	ComposeFile       string   `json:"compose_file"`
	CurrentGeneration string   `json:"current_generation,omitempty"`
	ProxyStatus       string   `json:"proxy_status,omitempty"`
	HealthyBackends   int      `json:"healthy_backends"`
	UnhealthyBackends int      `json:"unhealthy_backends"`
	Steps             []string `json:"steps"`
	Timeout           string   `json:"timeout"`
	Drain             string   `json:"drain"`
	PreflightChecks   []Check  `json:"preflight_checks"`
	PreflightPassed   bool     `json:"preflight_passed"`
}

// DeployResult is the completion summary — the --json payload for a
// (non-dry-run) deploy that ran to completion, successfully or not.
type DeployResult struct {
	Service            string `json:"service"`
	Success            bool   `json:"success"`
	Error              string `json:"error,omitempty"`
	DurationMS         int64  `json:"duration_ms"`
	PreviousGeneration string `json:"previous_generation,omitempty"`
	CurrentGeneration  string `json:"current_generation,omitempty"`
	ProxyStatus        string `json:"proxy_status,omitempty"`
	HealthyBackends    int    `json:"healthy_backends"`
	UnhealthyBackends  int    `json:"unhealthy_backends"`
}

// deploySteps mirrors rollout.Run's own documented step sequence — shown in
// the plan preview as exactly what will happen, not a guess. Keep this in
// sync with rollout.Run's doc comment; it describes the same fixed sequence.
var deploySteps = []string{
	"Acquire deployment lock (prevents a concurrent deploy for this service)",
	"Optional: pull the new image (--pull)",
	"Scale the service +1 (start the new container alongside the old one)",
	"Wait for the new container's healthcheck to pass (or --timeout)",
	"Register the new container with the proxy — traffic starts splitting",
	"Save rollback state (enables 'docker orbit rollback' if this fails)",
	"Drain the old container for --drain, then remove it",
	"Deregister the old backend and the initial seed backend",
}

func runDeploy(cmd *cobra.Command, p *output.Printer, opts rollout.Options, project string, dryRun, yes, forceUnlock bool, log *zap.Logger) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Verify the service exists in the compose file up front ───────────
	cf, err := compose.ParseFile(opts.ComposeFile)
	if err != nil {
		e := clierr.Wrap(err, output.ExitConfig,
			fmt.Sprintf("could not read %s", opts.ComposeFile),
			"Run 'docker orbit generate' first, or pass --file <path>")
		return renderCLIErr(p, e)
	}
	if _, ok := cf.Services[opts.Service]; !ok {
		e := clierr.NewWithCode(output.ExitConfig,
			fmt.Sprintf("service %q not found in %s", opts.Service, opts.ComposeFile),
			"", "Check the service name, or run 'docker orbit generate' first")
		return renderCLIErr(p, e)
	}

	// ── Pre-flight safety checks (same checks 'doctor' runs) ──────────────
	report := runDoctorChecks(ctx, opts.ControlAddr, opts.ComposeFile, resolveProject(project))
	preflightOK := deployPreflightPassed(report)

	// ── Build the plan preview from live state ────────────────────────────
	plan := DeployPlan{
		Service:         opts.Service,
		ComposeFile:     opts.ComposeFile,
		Steps:           deploySteps,
		Timeout:         opts.Timeout.String(),
		Drain:           opts.Drain.String(),
		PreflightChecks: report.Checks,
		PreflightPassed: preflightOK,
	}
	if status, err := fetchStatus(opts.ControlAddr); err == nil {
		plan.CurrentGeneration = status.CurrentGeneration
		plan.ProxyStatus = status.ProxyStatus
		plan.HealthyBackends = len(status.HealthyBackends)
		plan.UnhealthyBackends = len(status.UnhealthyBackends)
		if project != "" && status.Service != project {
			e := clierr.NewWithCode(output.ExitConfig,
				fmt.Sprintf("proxy at %s reports service %q, not %q", opts.ControlAddr, status.Service, project),
				"", "Pass the --control-addr for the correct service, or omit --project to skip this check")
			return renderCLIErr(p, e)
		}
	}

	if dryRun {
		if p.IsJSON() {
			return p.JSON(plan)
		}
		p.Human(func(w io.Writer) { renderDeployPlanHuman(w, plan) })
		return nil
	}

	if !preflightOK {
		if p.IsJSON() {
			if err := p.JSON(plan); err != nil {
				return err
			}
		} else {
			p.Human(func(w io.Writer) {
				renderDeployPlanHuman(w, plan)
				fmt.Fprintln(w, "\n✗ Pre-flight checks failed — aborting before making any changes.")
				fmt.Fprintln(w, "  Run 'docker orbit doctor' for full remediation steps.")
			})
		}
		os.Exit(output.ExitUnavailable)
		return nil
	}

	// ── Plan + confirmation (human mode only — JSON/CI mode requires --yes) ──
	if !p.IsJSON() {
		p.Human(func(w io.Writer) { renderDeployPlanHuman(w, plan) })
	}
	if !yes {
		if p.IsJSON() {
			e := clierr.NewWithCode(output.ExitConfig,
				"confirmation required but --json was given without --yes",
				"", "Pass --yes to confirm non-interactively, or drop --json to see the interactive prompt")
			return renderCLIErr(p, e)
		}
		confirmed, err := confirmPrompt(cmd.InOrStdin(), p.Writer(), fmt.Sprintf("Deploy %s now?", opts.Service))
		if err != nil || !confirmed {
			p.Human(func(w io.Writer) { fmt.Fprintln(w, "Aborted — no changes made.") })
			return nil
		}
	}

	// ── Execute via the real, existing rollout engine ─────────────────────
	start := time.Now()
	var lock *rollout.FileLock
	if forceUnlock {
		lock, err = rollout.AcquireLockForce(opts.Service)
	} else {
		lock, err = rollout.AcquireLock(opts.Service)
	}
	if err != nil {
		e := clierr.Wrap(err, output.ExitError, "could not acquire deployment lock", "Check for a stale lock file, or pass --force-unlock after verifying no other deploy is running")
		return renderCLIErr(p, e)
	}
	defer lock.Release()

	opts.Progress = func(phase rollout.Phase, detail string) {
		p.Human(func(w io.Writer) { fmt.Fprintf(w, "  → [%s] %s\n", phase, detail) })
	}

	runErr := rollout.Run(ctx, opts, log)
	result := DeployResult{
		Service:            opts.Service,
		Success:            runErr == nil,
		DurationMS:         time.Since(start).Milliseconds(),
		PreviousGeneration: plan.CurrentGeneration,
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if status, err := fetchStatus(opts.ControlAddr); err == nil {
		result.CurrentGeneration = status.CurrentGeneration
		result.ProxyStatus = status.ProxyStatus
		result.HealthyBackends = len(status.HealthyBackends)
		result.UnhealthyBackends = len(status.UnhealthyBackends)
	}

	if p.IsJSON() {
		if err := p.JSON(result); err != nil {
			return err
		}
	} else {
		p.Human(func(w io.Writer) { renderDeployResultHuman(w, result) })
	}

	if runErr != nil {
		os.Exit(output.ExitError)
	}
	if result.UnhealthyBackends > 0 {
		os.Exit(output.ExitDegraded)
	}
	return nil
}

// deployPreflightPassed applies deploy-specific interpretation to doctor's
// checks: an ERROR always blocks, and for deploy specifically (unlike a
// general doctor run, which is also used before anything has ever been
// deployed) an unreachable/unhealthy proxy also blocks — deploy has nowhere
// to register a new backend without one.
func deployPreflightPassed(report DoctorReport) bool {
	for _, c := range report.Checks {
		if c.Status == StatusFail {
			return false
		}
		if c.Status == StatusWarn && (c.Name == "Proxy reachable" || c.Name == "Proxy healthy") {
			return false
		}
	}
	return true
}

// confirmPrompt reads a yes/no answer from in, writing the prompt to w.
// Only "y" or "yes" (case-insensitive) count as confirmation; anything else,
// including a read error or EOF, is treated as "no" — never guess consent.
func confirmPrompt(in io.Reader, w io.Writer, question string) (bool, error) {
	fmt.Fprintf(w, "%s [y/N]: ", question)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

func renderDeployPlanHuman(w io.Writer, plan DeployPlan) {
	fmt.Fprintf(w, "Deployment plan for %q\n\n", plan.Service)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "Compose file:\t%s\n", plan.ComposeFile)
	fmt.Fprintf(tw, "Current generation:\t%s\n", nonEmpty(plan.CurrentGeneration, "(none — first deploy)"))
	fmt.Fprintf(tw, "Proxy status:\t%s\n", nonEmpty(plan.ProxyStatus, "(unknown)"))
	fmt.Fprintf(tw, "Healthy backends:\t%d\n", plan.HealthyBackends)
	fmt.Fprintf(tw, "Unhealthy backends:\t%d\n", plan.UnhealthyBackends)
	fmt.Fprintf(tw, "Healthcheck timeout:\t%s\n", plan.Timeout)
	fmt.Fprintf(tw, "Drain period:\t%s\n", plan.Drain)
	_ = tw.Flush()

	fmt.Fprintln(w, "\nSteps that will run:")
	for i, s := range plan.Steps {
		fmt.Fprintf(w, "  %d. %s\n", i+1, s)
	}

	fmt.Fprintln(w, "\nPre-flight checks:")
	for _, c := range plan.PreflightChecks {
		fmt.Fprintf(w, "  %s  %s\n", statusGlyph(c.Status), c.Name)
	}
}

func renderDeployResultHuman(w io.Writer, r DeployResult) {
	fmt.Fprintln(w)
	if r.Success {
		fmt.Fprintf(w, "✓ Deployment complete (%dms)\n\n", r.DurationMS)
	} else {
		fmt.Fprintf(w, "✗ Deployment failed after %dms: %s\n\n", r.DurationMS, r.Error)
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "Previous generation:\t%s\n", nonEmpty(r.PreviousGeneration, "(none)"))
	fmt.Fprintf(tw, "Current generation:\t%s\n", nonEmpty(r.CurrentGeneration, "(unknown)"))
	fmt.Fprintf(tw, "Proxy status:\t%s\n", nonEmpty(r.ProxyStatus, "(unknown)"))
	fmt.Fprintf(tw, "Healthy backends:\t%d\n", r.HealthyBackends)
	fmt.Fprintf(tw, "Unhealthy backends:\t%d\n", r.UnhealthyBackends)
	_ = tw.Flush()

	if !r.Success {
		fmt.Fprintln(w, "\nRun 'docker orbit rollback "+r.Service+"' to restore the previous version if it's still recorded.")
	} else if r.UnhealthyBackends > 0 {
		fmt.Fprintln(w, "\n⚠ Deployment completed but unhealthy backends remain — check 'docker orbit status'.")
	}
}
