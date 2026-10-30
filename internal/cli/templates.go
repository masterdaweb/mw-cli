package cli

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

var engineNames = map[string]string{"postgres": "PostgreSQL", "mysql": "MySQL", "mariadb": "MariaDB", "valkey": "Redis"}

// makes says what a template creates: "3 services, PostgreSQL, Redis, Object Storage".
func makes(t api.Template) string {
	parts := []string{plural(len(t.Services), "service", "services")}
	for _, d := range t.Databases {
		parts = append(parts, engineNames[d.Engine])
	}
	if len(t.Buckets) > 0 {
		parts = append(parts, "Object Storage")
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func templatesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "templates",
		Aliases: []string{"template"},
		Short:   "Start a whole project from a template: services, databases and files already wired",
		Example: "  mw templates\n  mw templates show laravel\n  mw templates deploy laravel --name loja",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var list []api.Template
			if err := c.Do(cmd.Context(), "GET", "/serverless/templates", nil, nil, &list); err != nil {
				return err
			}
			return a.print(list, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "TEMPLATE\tNAME\tMAKES")
				for _, t := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\n", t.ID, t.Name, makes(t))
				}
				fmt.Fprintln(w, "\nSee one: `mw templates show <template>`; deploy it: `mw templates deploy <template> --name <project>`.")
			})
		},
	}

	show := &cobra.Command{
		Use:   "show <template>",
		Short: "What a template makes, what it costs and what it asks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var t api.Template
			if err := c.Do(cmd.Context(), "GET", "/serverless/templates/"+args[0], nil, nil, &t); err != nil {
				return err
			}
			return a.print(t, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s\t%s\n\n", t.Name, t.Summary.EN)
				for _, s := range t.Services {
					what := strings.ToLower(s.Kind)
					if s.CronSchedule != nil {
						what += " (" + *s.CronSchedule + ")"
					}
					if s.Image != nil {
						what += ", image " + *s.Image
					}
					fmt.Fprintf(w, "Service %s\t%s\n", s.Name, what)
				}
				for _, d := range t.Databases {
					fmt.Fprintf(w, "Database\t%s, size %s\n", engineNames[d.Engine], d.Size)
				}
				for _, b := range t.Buckets {
					access := "private (signed links)"
					if b.Public {
						access = "public"
					}
					fmt.Fprintf(w, "Bucket\t%s\n", access)
				}
				if t.RepoURL != nil {
					fmt.Fprintf(w, "Code\t%s\n", strings.TrimSuffix(*t.RepoURL, ".git"))
				}
				if e := t.Estimate; e != nil {
					fmt.Fprintf(w, "\nCost\t%s\n", estimateText(*e, len(t.Databases) > 0))
				}
				for _, q := range t.Questions {
					fmt.Fprintf(w, "Asks\t%s: --answer %s=…\n", q.Label.EN, q.Key)
				}
			})
		},
	}

	var name, location string
	var answers []string
	var detach bool
	deploy := &cobra.Command{
		Use:     "deploy <template>",
		Short:   "Deploy a template as a new project, and follow it until it is live",
		Example: "  mw templates deploy laravel --name loja\n  mw templates deploy laravel --name loja-us --location dallas\n  mw templates deploy directus --name cms --answer ADMIN_EMAIL=ana@loja.com.br --answer ADMIN_PASSWORD=…",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return errors.New("name the project: --name <project>")
			}
			given := map[string]string{}
			for _, kv := range answers {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("--answer takes KEY=VALUE, not %q", kv)
				}
				given[k] = v
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var r api.TemplateDeployResult
			body := map[string]interface{}{"project_name": name, "answers": given}
			if location != "" {
				id, err := a.resolveLocation(cmd.Context(), appLocations, "projects", location)
				if err != nil {
					return err
				}
				body["datacenter_id"] = id
			}
			if err := c.Do(cmd.Context(), "POST", "/serverless/templates/"+args[0]+"/deploy", nil, body, &r); err != nil {
				return err
			}
			return a.followProject(cmd, name, r, detach)
		},
	}
	deploy.Flags().StringVar(&name, "name", "", "the new project's name")
	deploy.Flags().StringVar(&location, "location", "", locationFlag)
	deploy.Flags().StringArrayVar(&answers, "answer", nil, "an answer to the template's question, KEY=VALUE (repeatable)")
	deploy.Flags().BoolVar(&detach, "detach", false, "return once the project is made, without following the deployments")

	cmd.AddCommand(show, deploy)
	return cmd
}

// followProject reports what a whole-project deploy made (a template, a
// repository) and follows each service's first deployment until it is live.
func (a *App) followProject(cmd *cobra.Command, name string, r api.TemplateDeployResult, detach bool) error {
	if a.JSON || detach {
		return a.print(r, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "Project %s made: %d service(s) deploying.\n", name, len(r.Services))
		})
	}
	c, err := a.Client()
	if err != nil {
		return err
	}
	a.printf("Project %s made.\n", name)
	for _, db := range r.Databases {
		a.printf("  database %s: being created (3 to 6 minutes; the services build meanwhile)\n", db)
	}
	for _, b := range r.Buckets {
		a.printf("  bucket %s: ready\n", b)
	}
	var failed []string
	for _, s := range r.Services {
		if s.DeploymentID == nil {
			continue
		}
		var d api.Deployment
		if err := c.Do(cmd.Context(), "GET", "/serverless/deployments/"+*s.DeploymentID, nil, nil, &d); err != nil {
			return err
		}
		a.printf("Deployment #%d of %s:\n", d.Number, s.Name)
		if err := a.followDeployment(cmd.Context(), &d); err != nil {
			a.printf("  %v\n", err)
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s did not go live: `mw logs <service>` and the console say why", strings.Join(failed, ", "))
	}
	a.printf("All live. `mw status` shows the addresses; `mw link` connects this folder to a service.\n")
	return nil
}

// estimateText: "from R$ 140,00 a month (databases); services billed by use".
func estimateText(e api.TemplateEstimate, hasDatabases bool) string {
	total := new(big.Int)
	known := true
	for _, p := range []*string{e.DatabasesMonthlyMicroCents, e.AlwaysOnMonthlyMicroCents} {
		if p == nil {
			known = false
			continue
		}
		v, ok := new(big.Int).SetString(*p, 10)
		if !ok {
			known = false
			continue
		}
		total.Add(total, v)
	}
	usage := "services billed by use"
	if e.Sleeps {
		usage = "services sleep without traffic and are billed by use"
	} else if e.SomeSleep {
		usage = "the site sleeps without traffic and is billed by use"
	}
	if !known || total.Sign() == 0 {
		return usage
	}
	what := "always-on"
	if hasDatabases {
		what = "databases"
	}
	return fmt.Sprintf("from %s a month (%s); %s", money(total.String(), 2), what, usage)
}
