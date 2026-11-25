package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// listingGroups are the commands whose bare form lists what they hold. Each
// also answers to `list` and `ls`, the words gh, kubectl and docker taught
// everyone: `mw db list`, `mw instances list` and `mw projects ls` were
// "unknown command" (production check, 2026-11-25) while `mw vpn list` and
// `mw custom-images list` worked. Only these: a bare `mw deploy` deploys,
// `mw security` is an overview, and `mw storage ls` lists a bucket's objects.
var listingGroups = []string{
	"projects", "services", "db", "redis", "instances", "firewalls", "vpcs",
	"ssh-keys", "webhooks", "subscriptions", "operations", "templates",
	"registry", "groups", "deployments", "domains", "drains", "env", "alerts",
}

// addListAliases gives each listing group a hidden `list` subcommand (and
// `ls`, where the group does not already use it) that runs the bare command
// with the same flags.
func addListAliases(root *cobra.Command) {
	for _, name := range listingGroups {
		group, _, err := root.Find([]string{name})
		if err != nil || group == root || group.RunE == nil {
			continue
		}
		taken := map[string]bool{}
		for _, sub := range group.Commands() {
			taken[sub.Name()] = true
			for _, alias := range sub.Aliases {
				taken[alias] = true
			}
		}
		if taken["list"] {
			continue
		}
		list := &cobra.Command{
			Use:    "list",
			Short:  fmt.Sprintf("The same as `mw %s`", group.Name()),
			Args:   group.Args,
			RunE:   group.RunE,
			Hidden: true,
		}
		if !taken["ls"] {
			list.Aliases = []string{"ls"}
		}
		// The same flags, bound to the same variables: `mw groups list
		// --project loja` reads --project as `mw groups --project loja` does.
		list.Flags().AddFlagSet(group.LocalNonPersistentFlags())
		group.AddCommand(list)
	}
}
