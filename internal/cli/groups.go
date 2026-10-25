package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// groupsCmd: a project's variable groups — variables several services read
// (a template keeps its generated secrets in one).
func groupsCmd(a *App) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:     "groups",
		Aliases: []string{"group"},
		Short:   "Variable groups: variables a project's services share",
		Long: "A group's variables reach every service that reads it; a service's own variables win over\n" +
			"them, and later groups over earlier ones. Changing a group redeploys the live services\n" +
			"reading it, unless --no-deploy.",
		Example: "  mw groups --project loja\n" +
			"  mw groups create shared --project loja\n" +
			"  mw groups set shared MAIL_HOST=smtp.loja.com.br --project loja\n" +
			"  mw groups use shared --service web --project loja",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, groups, err := a.groups(cmd.Context(), project)
			if err != nil {
				return err
			}
			return a.print(groups, func(w *tabwriter.Writer) {
				if len(groups) == 0 {
					fmt.Fprintf(w, "No variable groups in %s: `mw groups create <name> --project %s`.\n", p.Name, p.Name)
					return
				}
				fmt.Fprintln(w, "GROUP\tVARIABLES\tSERVICES")
				for _, g := range groups {
					fmt.Fprintf(w, "%s\t%d\t%d\n", g.Name, g.VariableCount, len(g.ServiceIDs))
				}
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "the project (name or id)")

	var createProject string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a variable group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.resolveProject(cmd.Context(), createProject)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var g api.EnvGroup
			if err := c.Do(cmd.Context(), "POST", "/serverless/projects/"+p.ID+"/env-groups", nil, map[string]string{"name": args[0]}, &g); err != nil {
				return err
			}
			return a.print(g, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Group %s created in %s. Next: `mw groups set %s KEY=value --project %s`.\n", g.Name, p.Name, g.Name, p.Name)
			})
		},
	}
	create.Flags().StringVar(&createProject, "project", "", "the project (name or id)")

	var deleteProject string
	del := &cobra.Command{
		Use:   "delete <group>",
		Short: "Delete a variable group no service reads",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.group(cmd.Context(), deleteProject, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/env-groups/"+g.ID, nil, nil, nil); err != nil {
				return err
			}
			a.printf("Group %s deleted.\n", g.Name)
			return nil
		},
	}
	del.Flags().StringVar(&deleteProject, "project", "", "the project (name or id)")

	var envProject string
	env := &cobra.Command{
		Use:   "env <group>",
		Short: "A group's variables (secrets are never shown)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.group(cmd.Context(), envProject, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var vars []api.Variable
			if err := c.Do(cmd.Context(), "GET", "/serverless/env-groups/"+g.ID+"/variables", nil, nil, &vars); err != nil {
				return err
			}
			return a.print(vars, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "KEY\tVALUE")
				for _, v := range vars {
					value := str(v.Value)
					if v.IsSecret {
						value = "(secret)"
					}
					fmt.Fprintf(w, "%s\t%s\n", v.Key, value)
				}
			})
		},
	}
	env.Flags().StringVar(&envProject, "project", "", "the project (name or id)")

	var setProject string
	var secret, noDeploy bool
	set := &cobra.Command{
		Use:   "set <group> KEY=value [KEY=value...]",
		Short: "Create or change a group's variables (all or nothing)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pairs, err := parseAssignments(args[1:])
			if err != nil {
				return err
			}
			g, err := a.group(cmd.Context(), setProject, args[0])
			if err != nil {
				return err
			}
			var list []map[string]interface{}
			for _, p := range pairs {
				v := map[string]interface{}{"key": p[0], "value": p[1]}
				if cmd.Flags().Changed("secret") {
					v["is_secret"] = secret
				}
				list = append(list, v)
			}
			c, _ := a.Client()
			var ch struct {
				Created     []string         `json:"created"`
				Updated     []string         `json:"updated"`
				Removed     []string         `json:"removed"`
				Deployments []api.Deployment `json:"deployments"`
			}
			if err := c.Do(cmd.Context(), "PUT", "/serverless/env-groups/"+g.ID+"/variables", nil,
				map[string]interface{}{"variables": list, "deploy": !noDeploy}, &ch); err != nil {
				return err
			}
			return a.print(ch, func(w *tabwriter.Writer) {
				n := len(ch.Created) + len(ch.Updated) + len(ch.Removed)
				if n == 0 {
					fmt.Fprintf(w, "Nothing changed in %s.\n", g.Name)
					return
				}
				fmt.Fprintf(w, "%s: %d added, %d changed.\n", g.Name, len(ch.Created), len(ch.Updated))
				if len(ch.Deployments) > 0 {
					fmt.Fprintf(w, "Applying to %s reading it.\n", plural(len(ch.Deployments), "service", "services"))
				} else {
					fmt.Fprintln(w, "Saved: applied with each service's next deployment.")
				}
			})
		},
	}
	set.Flags().StringVar(&setProject, "project", "", "the project (name or id)")
	set.Flags().BoolVar(&secret, "secret", false, "write-only: never shown again once saved")
	set.Flags().BoolVar(&noDeploy, "no-deploy", false, "save only: applied with each service's next deployment")

	var useService, useProject string
	var useNoDeploy bool
	use := &cobra.Command{
		Use:   "use <group>[,<group>...]",
		Short: "Choose the groups a service reads, in order (later ones win); \"none\" for none",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), useService, useProject)
			if err != nil {
				return err
			}
			ids := []string{}
			if args[0] != "none" {
				for _, name := range strings.Split(args[0], ",") {
					g, err := a.group(cmd.Context(), s.ProjectID, strings.TrimSpace(name))
					if err != nil {
						return err
					}
					ids = append(ids, g.ID)
				}
			}
			c, _ := a.Client()
			var out struct {
				Deployment *api.Deployment `json:"deployment"`
			}
			if err := c.Do(cmd.Context(), "PUT", "/serverless/services/"+s.ID+"/env-groups", nil,
				map[string]interface{}{"env_group_ids": ids, "deploy": !useNoDeploy}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s reads %s.\n", s.Name, plural(len(ids), "group", "groups"))
				if out.Deployment != nil {
					fmt.Fprintf(w, "Applying with deployment #%d.\n", out.Deployment.Number)
				}
			})
		},
	}
	serviceFlag(use, &useService, &useProject)
	use.Flags().BoolVar(&useNoDeploy, "no-deploy", false, "save only: applied with the next deployment")

	cmd.AddCommand(create, del, env, set, use)
	return cmd
}

// groups is a project's variable groups.
func (a *App) groups(ctx context.Context, project string) (*api.Project, []api.EnvGroup, error) {
	if project == "" {
		return nil, nil, errors.New("which project? pass --project")
	}
	p, err := a.resolveProject(ctx, project)
	if err != nil {
		return nil, nil, err
	}
	c, _ := a.Client()
	var groups []api.EnvGroup
	if err := c.Do(ctx, "GET", "/serverless/projects/"+p.ID+"/env-groups", nil, nil, &groups); err != nil {
		return nil, nil, err
	}
	return p, groups, nil
}

// group is a project's variable group, by name or id.
func (a *App) group(ctx context.Context, project, ref string) (*api.EnvGroup, error) {
	p, groups, err := a.groups(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Name == ref || g.ID == ref {
			g := g
			return &g, nil
		}
	}
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s has no variable groups", p.Name)
	}
	return nil, fmt.Errorf("no group %q in %s (there is %s)", ref, p.Name, strings.Join(names, ", "))
}

// registryCmd: credentials image services pull private images with.
func registryCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "registry",
		Aliases: []string{"registries"},
		Short:   "Credentials for private image registries (Docker Hub, GHCR…)",
		Example: "  mw registry\n" +
			"  echo \"$GHCR_TOKEN\" | mw registry add ghcr.io --username ana --password-stdin\n" +
			"  mw services create api --project loja --image ghcr.io/ana/api:1.4 --registry ghcr.io\n" +
			"  mw registry remove ghcr.io",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := a.registryCredentials(cmd.Context())
			if err != nil {
				return err
			}
			return a.print(list, func(w *tabwriter.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No registry credentials: `mw registry add <host> --username … --password-stdin`.")
					return
				}
				fmt.Fprintln(w, "NAME\tREGISTRY\tUSERNAME\tLAST USED")
				for _, r := range list {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.RegistryHost, r.Username, dateOr(r.LastUsedAt, "never"))
				}
			})
		},
	}

	var name, username string
	var fromStdin bool
	add := &cobra.Command{
		Use:   "add <registry host>",
		Short: "Save a credential (the password or token is write-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if username == "" {
				return errors.New("pass --username")
			}
			if !fromStdin {
				return errors.New("pass the password or token on standard input with --password-stdin (never as an argument: it would stay in your shell history)")
			}
			line, err := bufio.NewReader(a.In).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			password := strings.TrimRight(line, "\r\n")
			if password == "" {
				return errors.New("no password on standard input")
			}
			if name == "" {
				name = args[0]
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var r api.RegistryCredential
			body := map[string]string{"name": name, "registry_host": args[0], "username": username, "password": password}
			if err := c.Do(cmd.Context(), "POST", "/serverless/registry-credentials", nil, body, &r); err != nil {
				return err
			}
			return a.print(r, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Credential %s saved for %s. Image services use it with --registry %s.\n", r.Name, r.RegistryHost, r.Name)
			})
		},
	}
	add.Flags().StringVar(&name, "name", "", "a name for it (default: the registry host)")
	add.Flags().StringVar(&username, "username", "", "the registry user")
	add.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password or token from standard input")

	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a credential no service uses",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.registryCredential(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/registry-credentials/"+url.PathEscape(r.ID), nil, nil, nil); err != nil {
				return err
			}
			a.printf("Credential %s deleted.\n", r.Name)
			return nil
		},
	}
	cmd.AddCommand(add, remove)
	return cmd
}

func (a *App) registryCredentials(ctx context.Context) ([]api.RegistryCredential, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var list []api.RegistryCredential
	err = c.Do(ctx, "GET", "/serverless/registry-credentials", nil, nil, &list)
	return list, err
}

// registryCredential finds one by name, id or registry host (when one only has it).
func (a *App) registryCredential(ctx context.Context, ref string) (*api.RegistryCredential, error) {
	list, err := a.registryCredentials(ctx)
	if err != nil {
		return nil, err
	}
	var byHost []api.RegistryCredential
	for _, r := range list {
		if r.Name == ref || r.ID == ref {
			r := r
			return &r, nil
		}
		if r.RegistryHost == ref {
			byHost = append(byHost, r)
		}
	}
	if len(byHost) == 1 {
		return &byHost[0], nil
	}
	if len(byHost) > 1 {
		return nil, fmt.Errorf("more than one credential for %s: name one", ref)
	}
	return nil, fmt.Errorf("no registry credential %q: `mw registry` lists them", ref)
}

func dateOr(iso *string, none string) string {
	if iso == nil || *iso == "" {
		return none
	}
	if len(*iso) >= 10 {
		return (*iso)[:10]
	}
	return *iso
}
