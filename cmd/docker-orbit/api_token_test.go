package main

import (
	"strings"
	"testing"
)

// TestHelpOutput_NeverLeaksAPIToken reproduces a real secret leak: cobra/pflag
// prints a flag's *default value* in --help text whenever that default is
// non-empty. Wiring the --api-token flag's default straight to
// os.Getenv("ORBIT_API_TOKEN") means the live secret — not a placeholder —
// gets printed verbatim to anyone who runs --help with the token exported
// (shell history, CI logs, terminal recordings), and the same text is what
// `docker orbit docs` writes into docs/cli-reference/*.md.
func TestHelpOutput_NeverLeaksAPIToken(t *testing.T) {
	const secret = "SUPERSECRET_TOKEN_VALUE_123"
	t.Setenv("ORBIT_API_TOKEN", secret)

	for _, cmdName := range []string{"rollout", "deploy", "recover", "rollback"} {
		t.Run(cmdName, func(t *testing.T) {
			stdout, stderr, _ := runCLISubprocess(t, cmdName, "--help")
			combined := stdout + stderr
			if strings.Contains(combined, secret) {
				t.Fatalf("%s --help leaked ORBIT_API_TOKEN value into its output:\n%s", cmdName, combined)
			}
		})
	}
}
