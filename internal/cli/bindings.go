package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// bindingKind is the API's resource kind for a database engine: MySQL and
// MariaDB are one kind, with one set of variables.
func bindingKind(engine string) string {
	switch engine {
	case "postgres":
		return "POSTGRES"
	case "valkey":
		return "REDIS"
	case "mysql", "mariadb":
		return "MYSQL"
	case "sqlserver":
		return "SQLSERVER"
	}
	// An engine this CLI does not know: the API names it, and refuses it.
	return strings.ToUpper(engine)
}

func (a *App) bindings(ctx context.Context, s *api.Service) ([]api.Binding, error) {
	c, _ := a.Client()
	var all []api.Binding
	err := c.Do(ctx, "GET", "/serverless/services/"+s.ID+"/bindings", nil, nil, &all)
	return all, err
}

func bindingsCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "bindings",
		Short: "The databases and buckets a service is bound to, and the variables each gives it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			all, err := a.bindings(cmd.Context(), s)
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "RESOURCE\tKIND\tVARIABLES\tCREDENTIAL ISSUED")
				for _, b := range all {
					name := str(b.ResourceName)
					if name == "" {
						name = "(removed)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, strings.ToLower(b.ResourceKind), strings.Join(b.Variables, " "), whenPtr(b.CredentialRotatedAt))
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

// bindOptions are a binding's variable layout, and a bucket's access.
type bindOptions struct {
	service, project string
	layout, prefix   string
	readOnly         bool
	noDeploy         bool
}

func (o *bindOptions) flags(cmd *cobra.Command, layouts string, bucket bool) {
	serviceFlag(cmd, &o.service, &o.project)
	cmd.Flags().StringVar(&o.layout, "layout", "", "the variables it gives the service: "+layouts)
	cmd.Flags().StringVar(&o.prefix, "prefix", "", "the variables' prefix, for the prefixed layout (ends in _)")
	if bucket {
		cmd.Flags().BoolVar(&o.readOnly, "read-only", false, "the service may read the bucket, not write to it")
	}
	cmd.Flags().BoolVar(&o.noDeploy, "no-deploy", false, "save only: applied with the next deployment")
}

func (a *App) bind(cmd *cobra.Command, kind, resource string, o *bindOptions) error {
	s, err := a.resolveService(cmd.Context(), o.service, o.project)
	if err != nil {
		return err
	}
	body := map[string]interface{}{"resource_kind": kind, "resource": resource, "deploy": !o.noDeploy}
	if o.layout != "" {
		body["env_profile"] = o.layout
	}
	if o.prefix != "" {
		body["env_prefix"] = o.prefix
		if o.layout == "" {
			body["env_profile"] = "prefixed"
		}
	}
	if kind == "BUCKET" {
		body["access"] = "read_write"
		if o.readOnly {
			body["access"] = "read_only"
		}
	}
	c, _ := a.Client()
	var ch api.BindingChange
	if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/bindings", nil, body, &ch); err != nil {
		return err
	}
	return a.print(ch, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "%s is bound to %s, with a credential of its own.\n", resource, s.Name)
		if b := ch.Binding; b != nil {
			fmt.Fprintf(w, "Its variables: %s\n", strings.Join(b.Variables, " "))
		}
		if d := ch.Deployment; d != nil {
			fmt.Fprintf(w, "Applying with deployment #%d.\n", d.Number)
		} else {
			fmt.Fprintln(w, "Applied with the next deployment.")
		}
	})
}

func (a *App) unbind(cmd *cobra.Command, kind, resource, service, project string) error {
	s, err := a.resolveService(cmd.Context(), service, project)
	if err != nil {
		return err
	}
	all, err := a.bindings(cmd.Context(), s)
	if err != nil {
		return err
	}
	var found *api.Binding
	for i := range all {
		if all[i].ResourceKind == kind && str(all[i].ResourceName) == resource {
			found = &all[i]
		}
	}
	if found == nil {
		return fmt.Errorf("%s is not bound to %s", resource, s.Name)
	}
	c, _ := a.Client()
	var ch api.BindingChange
	if err := c.Do(cmd.Context(), "DELETE", "/serverless/services/"+s.ID+"/bindings/"+found.ID, nil, nil, &ch); err != nil {
		return err
	}
	return a.print(ch, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "%s is no longer bound to %s: its variables leave with the next deployment", resource, s.Name)
		if d := ch.Deployment; d != nil {
			fmt.Fprintf(w, " (#%d)", d.Number)
		}
		fmt.Fprintln(w, "; its credential stops working an hour later.")
	})
}

func dbBindCmd(a *App, engine string) *cobra.Command {
	o := &bindOptions{}
	cmd := &cobra.Command{
		Use:   "bind <name>",
		Short: "Give a service this database as variables, with a login of its own",
		Long: "Bind a database to a service: the service gets a login of its own and the variables\n" +
			"its code reads. Layouts: url (DATABASE_URL / REDIS_URL, the default), framework\n" +
			"(Laravel's DB_* / REDIS_*), libpq (PostgreSQL's PG*), prefixed (with --prefix) or\n" +
			"dotnet (ConnectionStrings__DefaultConnection, Redis for Redis; --prefix names it).\n" +
			"MySQL and MariaDB take url, framework, prefixed or dotnet. SQL Server takes url\n" +
			"(sqlserver://, Prisma's form), framework (Laravel's sqlsrv), prefixed (with a JDBC URL)\n" +
			"or dotnet (SqlClient).",
		Example: "  mw db bind loja-prod --service web --layout framework\n  mw redis bind filas --service worker",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			return a.bind(cmd, bindingKind(d.Engine), d.Name, o)
		},
	}
	layouts := "url, framework, libpq, prefixed or dotnet"
	if engine == "valkey" || engine == "mysql" || engine == "mariadb" {
		layouts = "url, framework, prefixed or dotnet"
	}
	o.flags(cmd, layouts, false)
	return cmd
}

func dbUnbindCmd(a *App, engine string) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "unbind <name>",
		Short: "Stop giving a service this database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			return a.unbind(cmd, bindingKind(d.Engine), d.Name, service, project)
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}
