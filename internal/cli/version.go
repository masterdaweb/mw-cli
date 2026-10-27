package cli

import (
	"fmt"
	"runtime"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

func versionCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "The CLI's version, and the API it talks to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			saved, _ := config.Load()
			info := map[string]string{
				"version": a.Version,
				"go":      runtime.Version(),
				"os":      runtime.GOOS,
				"arch":    runtime.GOARCH,
				"api_url": firstNonEmpty(a.apiURL, saved.APIURL, api.DefaultBaseURL),
			}
			if a.JSON {
				return a.encode(info)
			}
			fmt.Fprintf(a.Out, "mw %s (%s, %s/%s)\nAPI %s\n", info["version"], info["go"], info["os"], info["arch"], info["api_url"])
			return nil
		},
	}
}

// addGroups sorts the commands into the sections `mw --help` shows.
func addGroups(root *cobra.Command) {
	groups := []struct {
		id, title string
		names     []string
	}{
		{"infra", "Instances (VPS):", []string{"instances", "snapshots", "backups", "ssh-keys", "regions", "plans", "images", "custom-images", "addons", "orders", "operations"}},
		{"network", "Network:", []string{"firewalls", "vpcs", "vpn", "dns", "rdns"}},
		{"security", "Security:", []string{"security"}},
		{"apps", "Serverless apps:", []string{"projects", "services", "init", "link", "status", "deploy", "redeploy", "restart", "rollback", "deployments", "logs", "scale", "run", "env", "domains", "bindings", "templates", "launch", "groups", "registry", "metrics", "alerts", "egress", "outbound-ips", "drains"}},
		{"data", "Databases and storage:", []string{"db", "redis", "storage"}},
		{"billing", "Billing:", []string{"subscriptions", "invoices", "wallet", "usage"}},
		{"account", "Account:", []string{"login", "logout", "whoami", "activity", "webhooks", "version"}},
	}
	byName := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		byName[c.Name()] = c
	}
	for _, g := range groups {
		root.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, n := range g.names {
			if c, ok := byName[n]; ok {
				c.GroupID = g.id
			}
		}
	}
	// Whatever is left (help, completion) goes under "Additional Commands".
}
