package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func envCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:     "env",
		Aliases: []string{"variables", "vars"},
		Short:   "A service's variables: list, set, unset, import a .env, pull one",
		Long: "Variables reach the service with its next deployment; set, unset and import start one\n" +
			"unless --no-deploy. A secret is write-only: once saved it is never shown again, here or anywhere.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			vars, err := a.variables(cmd, s)
			if err != nil {
				return err
			}
			return a.print(vars, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "KEY\tVALUE\tBUILD")
				for _, v := range vars {
					value := str(v.Value)
					if v.IsSecret {
						value = "(secret)"
					}
					build := ""
					if v.AvailableAtBuild {
						build = "yes"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", v.Key, value, build)
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.AddCommand(envSetCmd(a), envUnsetCmd(a), envImportCmd(a), envPullCmd(a))
	return cmd
}

func (a *App) variables(cmd *cobra.Command, s *api.Service) ([]api.Variable, error) {
	c, _ := a.Client()
	var vars []api.Variable
	err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/variables", nil, nil, &vars)
	return vars, err
}

// printChange says what a variables call did, and the deployment applying it.
func (a *App) printChange(s *api.Service, ch api.VariableChange) error {
	return a.print(ch, func(w *tabwriter.Writer) {
		parts := []string{}
		for _, p := range []struct {
			n    int
			verb string
		}{{len(ch.Created), "added"}, {len(ch.Updated), "changed"}, {len(ch.Removed), "removed"}} {
			if p.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", p.n, p.verb))
			}
		}
		if len(parts) == 0 {
			fmt.Fprintf(w, "Nothing changed on %s.\n", s.Name)
			return
		}
		fmt.Fprintf(w, "%s: %s.\n", s.Name, strings.Join(parts, ", "))
		if d := ch.Deployment; d != nil {
			fmt.Fprintf(w, "Applying with deployment #%d (`mw deployments` follows it).\n", d.Number)
		} else {
			fmt.Fprintln(w, "Saved: applied with the next deployment.")
		}
	})
}

func envSetCmd(a *App) *cobra.Command {
	var service, project string
	var secret, build, noDeploy bool
	cmd := &cobra.Command{
		Use:   "set KEY=value [KEY=value...]",
		Short: "Create or change variables (all or nothing)",
		Example: "  mw env set APP_ENV=production LOG_LEVEL=info\n" +
			"  mw env set --secret STRIPE_KEY=sk_live_…\n" +
			"  mw env set --build NEXT_PUBLIC_API_URL=https://api.loja.com.br",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pairs, err := parseAssignments(args)
			if err != nil {
				return err
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			var list []map[string]interface{}
			for _, p := range pairs {
				v := map[string]interface{}{"key": p[0], "value": p[1]}
				if cmd.Flags().Changed("secret") {
					v["is_secret"] = secret
				}
				if cmd.Flags().Changed("build") {
					v["available_at_build"] = build
				}
				list = append(list, v)
			}
			c, _ := a.Client()
			var ch api.VariableChange
			if err := c.Do(cmd.Context(), "PUT", "/serverless/services/"+s.ID+"/variables", nil,
				map[string]interface{}{"variables": list, "deploy": !noDeploy}, &ch); err != nil {
				return err
			}
			return a.printChange(s, ch)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&secret, "secret", false, "write-only: never shown again once saved")
	cmd.Flags().BoolVar(&build, "build", false, "also given to the build (e.g. NEXT_PUBLIC_*)")
	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "save only: applied with the next deployment")
	return cmd
}

func envUnsetCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "unset KEY [KEY...]",
		Short: "Remove variables (applied with a new deployment)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var ch api.VariableChange
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/variables/delete", nil,
				map[string]interface{}{"keys": args}, &ch); err != nil {
				return err
			}
			return a.printChange(s, ch)
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

func envImportCmd(a *App) *cobra.Command {
	var service, project string
	var secrets []string
	var allSecret, replace, noDeploy bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Set the variables of a .env file (- reads standard input)",
		Long: "Set the variables of a .env file, all or nothing: a line the API cannot read is\n" +
			"reported with its number and nothing is saved. --replace also removes the variables\n" +
			"the file does not have.",
		Example: "  mw env import .env --secret DB_PASSWORD --secret APP_KEY\n" +
			"  heroku config -s | mw env import - --all-secret",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw []byte
			var err error
			if args[0] == "-" {
				raw, err = io.ReadAll(io.LimitReader(a.In, 1<<20))
			} else {
				raw, err = os.ReadFile(args[0])
			}
			if err != nil {
				return err
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			body := map[string]interface{}{
				"content":     string(raw),
				"all_secret":  allSecret,
				"replace_all": replace,
				"deploy":      !noDeploy,
			}
			if len(secrets) > 0 {
				body["secret_keys"] = secrets
			}
			c, _ := a.Client()
			var ch api.VariableChange
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/variables/import", nil, body, &ch); err != nil {
				return err
			}
			return a.printChange(s, ch)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "a key to save as a secret (repeat)")
	cmd.Flags().BoolVar(&allSecret, "all-secret", false, "save every variable as a secret")
	cmd.Flags().BoolVar(&replace, "replace", false, "remove the variables the file does not have")
	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "save only: applied with the next deployment")
	return cmd
}

func envPullCmd(a *App) *cobra.Command {
	var service, project string
	var force bool
	cmd := &cobra.Command{
		Use:   "pull [file]",
		Short: "Write the variables as a .env (standard output without a file); secrets are left out",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			vars, err := a.variables(cmd, s)
			if err != nil {
				return err
			}
			var b strings.Builder
			var secret []string
			for _, v := range vars {
				if v.IsSecret || v.Value == nil {
					secret = append(secret, v.Key)
					continue
				}
				fmt.Fprintf(&b, "%s=%s\n", v.Key, dotenvValue(*v.Value))
			}
			if len(secret) > 0 {
				fmt.Fprintf(&b, "# Secrets are never shown again once saved: %s\n", strings.Join(secret, ", "))
			}
			if len(args) == 0 {
				_, err := io.WriteString(a.Out, b.String())
				return err
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if force {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			f, err := os.OpenFile(args[0], flags, 0o600)
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%s exists: pass --force to replace it", args[0])
			}
			if err != nil {
				return err
			}
			if _, err := f.WriteString(b.String()); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(a.Err, "Wrote %d variable(s) of %s to %s.\n", len(vars)-len(secret), s.Name, args[0])
			return nil
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&force, "force", false, "replace the file if it exists")
	return cmd
}

var needsQuoting = regexp.MustCompile("[\\s#\"'\\\\$`]")

// dotenvValue writes a value as the API's own export does, so that
// `mw env pull | mw env import -` changes nothing.
func dotenvValue(v string) string {
	if v == "" || !needsQuoting.MatchString(v) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}
