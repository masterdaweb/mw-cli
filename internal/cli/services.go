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
		Short:   "Projects: what belongs to one system — instances, apps, databases and buckets",
		Long: "A project groups what belongs to the same system or client: VPS instances, serverless\n" +
			"services, databases and buckets. `mw projects resources` lists and changes what is in one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := a.projects(cmd.Context())
			if err != nil {
				return err
			}
			ids := make([]*int64, len(all))
			for i := range all {
				ids[i] = all[i].DatacenterID
			}
			names := a.locationNames(cmd.Context(), ids...)
			l := listing{headers: []string{"NAME", "LOCATION", "INSTANCES", "SERVICES", "LIVE", "FAILED", "DATABASES", "BUCKETS", "ID"}, empty: "No projects yet: `mw projects create <name>`."}
			for _, p := range all {
				l.add(p.ID, p.Name, locationText(names, p.DatacenterID), fmt.Sprint(p.InstanceCount), fmt.Sprint(p.ServiceCount), fmt.Sprint(p.LiveCount), fmt.Sprint(p.FailedCount), fmt.Sprint(p.DatabaseCount), fmt.Sprint(p.BucketCount), p.ID)
			}
			return a.printList(all, l)
		},
	}
	var createLocation string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a project, in the location you pick (default: the platform's)",
		Example: "  mw projects create loja\n" +
			"  mw projects create loja-us --location dallas",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"name": args[0]}
			if createLocation != "" {
				id, err := a.resolveLocation(cmd.Context(), appLocations, "projects", createLocation)
				if err != nil {
					return err
				}
				body["datacenter_id"] = id
			}
			var p api.Project
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects", nil, body, &p); err != nil {
				return err
			}
			return a.print(p, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Project %s created in %s (%s).\n", p.Name, locationText(a.locationNames(cmd.Context(), p.DatacenterID), p.DatacenterID), p.ID)
			})
		},
	}
	create.Flags().StringVar(&createLocation, "location", "", locationFlag)
	cmd.AddCommand(create)
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
		previews    bool
	)
	update := &cobra.Command{
		Use:   "update <project>",
		Short: "Change a project's deletion protection, description, preview environments, or freeze its deploys",
		Example: "  mw projects update loja-staging --deletion-protection=false\n" +
			"  mw projects update loja --freeze \"Black Friday\"\n" +
			"  mw projects update loja --unfreeze\n" +
			"  mw projects update loja --previews",
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
			if f.Changed("previews") {
				patch["previews_enabled"] = previews
			}
			if len(patch) == 0 {
				return errors.New("nothing to change: pass --deletion-protection, --description, --previews, --freeze or --unfreeze")
			}
			c, _ := a.Client()
			var out api.Project
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/projects/"+p.ID, nil, patch, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s saved: deletion protection %s, deploys %s.", out.Name, onOff(out.DeletionProtection), map[bool]string{true: "frozen", false: "allowed"}[out.DeploysFrozen])
				if f.Changed("previews") {
					fmt.Fprintf(w, " Preview environments %s.", onOff(out.PreviewsEnabled))
				}
				fmt.Fprintln(w)
			})
		},
	}
	update.Flags().BoolVar(&protect, "deletion-protection", true, "protect it from deletion")
	update.Flags().BoolVar(&previews, "previews", true, "a copy of the project for each pull request against a branch it deploys, removed when it closes")
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

	cmd.AddCommand(&cobra.Command{
		Use:   "previews <project>",
		Short: "A project's preview environments: one per open pull request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var list []api.PreviewEnvironment
			if err := c.Do(cmd.Context(), "GET", "/serverless/projects/"+p.ID+"/previews", nil, nil, &list); err != nil {
				return err
			}
			return a.print(list, func(w *tabwriter.Writer) {
				if len(list) == 0 {
					if p.PreviewsEnabled {
						fmt.Fprintf(w, "No preview open for %s: the next pull request gets one.\n", p.Name)
					} else {
						fmt.Fprintf(w, "Preview environments are off for %s: `mw projects update %s --previews` turns them on.\n", p.Name, p.Name)
					}
					return
				}
				fmt.Fprintln(w, "PULL REQUEST\tBRANCH\tPROJECT\tADDRESSES")
				for _, e := range list {
					pr := e.Project.Preview
					if pr == nil {
						continue
					}
					var urls []string
					for _, s := range e.Services {
						if s.URL != nil {
							urls = append(urls, *s.URL)
						}
					}
					fmt.Fprintf(w, "#%d\t%s\t%s\t%s\n", pr.Number, pr.Branch, e.Project.Name, strings.Join(urls, ", "))
				}
			})
		},
	})

	var confirm string
	del := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project: its services go; instances, databases and buckets only leave it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if confirm != p.Name {
				return fmt.Errorf("deleting %s tears its serverless services down (its instances, databases and buckets stay, outside any project): confirm with --confirm %s", p.Name, p.Name)
			}
			base, err := a.projectsBase(cmd.Context())
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", base+"/"+p.ID, url.Values{"confirm": {confirm}}, nil, nil); err != nil {
				return err
			}
			a.printf("Project %s is being deleted.\n", p.Name)
			return nil
		},
	}
	del.Flags().StringVar(&confirm, "confirm", "", "the project's name, to confirm")
	cmd.AddCommand(del, projectResourcesCmd(a))
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
		image, kind, repo, branch, provider, connection, registry, volume string
		port                                                              int
		envs                                                              []string
	)
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a service from a repository or an image, and deploy it",
		Long: "Create a service in a project and deploy it.\n" +
			"From a repository (--repo ana/loja, connected in the console's Settings › Integrations, so\n" +
			"every push deploys; or the https address of a public one): built from its code.\n" +
			"From an image (--image nginx:1.27): run as it is; the paths it keeps data in that no volume\n" +
			"holds are named once it is made.\n" +
			"--volume /data:5 gives it a persistent volume of 5 GB at /data (it then runs one instance).\n" +
			"A whole app with its databases at once: `mw launch`.",
		Example: "  mw services create api --project loja --repo ana/loja --branch main\n" +
			"  mw services create site --project loja --repo https://github.com/ana/site\n" +
			"  mw services create proxy --project loja --image nginx:1.27 --port 80\n" +
			"  mw services create blog --project loja --image ghost:6 --port 2368 --volume /var/lib/ghost/content:5",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return errors.New("which project? pass --project")
			}
			if (image == "") == (repo == "") {
				return errors.New("pass --repo (a repository) or --image (an image), one of them")
			}
			mount, size := "", 0
			if volume != "" {
				var err error
				if mount, size, err = parseVolume(volume); err != nil {
					return err
				}
				if strings.EqualFold(kind, "cron") {
					return errors.New("a cron job cannot have a volume: give one to the service that keeps the data")
				}
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
			settings := map[string]interface{}{}
			if port > 0 {
				settings["port"] = port
			}
			if mount != "" {
				settings["volume_mount_path"], settings["volume_size_gb"] = mount, size
				// A volume has one writer: the service runs one instance.
				if strings.EqualFold(kind, "worker") {
					settings["instances"] = 1
				} else {
					settings["max_instances"] = 1
				}
			}
			if len(settings) > 0 {
				body["settings"] = settings
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
			// What the image keeps where no volume holds it: told, not refused.
			var hints []string
			if image != "" && !strings.EqualFold(kind, "cron") {
				inspect := map[string]string{"image": image}
				if id := img["registry_credential_id"]; id != "" {
					inspect["registry_credential_id"] = id
				}
				hints = a.imageVolumeHints(cmd.Context(), inspect, out.Service.Name, mount)
			}
			if a.JSON {
				for _, h := range hints {
					fmt.Fprintln(a.Err, h)
				}
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Service %s created in %s.\n", out.Service.Name, p.Name)
				if out.Deployment != nil {
					fmt.Fprintf(w, "Deployment #%d started: `mw logs --service %s` follows it.\n", out.Deployment.Number, out.Service.Name)
				}
				for _, h := range hints {
					fmt.Fprintln(w, h)
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
	create.Flags().StringVar(&volume, "volume", "", "a persistent volume, PATH:GB like /data:5 (the service then runs one instance)")
	cmd.AddCommand(create)

	var protectService bool
	var rolloutPercent, rolloutMinutes int
	updateService := &cobra.Command{
		Use:   "update <service>",
		Short: "Change a service's deletion protection, or how its new versions take traffic",
		Long: "--rollout 10 makes a web or private service's new versions take 10% of the requests first,\n" +
			"for --rollout-minutes (default 10), and all of them only if they stay healthy; 0 turns it off.\n" +
			"It applies from the next deployment.",
		Example: "  mw services update web --deletion-protection=true\n  mw services update web --rollout 10 --rollout-minutes 15",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rollout := cmd.Flags().Changed("rollout") || cmd.Flags().Changed("rollout-minutes")
			if !cmd.Flags().Changed("deletion-protection") && !rollout {
				return errors.New("nothing to change: pass --deletion-protection or --rollout")
			}
			s, err := a.resolveService(cmd.Context(), args[0], project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Service
			if cmd.Flags().Changed("deletion-protection") {
				if err := c.Do(cmd.Context(), "PUT", "/serverless/services/"+s.ID+"/deletion-protection", nil, map[string]bool{"enabled": protectService}, &out); err != nil {
					return err
				}
			}
			if rollout {
				settings := map[string]interface{}{}
				if cmd.Flags().Changed("rollout") {
					if rolloutPercent == 0 {
						settings["rollout_percent"] = nil
					} else {
						settings["rollout_percent"] = rolloutPercent
					}
				}
				if cmd.Flags().Changed("rollout-minutes") {
					settings["rollout_minutes"] = rolloutMinutes
				}
				var saved struct {
					Service api.Service `json:"service"`
				}
				if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/settings", nil,
					map[string]interface{}{"settings": settings, "deploy": false}, &saved); err != nil {
					return err
				}
				out = saved.Service
			}
			return a.print(out, func(w *tabwriter.Writer) {
				if cmd.Flags().Changed("deletion-protection") {
					fmt.Fprintf(w, "%s saved: deletion protection %s.\n", out.Name, onOff(out.DeletionProtect))
				}
				if rollout {
					if p := out.Settings.RolloutPercent; p != nil && *p > 0 {
						fmt.Fprintf(w, "%s saved: new versions take %d%% of the requests for %d minutes first, from the next deployment.\n", out.Name, *p, out.Settings.RolloutMinutes)
					} else {
						fmt.Fprintf(w, "%s saved: new versions take every request at once.\n", out.Name)
					}
				}
			})
		},
	}
	updateService.Flags().BoolVar(&protectService, "deletion-protection", true, "protect it from deletion")
	updateService.Flags().IntVar(&rolloutPercent, "rollout", 0, "web and private: a new version's first share of the requests, 1-50 (0: off)")
	updateService.Flags().IntVar(&rolloutMinutes, "rollout-minutes", 10, "how long a gradual rollout lasts, 1-60")
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
