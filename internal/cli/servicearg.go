package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// serviceArgCommands name their service with --service, or take it as their
// one argument: `mw status web`, `mw logs web` and `mw open web` were
// `unknown command "web"` (production check, 2026-11-25), as if web were a
// subcommand.
var serviceArgCommands = []string{
	"status", "logs", "open", "metrics", "restart", "redeploy", "deployments",
	"env", "domains", "bindings", "alerts", "scale", "volume",
}

// addServiceArgs lets each of serviceArgCommands take `[service]`, the same
// as --service. A command that already takes an argument is left alone.
func addServiceArgs(root *cobra.Command) {
	for _, name := range serviceArgCommands {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == root || cmd.RunE == nil || cmd.Flags().Lookup("service") == nil {
			continue
		}
		if cmd.Args != nil && cmd.Args(cmd, []string{"web"}) == nil {
			continue
		}
		run := cmd.RunE
		cmd.Use += " [service]"
		cmd.Args = cobra.MaximumNArgs(1)
		cmd.RunE = func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				f := c.Flags().Lookup("service")
				if f.Changed && f.Value.String() != args[0] {
					return fmt.Errorf("the service is named twice: %s and --service %s", args[0], f.Value.String())
				}
				if err := c.Flags().Set("service", args[0]); err != nil {
					return err
				}
			}
			return run(c, nil)
		}
	}
}
