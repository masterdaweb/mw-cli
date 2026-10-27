package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// ── Gradual rollouts (`mw deployments promote|abort`) ──────────────────────

// rollingOut: a deployment whose new version takes part of the requests.
func rollingOut(d *api.Deployment) bool { return d.Status == "DEPLOYING" && d.Rollout != nil }

func deploymentsRolloutCmd(a *App, action string) *cobra.Command {
	var service, project string
	short := "Give a gradual rollout's new version every request now (default: the one rolling out)"
	long := "A deployment rolling out gradually sends part of the requests to its new version until its\n" +
		"time is up. promote gives it every request now; abort gives them all back to the version live\n" +
		"until then, and ends the deployment."
	if action == "abort" {
		short = "Stop a gradual rollout: every request back on the version live until then"
	}
	cmd := &cobra.Command{
		Use:     action + " [<number>|<id>]",
		Short:   short,
		Long:    long,
		Example: "  mw deployments " + action + "\n  mw deployments " + action + " 15 --service web",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			target, err := a.deploymentByArg(cmd.Context(), service, project, args, rollingOut, "rolling out")
			if err != nil {
				return err
			}
			var d api.Deployment
			if err := c.Do(cmd.Context(), "POST", "/serverless/deployments/"+target.ID+"/rollout/"+action, nil, nil, &d); err != nil {
				return err
			}
			return a.print(d, func(w *tabwriter.Writer) {
				if action == "promote" {
					fmt.Fprintf(w, "Deployment #%d: its new version takes every request in a few seconds.\n", d.Number)
				} else {
					fmt.Fprintf(w, "Deployment #%d: every request goes back to the version live until now, in a few seconds.\n", d.Number)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

// deploymentByArg is the deployment an id or a number names, or else the
// service's one that `fits` (the one rolling out, the latest built).
func (a *App) deploymentByArg(ctx context.Context, service, project string, args []string,
	fits func(*api.Deployment) bool, what string) (*api.Deployment, error) {
	c, _ := a.Client()
	if len(args) == 1 && uuidRe.MatchString(args[0]) {
		var d api.Deployment
		if err := c.Do(ctx, "GET", "/serverless/deployments/"+args[0], nil, nil, &d); err != nil {
			return nil, err
		}
		return &d, nil
	}
	s, err := a.resolveService(ctx, service, project)
	if err != nil {
		return nil, err
	}
	var page api.Page[api.Deployment]
	if err := c.Do(ctx, "GET", "/serverless/services/"+s.ID+"/deployments", url.Values{"limit": {"50"}}, nil, &page); err != nil {
		return nil, err
	}
	if len(args) == 1 {
		want := strings.TrimPrefix(args[0], "#")
		for i := range page.Data {
			if strconv.Itoa(page.Data[i].Number) == want {
				return &page.Data[i], nil
			}
		}
		return nil, fmt.Errorf("%s has no deployment #%s among its last 50", s.Name, want)
	}
	for i := range page.Data {
		if fits(&page.Data[i]) {
			return &page.Data[i], nil
		}
	}
	return nil, fmt.Errorf("no deployment of %s is %s", s.Name, what)
}

// ── Build log (`mw deployments build-log`) ─────────────────────────────────

func deploymentsBuildLogCmd(a *App) *cobra.Command {
	var service, project string
	var follow bool
	cmd := &cobra.Command{
		Use:   "build-log [<number>|<id>]",
		Short: "What a deployment's build printed (default: the latest built); -f follows one building",
		Long: "The build's output, each step in order: the language found, the packages restored, the\n" +
			"compiler's errors. Kept for a service's 50 newest deployments (30 days). A rollback, a restart\n" +
			"or a settings change builds nothing, and has none.",
		Example: "  mw deployments build-log\n  mw deployments build-log 15 -f",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			built := func(d *api.Deployment) bool {
				for _, s := range d.Steps {
					if s.Kind == "BUILD" && s.Status != "PENDING" && s.Status != "SKIPPED" {
						return true
					}
				}
				return false
			}
			target, err := a.deploymentByArg(cmd.Context(), service, project, args, built, "built")
			if err != nil {
				return err
			}
			printed := ""
			for {
				var view api.BuildLog
				if err := c.Do(cmd.Context(), "GET", "/serverless/deployments/"+target.ID+"/build-log", nil, nil, &view); err != nil {
					return err
				}
				if a.JSON {
					return a.print(view, nil)
				}
				if view.State == "none" {
					return fmt.Errorf("deployment #%d has no build log: nothing was built for it, or it is older than what is kept", target.Number)
				}
				fmt.Fprint(a.Out, newLines(printed, view.Log))
				printed = view.Log
				if view.State != "running" || !follow {
					if view.Truncated {
						a.printf("(only the last part was kept: the build printed more than 1 MB)\n")
					}
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing while it builds")
	return cmd
}

// newLines is what `now` holds past what was printed: a live log is its
// last lines, so the end of `before` is found again in it.
func newLines(before, now string) string {
	if before == "" {
		return withNewline(now)
	}
	if strings.HasPrefix(now, before) {
		return withNewline(now[len(before):])
	}
	lines := strings.Split(strings.TrimRight(before, "\n"), "\n")
	last := lines[len(lines)-1]
	if i := strings.LastIndex(now, last+"\n"); i >= 0 && last != "" {
		return withNewline(now[i+len(last)+1:])
	}
	return withNewline(now)
}

func withNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
