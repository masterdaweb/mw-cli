package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// Projects are universal: an instance, a bucket or a database can be put in
// one beside the serverless services. The API names them /v1/projects; an
// older one only has /v1/serverless/projects, which the CLI falls back to.

// projectsBase is "/projects" or, on an API without it, "/serverless/projects".
func (a *App) projectsBase(ctx context.Context) (string, error) {
	if a.projectsPath != "" {
		return a.projectsPath, nil
	}
	c, err := a.Client()
	if err != nil {
		return "", err
	}
	var p api.Page[api.Project]
	err = c.Do(ctx, "GET", "/projects", map[string][]string{"limit": {"1"}}, nil, &p)
	switch {
	case err == nil:
		a.projectsPath = "/projects"
	case routeMissing(err):
		a.projectsPath = "/serverless/projects"
	default:
		return "", err
	}
	return a.projectsPath, nil
}

func (a *App) projects(ctx context.Context) ([]api.Project, error) {
	base, err := a.projectsBase(ctx)
	if err != nil {
		return nil, err
	}
	c, _ := a.Client()
	return api.All[api.Project](ctx, c, base, nil)
}

var resourceKinds = map[string]string{
	"instance": "INSTANCE", "instances": "INSTANCE", "vps": "INSTANCE", "server": "INSTANCE",
	"bucket": "BUCKET", "buckets": "BUCKET", "storage": "BUCKET",
	"database": "DATABASE", "databases": "DATABASE", "db": "DATABASE", "redis": "DATABASE",
}

func resourceKind(s string) (string, error) {
	if k, ok := resourceKinds[strings.ToLower(strings.TrimSpace(s))]; ok {
		return k, nil
	}
	return "", errors.New("the kind is instance, bucket or database")
}

// resourceID is what the API keeps a resource under: an instance's number
// (by its id or name), a bucket's or database's name or id as given.
func (a *App) resourceID(ctx context.Context, kind, ref string) (string, error) {
	if kind == "INSTANCE" {
		in, err := a.resolveInstance(ctx, ref)
		if err != nil {
			return "", err
		}
		return in.ID, nil
	}
	return ref, nil
}

func projectResourcesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resources <project>",
		Aliases: []string{"resource"},
		Short:   "What a project holds besides its services: instances, buckets and databases",
		Example: "  mw projects resources loja\n" +
			"  mw projects resources add loja instance web-1 web-2\n" +
			"  mw projects resources add loja bucket loja-fotos\n" +
			"  mw projects resources remove loja instance web-2",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.resolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			list, err := a.projectResources(ctx, p.ID)
			if err != nil {
				return err
			}
			return a.printResources(p, list)
		},
	}

	add := &cobra.Command{
		Use:   "add <project> <kind> <resource>...",
		Short: "Put instances, buckets or databases in a project (moved from the one they were in)",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.resolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			kind, err := resourceKind(args[1])
			if err != nil {
				return err
			}
			var refs []map[string]string
			for _, ref := range args[2:] {
				id, err := a.resourceID(ctx, kind, ref)
				if err != nil {
					return err
				}
				refs = append(refs, map[string]string{"kind": kind, "id": id})
			}
			list, err := a.addProjectResources(ctx, p.ID, refs)
			if err != nil {
				return err
			}
			if !a.JSON {
				fmt.Fprintf(a.Out, "%d %s(s) put in project %s.\n", len(refs), strings.ToLower(kind), p.Name)
			}
			return a.printResources(p, list)
		},
	}

	remove := &cobra.Command{
		Use:     "remove <project> <kind> <resource>",
		Aliases: []string{"rm"},
		Short:   "Take a resource out of a project (the resource itself stays as it is)",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.resolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			kind, err := resourceKind(args[1])
			if err != nil {
				return err
			}
			id, err := a.resourceID(ctx, kind, args[2])
			if err != nil {
				return err
			}
			if err := a.removeProjectResource(ctx, p.ID, kind, id); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"project_id": p.ID, "kind": kind, "id": id, "removed": true})
			}
			fmt.Fprintf(a.Out, "%s %s taken out of project %s (it stays as it was).\n", strings.ToLower(kind), args[2], p.Name)
			return nil
		},
	}
	cmd.AddCommand(add, remove)
	return cmd
}

func (a *App) printResources(p *api.Project, list []api.ProjectResource) error {
	l := listing{headers: []string{"KIND", "ID", "NAME", "STATUS", "DETAIL"}, empty: fmt.Sprintf("Project %s holds no instances, buckets or databases.", p.Name)}
	for _, r := range list {
		detail := orDashPtr(r.Detail)
		if r.Image != nil && *r.Image != "" {
			detail = strings.TrimSpace(detail + " · " + *r.Image)
		}
		l.add(r.ID, strings.ToLower(r.Kind), r.ID, r.Name, strings.ToLower(r.Status), detail)
	}
	return a.printList(list, l)
}

// projectResources, addProjectResources and removeProjectResource use
// /v1/projects/{id}/resources, or the older /v1/serverless/… path.
func (a *App) projectResources(ctx context.Context, id string) ([]api.ProjectResource, error) {
	var out []api.ProjectResource
	err := a.onProject(ctx, id, func(c *api.Client, base string) error {
		return c.Do(ctx, "GET", base+"/"+id+"/resources", nil, nil, &out)
	})
	return out, err
}

func (a *App) addProjectResources(ctx context.Context, id string, refs []map[string]string) ([]api.ProjectResource, error) {
	var out []api.ProjectResource
	err := a.onProject(ctx, id, func(c *api.Client, base string) error {
		return c.Do(ctx, "POST", base+"/"+id+"/resources", nil, map[string]interface{}{"resources": refs}, &out)
	})
	return out, err
}

func (a *App) removeProjectResource(ctx context.Context, id, kind, resourceID string) error {
	return a.onProject(ctx, id, func(c *api.Client, base string) error {
		return c.Do(ctx, "DELETE", base+"/"+id+"/resources/"+strings.ToLower(kind)+"/"+resourceID, nil, nil, nil)
	})
}

// onProject runs fn on the new path, again on the old one if the API has
// only that.
func (a *App) onProject(ctx context.Context, id string, fn func(c *api.Client, base string) error) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	base := a.projectsPath
	if base == "" {
		base = "/projects"
	}
	err = fn(c, base)
	if err != nil && routeMissing(err) && base == "/projects" {
		a.projectsPath = "/serverless/projects"
		err = fn(c, a.projectsPath)
	}
	return notOffered(err, "Putting instances, buckets and databases in projects")
}

// putInProject puts a resource just made in a project (bucket create
// --project): the resource is made either way; a failure here is reported.
func (a *App) putInProject(ctx context.Context, project, kind, id string) error {
	p, err := a.resolveProject(ctx, project)
	if err != nil {
		return err
	}
	if _, err := a.addProjectResources(ctx, p.ID, []map[string]string{{"kind": kind, "id": id}}); err != nil {
		return fmt.Errorf("made, but not put in project %s: %s", p.Name, Describe(err))
	}
	if !a.JSON {
		fmt.Fprintf(a.Out, "Put in project %s.\n", p.Name)
	}
	return nil
}
