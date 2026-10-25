package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func domainsCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "A service's domains: add your own, check its DNS records, remove it",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			all, err := a.domains(cmd.Context(), s)
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "HOSTNAME\tSTATUS\tSINCE")
				for _, d := range all {
					status := domainStatus(d.Status)
					if d.IsDefault {
						status += " (default)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", d.Hostname, status, whenPtr(d.ActivatedAt))
				}
			})
		},
	}
	serviceFlag(cmd, &service, &project)

	add := &cobra.Command{
		Use:     "add <hostname>",
		Short:   "Add a domain of yours, and print the DNS records to create",
		Example: "  mw domains add app.loja.com.br",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var d api.Domain
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/domains", nil,
				map[string]string{"hostname": strings.ToLower(strings.TrimSpace(args[0]))}, &d); err != nil {
				return err
			}
			return a.print(d, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s added to %s.\n", d.Hostname, s.Name)
				domainRecords(w, d)
				fmt.Fprintf(w, "Then `mw domains check %s`: the certificate is issued once they are found.\n", d.Hostname)
			})
		},
	}
	serviceFlag(add, &service, &project)

	check := &cobra.Command{
		Use:   "check <hostname>",
		Short: "Check a domain's DNS records now, and say what is missing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.findDomain(cmd.Context(), service, project, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Domain
			if err := c.Do(cmd.Context(), "POST", "/serverless/domains/"+d.ID+"/check", nil, nil, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s: %s\n", out.Hostname, domainStatus(out.Status))
				if e := out.Error; e != nil && e.Message != nil {
					fmt.Fprintf(w, "  %s\n", *e.Message)
				}
				if out.Status != "ACTIVE" {
					domainRecords(w, out)
				}
			})
		},
	}
	serviceFlag(check, &service, &project)

	remove := &cobra.Command{
		Use:     "remove <hostname>",
		Aliases: []string{"rm"},
		Short:   "Remove a domain: it stops serving the service at once",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.findDomain(cmd.Context(), service, project, args[0])
			if err != nil {
				return err
			}
			if d.IsDefault {
				return fmt.Errorf("%s is the service's own address: it cannot be removed", d.Hostname)
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/domains/"+d.ID, nil, nil, nil); err != nil {
				return err
			}
			a.printf("%s removed.\n", d.Hostname)
			return nil
		},
	}
	serviceFlag(remove, &service, &project)

	cmd.AddCommand(add, check, remove)
	return cmd
}

// domainStatus says a domain's state in words.
func domainStatus(status string) string {
	switch status {
	case "PENDING_DNS":
		return "waiting for DNS"
	case "PENDING_CERTIFICATE":
		return "issuing its certificate"
	}
	return strings.ToLower(status)
}

func (a *App) domains(ctx context.Context, s *api.Service) ([]api.Domain, error) {
	c, _ := a.Client()
	var all []api.Domain
	err := c.Do(ctx, "GET", "/serverless/services/"+s.ID+"/domains", nil, nil, &all)
	return all, err
}

func (a *App) findDomain(ctx context.Context, service, project, hostname string) (*api.Domain, error) {
	s, err := a.resolveService(ctx, service, project)
	if err != nil {
		return nil, err
	}
	all, err := a.domains(ctx, s)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	for i := range all {
		if all[i].Hostname == want {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%s has no domain %s", s.Name, want)
}

// domainRecords prints the records to create, and which the last check found.
// A root domain has alternatives (ALIAS, or A records to the redirect
// service): each is labelled.
func domainRecords(w *tabwriter.Writer, d api.Domain) {
	if len(d.Instructions) == 0 {
		return
	}
	fmt.Fprintln(w, "Create these records at your DNS provider:")
	fmt.Fprintln(w, "  TYPE\tNAME\tVALUE\t")
	for _, r := range d.Instructions {
		note := ""
		if r.Found != nil {
			if *r.Found {
				note = "found"
			} else {
				note = "not found yet"
			}
		}
		if r.Option != nil {
			switch *r.Option {
			case "alias":
				note = strings.TrimSpace("if your DNS has ALIAS/flattening " + note)
			case "redirect":
				note = strings.TrimSpace("otherwise, to redirect to www " + note)
			}
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", r.Type, r.Name, r.Value, note)
	}
}
