package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

// LogDrain is a project's log destination.
type LogDrain struct {
	ID                  string  `json:"id"`
	ProjectID           string  `json:"project_id"`
	Kind                string  `json:"kind"`
	URL                 string  `json:"url"`
	Enabled             bool    `json:"enabled"`
	Status              string  `json:"status"`
	HasToken            bool    `json:"has_token"`
	LastSentAt          *string `json:"last_sent_at"`
	LastError           *string `json:"last_error"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	LinesSent           string  `json:"lines_sent"`
	CreatedAt           string  `json:"created_at"`
}

// drainsCmd: where a project's logs are also sent — an HTTPS endpoint of
// yours, Datadog or Better Stack, near real time.
func drainsCmd(a *App) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:     "drains",
		Aliases: []string{"drain", "log-drains"},
		Short:   "Log drains: a project's logs also sent to your endpoint, Datadog or Better Stack",
		Long: "Every service's output of a project, also sent to your own tool near real time (about\n" +
			"30 s behind). An HTTPS endpoint gets NDJSON lines {timestamp, message, project, service,\n" +
			"revision, instance}; Datadog and Better Stack get their own formats. A destination that\n" +
			"keeps failing stops after 50 sends in a row and says why; turn it back on to resume.",
		Example: "  mw drains --project loja\n" +
			"  mw drains add --project loja --url https://logs.loja.com.br/ingest --token 'Bearer …'\n" +
			"  mw drains add --project loja --datadog --site eu1 --token $DD_API_KEY\n" +
			"  mw drains add --project loja --betterstack --token $BETTERSTACK_SOURCE_TOKEN\n" +
			"  mw drains test 0193f0e2 --project loja\n" +
			"  mw drains off 0193f0e2 --project loja",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, _, err := a.drains(cmd.Context(), project)
			if err != nil {
				return err
			}
			return a.print(list, func(w *tabwriter.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No log drains yet: `mw drains add --project <project> --url https://…`.")
					return
				}
				fmt.Fprintln(w, "ID\tDESTINATION\tSTATUS\tLAST SENT\tLINES")
				for _, d := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", shortID(d.ID), drainDestination(d), d.Status, whenPtr(d.LastSentAt), d.LinesSent)
					if d.LastError != nil && *d.LastError != "" {
						fmt.Fprintf(w, "\t  %s\n", *d.LastError)
					}
				}
			})
		},
	}
	cmd.PersistentFlags().StringVar(&project, "project", "", "the project (name or id; default: the linked one)")

	var url, token, site string
	var datadog, betterstack bool
	add := &cobra.Command{
		Use:   "add",
		Short: "Send the project's logs to an HTTPS endpoint, Datadog or Better Stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind := "HTTPS"
			switch {
			case datadog && betterstack:
				return errors.New("pick one: --datadog or --betterstack")
			case datadog:
				kind = "DATADOG"
			case betterstack:
				kind = "BETTERSTACK"
			}
			if kind == "HTTPS" && url == "" {
				return errors.New("give the endpoint: --url https://…")
			}
			if kind != "HTTPS" && token == "" {
				return errors.New("give the token: --token …")
			}
			p, err := a.drainProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"kind": kind}
			if url != "" {
				body["url"] = url
			}
			if token != "" {
				body["token"] = token
			}
			if kind == "DATADOG" && site != "" {
				body["site"] = site
			}
			var d LogDrain
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/log-drains", nil, body, &d); err != nil {
				return err
			}
			return a.print(d, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Sending %s's logs to %s from now on (%s).\n", p.Name, drainDestination(d), shortID(d.ID))
				fmt.Fprintln(w, "Check it with `mw drains test "+shortID(d.ID)+"`.")
			})
		},
	}
	add.Flags().StringVar(&url, "url", "", "HTTPS: the endpoint. Better Stack: the source's ingesting host")
	add.Flags().StringVar(&token, "token", "", "Datadog's API key, Better Stack's source token, or the Authorization header for an endpoint")
	add.Flags().StringVar(&site, "site", "", "Datadog's site: us1 (default), us3, us5, eu1, ap1, ap2")
	add.Flags().BoolVar(&datadog, "datadog", false, "send to Datadog")
	add.Flags().BoolVar(&betterstack, "betterstack", false, "send to Better Stack")

	test := &cobra.Command{
		Use:   "test <id>",
		Short: "Send one line now, to see that the destination takes it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.drain(cmd.Context(), project, args[0])
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var r struct {
				OK     bool    `json:"ok"`
				Status *int    `json:"status"`
				Error  *string `json:"error"`
			}
			if err := c.Do(cmd.Context(), "POST", "/serverless/log-drains/"+d.ID+"/test", nil, nil, &r); err != nil {
				return err
			}
			if !r.OK {
				return fmt.Errorf("%s refused the test line: %s", drainDestination(*d), str(r.Error))
			}
			fmt.Fprintf(a.Out, "%s took the test line.\n", drainDestination(*d))
			return nil
		},
	}

	toggle := func(use, short string, enabled bool) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				d, err := a.drain(cmd.Context(), project, args[0])
				if err != nil {
					return err
				}
				c, err := a.Client()
				if err != nil {
					return err
				}
				var out LogDrain
				if err := c.Do(cmd.Context(), "PATCH", "/serverless/log-drains/"+d.ID, nil, map[string]interface{}{"enabled": enabled}, &out); err != nil {
					return err
				}
				fmt.Fprintf(a.Out, "%s: %s.\n", drainDestination(out), out.Status)
				return nil
			},
		}
	}

	remove := &cobra.Command{
		Use:   "remove <id>",
		Short: "Stop sending to this destination and forget it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.drain(cmd.Context(), project, args[0])
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/log-drains/"+d.ID, nil, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Logs no longer go to %s.\n", drainDestination(*d))
			return nil
		},
	}

	cmd.AddCommand(add, test, toggle("on", "Turn a log drain back on (it resumes from where it stopped)", true),
		toggle("off", "Turn a log drain off", false), remove)
	return cmd
}

func drainDestination(d LogDrain) string {
	switch d.Kind {
	case "DATADOG":
		return "Datadog"
	case "BETTERSTACK":
		return "Better Stack"
	default:
		return d.URL
	}
}

// drainProject is the project named, or the one this folder is linked to.
func (a *App) drainProject(ctx context.Context, ref string) (*api.Project, error) {
	if ref == "" {
		link, _, err := config.FindLink(a.Dir)
		if err != nil || link == nil || link.ProjectID == "" {
			return nil, errors.New("pass --project (or run `mw link` in the app's folder)")
		}
		ref = link.ProjectID
	}
	return a.resolveProject(ctx, ref)
}

func (a *App) drains(ctx context.Context, project string) ([]LogDrain, *api.Project, error) {
	p, err := a.drainProject(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	c, err := a.Client()
	if err != nil {
		return nil, nil, err
	}
	var list []LogDrain
	if err := c.Do(ctx, "GET", "/serverless/projects/"+p.ID+"/log-drains", nil, nil, &list); err != nil {
		return nil, nil, err
	}
	return list, p, nil
}

// drain is the project's drain whose id starts with ref.
func (a *App) drain(ctx context.Context, project, ref string) (*LogDrain, error) {
	list, p, err := a.drains(ctx, project)
	if err != nil {
		return nil, err
	}
	var found []LogDrain
	for _, d := range list {
		if strings.HasPrefix(d.ID, ref) {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s has no log drain %s", p.Name, ref)
	case 1:
		return &found[0], nil
	default:
		return nil, fmt.Errorf("%s matches more than one log drain: give more of its id", ref)
	}
}
