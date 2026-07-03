package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/history"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// historyCmd answers "what happened?" — the deployment timeline for a
// service. Every entry comes from internal/history, which is populated by
// real rollout/rollback events as internal/rollout.Run and Rollback execute
// (see rollout.go) — nothing here is synthesized or backfilled. History
// only exists from the point this feature shipped forward; there is no
// retroactive record of earlier deployments, and the command says so
// explicitly on an empty log rather than implying data loss.
func historyCmd(log *zap.Logger) *cobra.Command {
	var project string
	var limit int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show the deployment timeline for a service",
		Long: `Lists recorded rollout and rollback events for a service, newest first.

Events are recorded as they happen by 'docker orbit rollout' and
'docker orbit rollback' — this command only reads that log, it does not
reconstruct or infer history from other state. A service with no recorded
events shows an empty timeline, not an error: either nothing has been
deployed through Orbit yet, or history started being recorded after the
last deployment.

Example:
  docker orbit history
  docker orbit history --project myapp
  docker orbit history --limit 20
  docker orbit history --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := output.New(cmd.OutOrStdout(), jsonOut)
			service := resolveProject(project)

			events, err := history.Read(service, limit)
			if err != nil {
				e := clierr.Wrap(err, output.ExitError,
					fmt.Sprintf("could not read history for %q", service),
					"Check that the history directory ("+history.Dir()+") is readable")
				return renderCLIErr(p, e)
			}

			if p.IsJSON() {
				return p.JSON(map[string]interface{}{
					"service": service,
					"count":   len(events),
					"events":  events,
				})
			}
			p.Human(func(w io.Writer) { renderHistoryHuman(w, service, events) })
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Service/project name (default: $ORBIT_PROXY_INSTANCE, else \"default\")")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of events to show (0 = all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// resolveProject applies the same default-resolution convention
// internal/config uses for ORBIT_PROXY_INSTANCE, so --project (or its
// absence) means the same thing here as it does to the running proxy.
func resolveProject(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("ORBIT_PROXY_INSTANCE"); env != "" {
		return env
	}
	return "default"
}

// renderHistoryHuman writes to w best-effort — see renderStatusHuman's doc
// comment in status.go for why write errors are explicitly discarded here.
func renderHistoryHuman(w io.Writer, service string, events []history.Event) {
	_, _ = fmt.Fprintf(w, "History for %q\n\n", service)
	if len(events) == 0 {
		_, _ = fmt.Fprintln(w, "No recorded events. Either nothing has been deployed through Orbit yet,")
		_, _ = fmt.Fprintln(w, "or history started being recorded after the last deployment.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tTYPE\tRESULT\tDURATION\tTRIGGER\tDETAIL")
	for _, ev := range events {
		detail := ev.Reason
		if detail == "" && (ev.OldGeneration != "" || ev.NewGeneration != "") {
			detail = fmt.Sprintf("%s → %s", nonEmpty(ev.OldGeneration, "?"), nonEmpty(ev.NewGeneration, "?"))
		}
		duration := "-"
		if ev.DurationMS > 0 {
			duration = fmt.Sprintf("%dms", ev.DurationMS)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			ev.Timestamp.Format("2006-01-02 15:04:05"),
			ev.Type,
			nonEmpty(ev.Result, "-"),
			duration,
			ev.Trigger,
			detail)
	}
	_ = tw.Flush()
}
