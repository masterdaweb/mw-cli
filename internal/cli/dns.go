package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

var dnsTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA"}

// dnsCmd: zones hosted on the platform's nameservers, and their records.
func dnsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "DNS zones and records on the platform's nameservers",
		Example: "  mw dns zones create loja.com.br\n" +
			"  mw dns records set loja.com.br www A 203.0.113.10\n" +
			"  mw dns records add loja.com.br @ MX \"10 mail.loja.com.br.\"\n" +
			"  mw dns records loja.com.br",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.listZones(cmd) },
	}
	cmd.AddCommand(dnsZonesCmd(a), dnsRecordsCmd(a))
	return cmd
}

func (a *App) listZones(cmd *cobra.Command) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	all, err := api.All[api.DNSZone](cmd.Context(), c, "/dns/zones", nil)
	if err != nil {
		return err
	}
	l := listing{headers: []string{"ID", "ZONE", "CREATED"}, empty: "No zones yet: `mw dns zones create <domain>`."}
	for _, z := range all {
		l.add(z.ID, z.ID, z.Name, whenPtr(z.CreatedAt))
	}
	return a.printList(all, l)
}

func dnsZonesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "zones",
		Aliases: []string{"zone", "domains"},
		Short:   "DNS zones",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return a.listZones(cmd) },
	}
	create := &cobra.Command{
		Use:   "create <domain>",
		Short: "Host a zone (then point the domain's nameservers here)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var z api.DNSZone
			if err := c.Do(cmd.Context(), "POST", "/dns/zones", nil, map[string]string{"name": strings.TrimSuffix(strings.ToLower(args[0]), ".")}, &z); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(z)
			}
			fmt.Fprintf(a.Out, "Zone %s hosted (%s). Point the domain's nameservers here at your registrar; `mw dns zones health %s` checks it.\n", z.Name, z.ID, z.Name)
			return nil
		},
	}
	get := &cobra.Command{
		Use:   "get <zone>",
		Short: "A zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var d detail
			d.add("ID", z.ID)
			d.add("Zone", z.Name)
			d.add("Created", whenPtr(z.CreatedAt))
			return a.printDetail(z, d)
		},
	}
	var yes bool
	remove := &cobra.Command{
		Use:     "delete <zone>",
		Aliases: []string{"remove", "rm"},
		Short:   "Stop hosting a zone: its records stop answering",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				q := fmt.Sprintf("Stop hosting %s? Every record in it stops answering.\nType the zone (%s) to confirm: ", z.Name, z.Name)
				if err := a.typeToConfirm(q, z.Name, "--yes"); err != nil {
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/dns/zones/"+z.ID, nil, nil, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": z.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Zone %s removed.\n", z.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	health := &cobra.Command{
		Use:   "health <zone>",
		Short: "Whether the domain's registrar points at our nameservers (looked up now)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var h api.DNSHealth
			if err := c.Do(cmd.Context(), "GET", "/dns/zones/"+z.ID+"/health", nil, nil, &h); err != nil {
				return notOffered(err, "Zone health from the API")
			}
			// Delegated first: `-q` prints yes or no.
			var d detail
			d.add("Delegated", yesNo(h.Delegated))
			d.add("Zone", z.Name)
			d.add("Nameservers now", orDash(strings.Join(h.Nameservers, ", ")))
			d.add("Expected", orDash(strings.Join(h.ExpectedNameservers, ", ")))
			if !h.Delegated && len(h.ExpectedNameservers) > 0 {
				d.after = append(d.after, fmt.Sprintf("Set the nameservers of %s to %s at your registrar; the change can take up to 48 hours to spread.",
					z.Name, strings.Join(h.ExpectedNameservers, " and ")))
			}
			return a.printDetail(h, d)
		},
	}
	cmd.AddCommand(create, get, remove, health)
	return cmd
}

func dnsRecordsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "records <zone>",
		Aliases: []string{"record"},
		Short:   "A zone's records",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			recs, err := a.records(cmd.Context(), z)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"NAME", "TYPE", "TTL", "VALUE"}, empty: "No records."}
			for _, r := range recs {
				for i, v := range r.Values {
					name, typ, ttl := r.Name, r.Type, fmt.Sprint(r.TTL)
					if i > 0 {
						name, typ, ttl = "", "", ""
					}
					l.add(r.Name+" "+r.Type, name, typ, ttl, v)
				}
				for _, v := range r.DisabledValues {
					l.add(r.Name+" "+r.Type, r.Name, r.Type, fmt.Sprint(r.TTL), v+"  (disabled)")
				}
			}
			return a.printList(recs, l)
		},
	}

	var ttl int
	var comment string
	set := &cobra.Command{
		Use:   "set <zone> <name> <type> <value>...",
		Short: "Set a record: its values become exactly these",
		Long: "Set a record set: the values at <name> and <type> become exactly the ones given (the old\n" +
			"ones are replaced). <name> is relative to the zone (www, @ for the zone itself) or\n" +
			"fully qualified. Types: " + strings.Join(dnsTypes, ", ") + ".",
		Example: "  mw dns records set loja.com.br www A 203.0.113.10\n" +
			"  mw dns records set loja.com.br @ A 203.0.113.10 203.0.113.11 --ttl 300\n" +
			"  mw dns records set loja.com.br @ TXT \"v=spf1 include:_spf.loja.com.br ~all\"",
		Args: cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			typ, err := dnsType(args[2])
			if err != nil {
				return err
			}
			if err := a.setRecord(cmd.Context(), z, args[1], typ, args[3:], ttl, comment); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"zone": z.Name, "name": fqdn(args[1], z.Name), "type": typ, "values": args[3:]})
			}
			fmt.Fprintf(a.Out, "%s %s → %s\n", fqdn(args[1], z.Name), typ, strings.Join(args[3:], ", "))
			return nil
		},
	}
	set.Flags().IntVar(&ttl, "ttl", 0, "seconds it may be cached (default: the zone's)")
	set.Flags().StringVar(&comment, "comment", "", "a note")

	var addTTL int
	add := &cobra.Command{
		Use:     "add <zone> <name> <type> <value>",
		Short:   "Add one value to a record set (keeping the ones it has)",
		Example: "  mw dns records add loja.com.br @ MX \"20 mail2.loja.com.br.\"",
		Args:    cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.resolveZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			typ, err := dnsType(args[2])
			if err != nil {
				return err
			}
			cur, err := a.recordSet(cmd.Context(), z, args[1], typ)
			if err != nil {
				return err
			}
			values := []string{args[3]}
			t := addTTL
			if cur != nil {
				for _, v := range cur.Values {
					if v == args[3] {
						return fmt.Errorf("%s %s already has %s", fqdn(args[1], z.Name), typ, args[3])
					}
				}
				values = append(append([]string{}, cur.Values...), args[3])
				if t == 0 {
					t = cur.TTL
				}
			}
			if err := a.setRecord(cmd.Context(), z, args[1], typ, values, t, ""); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"zone": z.Name, "name": fqdn(args[1], z.Name), "type": typ, "values": values})
			}
			fmt.Fprintf(a.Out, "%s %s → %s\n", fqdn(args[1], z.Name), typ, strings.Join(values, ", "))
			return nil
		},
	}
	add.Flags().IntVar(&addTTL, "ttl", 0, "seconds it may be cached (default: the record set's)")

	var value string
	var yes bool
	remove := &cobra.Command{
		Use:     "delete <zone> <name> <type>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a record set, or one value of it (--value)",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			z, err := a.resolveZone(ctx, args[0])
			if err != nil {
				return err
			}
			typ, err := dnsType(args[2])
			if err != nil {
				return err
			}
			name := fqdn(args[1], z.Name)
			if !yes {
				what := name + " " + typ
				if value != "" {
					what += " " + value
				}
				ok, err := a.confirm(fmt.Sprintf("Delete %s? [y/N] ", what), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if value != "" {
				cur, err := a.recordSet(ctx, z, args[1], typ)
				if err != nil {
					return err
				}
				if cur == nil {
					return fmt.Errorf("%s has no %s record", name, typ)
				}
				var keep []string
				for _, v := range cur.Values {
					if v != value {
						keep = append(keep, v)
					}
				}
				if len(keep) == len(cur.Values) {
					return fmt.Errorf("%s %s has no value %s", name, typ, value)
				}
				if len(keep) > 0 {
					if err := a.setRecord(ctx, z, args[1], typ, keep, cur.TTL, ""); err != nil {
						return err
					}
					if a.JSON {
						return a.encode(map[string]interface{}{"zone": z.Name, "name": name, "type": typ, "values": keep})
					}
					fmt.Fprintf(a.Out, "%s %s → %s\n", name, typ, strings.Join(keep, ", "))
					return nil
				}
			}
			if err := c.Do(ctx, "DELETE", "/dns/zones/"+z.ID+"/records", nil, map[string]string{"name": args[1], "type": typ}, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"zone": z.Name, "name": name, "type": typ, "deleted": true})
			}
			fmt.Fprintf(a.Out, "%s %s deleted.\n", name, typ)
			return nil
		},
	}
	remove.Flags().StringVar(&value, "value", "", "delete only this value of the record set")
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")

	cmd.AddCommand(set, add, remove)
	return cmd
}

// setRecord makes a record set exactly these values: one value through the
// replace endpoint every API has, several through the newer one.
func (a *App) setRecord(ctx context.Context, z *api.DNSZone, name, typ string, values []string, ttl int, comment string) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	if len(values) == 1 {
		body := map[string]interface{}{"name": name, "type": typ, "content": values[0]}
		if ttl > 0 {
			body["ttl"] = ttl
		}
		if comment != "" {
			body["comment"] = comment
		}
		return c.Do(ctx, "POST", "/dns/zones/"+z.ID+"/records", nil, body, nil)
	}
	body := map[string]interface{}{"name": name, "type": typ, "records": values}
	if ttl > 0 {
		body["ttl"] = ttl
	}
	if comment != "" {
		body["comment"] = comment
	}
	if err := c.Do(ctx, "PUT", "/dns/zones/"+z.ID+"/records", nil, body, nil); err != nil {
		return notOffered(err, "A record set of several values")
	}
	return nil
}

func (a *App) records(ctx context.Context, z *api.DNSZone) ([]api.DNSRecord, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	return api.All[api.DNSRecord](ctx, c, "/dns/zones/"+z.ID+"/records", nil)
}

// recordSet is the record set at name/type, or nil.
func (a *App) recordSet(ctx context.Context, z *api.DNSZone, name, typ string) (*api.DNSRecord, error) {
	recs, err := a.records(ctx, z)
	if err != nil {
		return nil, err
	}
	want := fqdn(name, z.Name)
	for _, r := range recs {
		if strings.EqualFold(r.Name, want) && strings.EqualFold(r.Type, typ) {
			r := r
			return &r, nil
		}
	}
	return nil, nil
}

func (a *App) resolveZone(ctx context.Context, ref string) (*api.DNSZone, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	all, err := api.All[api.DNSZone](ctx, c, "/dns/zones", nil)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSuffix(strings.ToLower(ref), ".")
	for _, z := range all {
		if z.ID == ref || strings.EqualFold(z.Name, want) {
			z := z
			return &z, nil
		}
	}
	return nil, fmt.Errorf("no zone %s (`mw dns zones` lists them)", ref)
}

// fqdn is a record name as the API lists it: www → www.example.com.
func fqdn(name, zone string) string {
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "@" || n == "" || n == zone || n == zone+".":
		return zone + "."
	case strings.HasSuffix(n, "."):
		return n
	case strings.HasSuffix(n, "."+zone):
		return n + "."
	default:
		return n + "." + zone + "."
	}
}

func dnsType(s string) (string, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	for _, known := range dnsTypes {
		if t == known {
			return t, nil
		}
	}
	return "", errors.New("the record type is one of " + strings.Join(dnsTypes, ", "))
}
