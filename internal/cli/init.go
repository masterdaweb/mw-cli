package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
	"github.com/spf13/cobra"
)

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// nameFrom makes a folder's name a valid service or project name: lowercase
// letters, digits and hyphens, starting with a letter.
func nameFrom(folder string) string {
	n := notLabel.ReplaceAllString(strings.ToLower(folder), "-")
	n = strings.Trim(n, "-")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "app-" + n
	}
	return strings.Trim(n, "-")
}

func initCmd(a *App) *cobra.Command {
	var project, name, kind, location string
	var port int
	var force bool
	cmd := &cobra.Command{
		Use:   "init [folder]",
		Short: "Make a service deployed from this folder, and link the folder to it",
		Long: "Make a service whose code is this folder (or the one given): `mw deploy` then packs it,\n" +
			"sends it and deploys it. The project is made too if it does not exist. Names default to\n" +
			"the folder's. The folder is linked (.mw/link.json), so commands run in it need no --service.",
		Example: "  mw init\n  mw init --project loja --name api --port 8080\n  mw init --kind worker",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := a.Dir
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			if l, at, err := config.FindLink(abs); err != nil {
				return err
			} else if l != nil && at == abs && !force {
				return fmt.Errorf("this folder is already linked to %s: `mw deploy` deploys it (or --force to make another service)", l.ServiceName)
			}
			base := nameFrom(filepath.Base(abs))
			if name == "" {
				name = base
			}
			if project == "" {
				project = base
			}
			p, err := a.findOrCreateProject(cmd.Context(), project, location)
			if err != nil {
				return err
			}
			body := map[string]interface{}{
				"name":   name,
				"kind":   strings.ToUpper(kind),
				"source": map[string]interface{}{"kind": "UPLOAD"},
			}
			if port > 0 {
				body["settings"] = map[string]int{"port": port}
			}
			c, _ := a.Client()
			var out struct {
				Service api.Service `json:"service"`
			}
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/services", nil, body, &out); err != nil {
				return err
			}
			s := out.Service
			if err := config.SaveLink(abs, config.Link{ServiceID: s.ID, ServiceName: s.Name, ProjectID: p.ID}); err != nil {
				return err
			}
			if a.JSON {
				return a.print(s, nil)
			}
			a.printf("Service %s created in project %s, and this folder linked to it.\n", s.Name, p.Name)
			a.printf("Next: `mw deploy` sends this folder and deploys it.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "the project (default: the folder's name; made if missing)")
	cmd.Flags().StringVar(&location, "location", "", "where a project made now runs, for good: a location's id, name or code (`mw regions`)")
	cmd.Flags().StringVar(&name, "name", "", "the service's name (default: the folder's name)")
	cmd.Flags().StringVar(&kind, "kind", "web", "web, private, worker or cron")
	cmd.Flags().IntVar(&port, "port", 0, "the port the app listens on, as $PORT tells it (default 8080)")
	cmd.Flags().BoolVar(&force, "force", false, "make a new service even if this folder is linked to one")
	return cmd
}

// findOrCreateProject is the project by name, made when there is none — in
// the location named, or the platform's default when none is.
func (a *App) findOrCreateProject(ctx context.Context, name, location string) (*api.Project, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	all, err := api.All[api.Project](ctx, c, "/serverless/projects", nil)
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.Name == name || p.ID == name {
			p := p
			return &p, nil
		}
	}
	body := map[string]interface{}{"name": name}
	if location != "" {
		id, err := a.resolveLocation(ctx, appLocations, "projects", location)
		if err != nil {
			return nil, err
		}
		body["datacenter_id"] = id
	}
	var p api.Project
	if err := c.Do(ctx, "POST", "/serverless/projects", nil, body, &p); err != nil {
		return nil, err
	}
	a.printf("Project %s created in %s.\n", p.Name, locationText(a.locationNames(ctx, p.DatacenterID), p.DatacenterID))
	return &p, nil
}
