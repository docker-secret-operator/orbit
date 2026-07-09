package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"go.uber.org/zap"
)

// docsCmd regenerates docs/cli-reference/ from the live Cobra command
// tree — the same Use/Short/Long/Example text `--help` shows, so the
// generated reference can never silently drift from actual CLI behavior.
// Hidden because it's a maintainer tool, not part of the operational
// surface (Phase 2.1's Product Philosophy commands are status/history/
// doctor/etc. — this one exists to keep documentation honest, not to
// answer "what is happening").
func docsCmd(log *zap.Logger) *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:    "docs",
		Short:  "Regenerate docs/cli-reference/ from the current command tree",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(outDir, 0755); err != nil {
				return fmt.Errorf("docs: create %s: %w", outDir, err)
			}
			root := buildRoot(log)
			// docker-orbit generate/docker-orbit_generate.md have used
			// hyphenated file names since the reference tree was added;
			// installation.md and .goreleaser.yaml link them by that exact
			// name. GenMarkdownTree derives both headers and file names from
			// CommandPath(), which follows the display-name annotation, so
			// clear it here to keep doc output on the hyphenated form while
			// live --help still shows 'docker orbit'.
			delete(root.Annotations, cobra.CommandDisplayNameAnnotation)
			root.InitDefaultHelpCmd()
			if err := doc.GenMarkdownTree(root, outDir); err != nil {
				return fmt.Errorf("docs: generate: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Generated CLI reference in %s\n", outDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "docs/cli-reference", "Output directory for generated markdown")
	return cmd
}
