package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

func projectsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Projects: a group of services, and the databases and buckets they use",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Project](cmd.Context(), c, "/serverless/projects", nil)
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tSERVICES\tLIVE\tFAILED\tID")
				for _, p := range all {
					fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", p.Name, p.ServiceCount, p.LiveCount, p.FailedCount, p.ID)
				}
			})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var p api.Project
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects", nil, map[string]string{"name": args[0]}, &p); err != nil {
				return err
			}
			return a.print(p, func(w *tabwriter.Writer) { fmt.Fprintf(w, "Project %s created (%s).\n", p.Name, p.ID) })
		},
	})
	var copyName string
	duplicate := &cobra.Command{
		Use:     "duplicate <project>",
		Short:   "Duplicate a project with data of its own (a staging): new, empty databases and buckets",
		Example: "  mw projects duplicate loja --name loja-staging",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if copyName == "" {
				return fmt.Errorf("name the copy: --name %s-staging", args[0])
			}
			p, err := a.resolveProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var r api.TemplateDeployResult
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/duplicate", nil, map[string]string{"name": copyName}, &r); err != nil {
				return err
			}
			return a.print(r, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Project %s made from %s: %d service(s) deploying.\n", copyName, p.Name, len(r.Services))
				for _, db := range r.Databases {
					fmt.Fprintf(w, "  database %s: new and empty, ready in a few minutes\n", db)
				}
				for _, b := range r.Buckets {
					fmt.Fprintf(w, "  bucket %s: new and empty\n", b)
				}
				fmt.Fprintln(w, "Custom domains stayed with the original; review variables that point at production services elsewhere.")
			})
		},
	}
	duplicate.Flags().StringVar(&copyName, "name", "", "the copy's name")
	cmd.AddCommand(duplicate)

	var (
		protect     bool
		description string
		freeze      string
		unfreeze    bool
	)
	update := &cobra.Command{
		Use:   "update <project>",
		Short: "Change a project's deletion protection, description, or freeze its deploys",
		Example: "  mw projects update loja-staging --deletion-protection=false\n" +
			"  mw projects update loja --freeze \"Black Friday\"\n" +
			"  mw projects update loja --unfreeze",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			patch := map[string]interface{}{}
			f := cmd.Flags()
			if f.Changed("deletion-protection") {
				patch["deletion_protection"] = protect
			}
			if f.Changed("description") {
				if description == "" {
					patch["description"] = nil
				} else {
					patch["description"] = description
				}
			}
			if f.Changed("freeze") && unfreeze {
				return errors.New("--freeze and --unfreeze together: choose one")
			}
			if f.Changed("freeze") {
				patch["deploys_frozen"] = true
				patch["frozen_reason"] = freeze
			}
			if unfreeze {
				patch["deploys_frozen"] = false
				patch["frozen_reason"] = nil
			}
			if len(patch) == 0 {
				return errors.New("nothing to change: pass --deletion-protection, --description, --freeze or --unfreeze")
			}
			c, _ := a.Client()
			var out api.Project
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/projects/"+p.ID, nil, patch, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s saved: deletion protection %s, deploys %s.\n", out.Name, onOff(out.DeletionProtection), map[bool]string{true: "frozen", false: "allowed"}[out.DeploysFrozen])
			})
		},
	}
	update.Flags().BoolVar(&protect, "deletion-protection", true, "protect it from deletion")
	update.Flags().StringVar(&description, "description", "", "what the project is for (\"\" clears it)")
	update.Flags().StringVar(&freeze, "freeze", "", "refuse every new deployment until --unfreeze, saying why")
	update.Flags().BoolVar(&unfreeze, "unfreeze", false, "allow deployments again")
	cmd.AddCommand(update)

	for _, pause := range []bool{true, false} {
		pause := pause
		verb, short := "resume", "Resume a paused project: its services start again"
		if pause {
			verb, short = "pause", "Pause a project: every service stops (its sites say \"temporarily unavailable\") until resumed"
		}
		cmd.AddCommand(&cobra.Command{
			Use:   verb + " <project>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := a.resolveProject(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				c, _ := a.Client()
				var out api.Project
				if err := c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/"+verb, nil, nil, &out); err != nil {
					return err
				}
				return a.print(out, func(w *tabwriter.Writer) {
					if pause {
						fmt.Fprintf(w, "%s paused: its services stop and bill nothing for instances; `mw projects resume %s` starts them again.\n", out.Name, out.Name)
					} else {
						fmt.Fprintf(w, "%s resumed: its services start again.\n", out.Name)
					}
				})
			},
		})
	}

	var confirm string
	del := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project and its services (protection off, name confirmed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if confirm != p.Name {
				return fmt.Errorf("type the project's name to confirm: --confirm %s", p.Name)
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/projects/"+p.ID, url.Values{"confirm": {confirm}}, nil, nil); err != nil {
				return err
			}
			a.printf("Project %s is being deleted.\n", p.Name)
			return nil
		},
	}
	del.Flags().StringVar(&confirm, "confirm", "", "the project's name, to confirm")
	cmd.AddCommand(del)
	return cmd
}

func serviceLine(s api.Service) string {
	url := ""
	if s.URL != nil {
		url = *s.URL
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", s.Name, s.ProjectName, strings.ToLower(s.Kind), strings.ToLower(s.Status), url)
}

func servicesCmd(a *App) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:     "services",
		Aliases: []string{"service", "svc"},
		Short:   "Services: web apps, private services, workers and cron jobs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if project != "" {
				p, err := a.resolveProject(cmd.Context(), project)
				if err != nil {
					return err
				}
				q.Set("project_id", p.ID)
			}
			all, err := api.All[api.Service](cmd.Context(), c, "/serverless/services", q)
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tPROJECT\tKIND\tSTATUS\tURL")
				for _, s := range all {
					fmt.Fprintln(w, serviceLine(s))
				}
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project's services")

	var (
		image, kind, repo, branch, provider, connection, registry string
		port                                                      int
		envs                                                      []string
	)
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a service from a repository or an image, and deploy it",
		Long: "Create a service in a project and deploy it.\n" +
			"From a repository (--repo ana/loja, connected in the console's Settings › Integrations, so\n" +
			"every push deploys; or the https address of a public one): built from its code.\n" +
			"From an image (--image nginx:1.27): run as it is.\n" +
			"A whole app with its databases at once: `mw launch`.",
		Example: "  mw services create api --project loja --repo ana/loja --branch main\n" +
			"  mw services create site --project loja --repo https://github.com/ana/site\n" +
			"  mw services create proxy --project loja --image nginx:1.27 --port 80",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return errors.New("which project? pass --project")
			}
			if (image == "") == (repo == "") {
				return errors.New("pass --repo (a repository) or --image (an image), one of them")
			}
			p, err := a.resolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			vars, err := parseAssignments(envs)
			if err != nil {
				return err
			}
			img := map[string]string{"ref": image}
			if registry != "" {
				if image == "" {
					return errors.New("--registry goes with --image")
				}
				r, err := a.registryCredential(cmd.Context(), registry)
				if err != nil {
					return err
				}
				img["registry_credential_id"] = r.ID
			}
			source := map[string]interface{}{"kind": "IMAGE", "image": img}
			var public map[string]interface{}
			if repo != "" {
				git, fallback, _, err := a.gitSource(cmd, repo, provider, connection, branch)
				if err != nil {
					return err
				}
				source = map[string]interface{}{"kind": "GIT", "git": git}
				if fallback != nil {
					public = map[string]interface{}{"kind": "GIT", "git": fallback}
				}
			}
			body := map[string]interface{}{
				"name":   args[0],
				"kind":   strings.ToUpper(kind),
				"source": source,
			}
			if port > 0 {
				body["settings"] = map[string]int{"port": port}
			}
			if len(vars) > 0 {
				var list []map[string]string
				for _, v := range vars {
					list = append(list, map[string]string{"key": v[0], "value": v[1]})
				}
				body["variables"] = list
			}
			c, _ := a.Client()
			var out struct {
				Service    api.Service     `json:"service"`
				Deployment *api.Deployment `json:"deployment"`
			}
			err = c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/services", nil, body, &out)
			// A connected account that cannot see it: someone else's public repository.
			if err != nil && public != nil && errors.Is(err, api.Code("INVALID_SOURCE")) {
				body["source"] = public
				err = c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/services", nil, body, &out)
			}
			if err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Service %s created in %s.\n", out.Service.Name, p.Name)
				if out.Deployment != nil {
					fmt.Fprintf(w, "Deployment #%d started: `mw logs --service %s` follows it.\n", out.Deployment.Number, out.Service.Name)
				}
			})
		},
	}
	create.Flags().StringVar(&project, "project", "", "the project to create it in")
	create.Flags().StringVar(&repo, "repo", "", "a repository: owner/name (connected in the console) or a public https address")
	create.Flags().StringVar(&branch, "branch", "", "the branch to deploy (default: the repository's default branch)")
	create.Flags().StringVar(&provider, "provider", "", "github, gitlab or bitbucket, when more than one is connected")
	create.Flags().StringVar(&connection, "connection", "", "the connected account's id, when one provider has several")
	create.Flags().StringVar(&image, "image", "", "an image to run, e.g. ghcr.io/acme/app:1.4")
	create.Flags().StringVar(&registry, "registry", "", "the credential a private image is pulled with (`mw registry`)")
	create.Flags().StringVar(&kind, "kind", "web", "web, private, worker or cron")
	create.Flags().IntVar(&port, "port", 0, "the port the app listens on (web and private)")
	create.Flags().StringArrayVar(&envs, "env", nil, "a variable, KEY=value (repeat)")
	cmd.AddCommand(create)

	var protectService bool
	updateService := &cobra.Command{
		Use:     "update <service>",
		Short:   "Change a service's deletion protection",
		Example: "  mw services update web --deletion-protection=true",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("deletion-protection") {
				return errors.New("nothing to change: pass --deletion-protection")
			}
			s, err := a.resolveService(cmd.Context(), args[0], project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Service
			if err := c.Do(cmd.Context(), "PUT", "/serverless/services/"+s.ID+"/deletion-protection", nil, map[string]bool{"enabled": protectService}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s saved: deletion protection %s.\n", out.Name, onOff(out.DeletionProtect))
			})
		},
	}
	updateService.Flags().BoolVar(&protectService, "deletion-protection", true, "protect it from deletion")
	updateService.Flags().StringVar(&project, "project", "", "the service's project, when its name repeats")
	cmd.AddCommand(updateService)

	var hookDisable bool
	hook := &cobra.Command{
		Use:   "hook <service>",
		Short: "A URL that deploys the service when anything POSTs to it (any CI); --disable turns it off",
		Long: "Creates the service's deploy-hook URL, or replaces it: the old one stops working. The URL is\n" +
			"shown this once — keep it as a secret in your CI. A POST to it deploys the latest commit or image.",
		Example: "  mw services hook web\n  curl -X POST \"$MW_DEPLOY_HOOK\"\n  mw services hook web --disable",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), args[0], project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if hookDisable {
				if err := c.Do(cmd.Context(), "DELETE", "/serverless/services/"+s.ID+"/deploy-hook", nil, nil, nil); err != nil {
					return err
				}
				a.printf("Deploy hook of %s disabled.\n", s.Name)
				return nil
			}
			var out struct {
				URL       string `json:"url"`
				RotatedAt string `json:"rotated_at"`
			}
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/deploy-hook", nil, nil, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s\n\nThe deploy hook of %s: shown this once, keep it secret (any earlier one stopped working).\n", out.URL, s.Name)
			})
		},
	}
	hook.Flags().BoolVar(&hookDisable, "disable", false, "turn the URL off")
	hook.Flags().StringVar(&project, "project", "", "the service's project, when its name repeats")
	cmd.AddCommand(hook)

	var confirm string
	del := &cobra.Command{
		Use:   "delete <service>",
		Short: "Delete a service (protection off, name confirmed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), args[0], project)
			if err != nil {
				return err
			}
			if confirm != s.Name {
				return fmt.Errorf("type the service's name to confirm: --confirm %s", s.Name)
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/services/"+s.ID, url.Values{"confirm": {confirm}}, nil, nil); err != nil {
				return err
			}
			a.printf("Service %s is being deleted.\n", s.Name)
			return nil
		},
	}
	del.Flags().StringVar(&confirm, "confirm", "", "the service's name, to confirm")
	del.Flags().StringVar(&project, "project", "", "the service's project, when its name repeats")
	cmd.AddCommand(del)
	return cmd
}

func linkCmd(a *App) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "link <service>",
		Short: "Link this directory to a service, so commands here need no --service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), args[0], project)
			if err != nil {
				return err
			}
			if err := config.SaveLink(a.Dir, config.Link{ServiceID: s.ID, ServiceName: s.Name, ProjectID: s.ProjectID}); err != nil {
				return err
			}
			a.printf("Linked %s to %s (%s). Saved in .mw/link.json.\n", a.Dir, s.Name, s.ProjectName)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "the service's project, when its name repeats")
	return cmd
}

// serviceFlag adds --service / --project to a command acting on one service.
func serviceFlag(cmd *cobra.Command, service, project *string) {
	cmd.Flags().StringVarP(service, "service", "s", "", "the service (default: the one this directory is linked to)")
	cmd.Flags().StringVar(project, "project", "", "the service's project, when its name repeats")
}

func statusCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "A service: its state, address, instances and last deployment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			return a.print(s, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Service\t%s (%s, %s)\n", s.Name, strings.ToLower(s.Kind), s.ProjectName)
				status := strings.ToLower(s.Status)
				if s.Status == "SLEEPING" {
					status += " — wakes up on the next request"
				}
				if s.StatusReason != nil && *s.StatusReason != "" {
					status += " (" + *s.StatusReason + ")"
				}
				fmt.Fprintf(w, "Status\t%s\n", status)
				if s.URL != nil {
					fmt.Fprintf(w, "URL\t%s\n", *s.URL)
				}
				if s.InternalURL != nil {
					fmt.Fprintf(w, "Internal URL\t%s\n", *s.InternalURL)
				}
				fmt.Fprintf(w, "Instances\t%d ready (min %d, max %d)\n", s.ReadyInstances, s.Settings.MinInstances, s.Settings.MaxInstances)
				fmt.Fprintf(w, "Size\t%s\n", sizeText(s.Settings))
				if d := s.LastDeployment; d != nil {
					fmt.Fprintf(w, "Last deployment\t#%d %s (%s)\n", d.Number, strings.ToLower(d.Status), d.CreatedAt)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

func sizeText(s api.ServiceSettings) string {
	label := ""
	if s.Size != nil {
		label = *s.Size + " · "
	}
	return label + strconv.FormatFloat(float64(s.VCPUMillis)/1000, 'f', -1, 64) + " vCPU · " + strconv.Itoa(s.MemoryMiB) + " MiB"
}

// parseAssignments reads KEY=value pairs.
func parseAssignments(items []string) ([][2]string, error) {
	var out [][2]string
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%q is not KEY=value", it)
		}
		out = append(out, [2]string{k, v})
	}
	return out, nil
}
