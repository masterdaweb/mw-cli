package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// vpcsCmd: private networks between instances.
func vpcsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vpcs",
		Aliases: []string{"vpc", "networks"},
		Short:   "VPCs: private networks between your instances",
		Example: "  mw vpcs\n" +
			"  mw vpcs create backend --subnet 10.20.0.0/24 --region br-bh\n" +
			"  mw vpcs attach backend web-1 --restart --wait",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.VPC](cmd.Context(), c, "/vpcs", nil)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "SUBNET", "DEFAULT", "INSTANCES"}, empty: "No VPCs yet: `mw vpcs create <name>`."}
			for _, v := range all {
				l.add(v.ID, v.ID, v.Name, orDashPtr(v.Subnet), yesNo(v.Default), orDash(strings.Join(v.InstanceIDs, ",")))
			}
			return a.printList(all, l)
		},
	}

	get := &cobra.Command{
		Use:   "get <vpc>",
		Short: "A VPC: its subnet and the instances in it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.resolveVPC(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printDetail(v, vpcDetail(*v, a.instanceNames(cmd.Context()), a.regionLabel(cmd.Context(), v.RegionID)))
		},
	}

	var description, subnet, region string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a VPC: a private network for instances in one region",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			r, err := a.resolveRegion(cmd.Context(), region)
			if err != nil {
				return err
			}
			body := map[string]interface{}{"name": args[0], "region": r.ID}
			if n, err := strconv.Atoi(r.ID); err == nil {
				body["region"] = n
			}
			if description != "" {
				body["description"] = description
			}
			if subnet != "" {
				body["subnet"] = subnet
			}
			var v api.VPC
			if err := c.Do(cmd.Context(), "POST", "/vpcs", nil, body, &v); err != nil {
				return notOffered(err, "Creating VPCs from the API")
			}
			return a.printDetail(v, vpcDetail(v, nil, ""))
		},
	}
	create.Flags().StringVar(&description, "description", "", "a note")
	create.Flags().StringVar(&subnet, "subnet", "", "CIDR inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16 (default 10.200.0.0/24)")
	create.Flags().StringVar(&region, "region", "", "region id, name or code (default: the only one)")

	var name, desc string
	update := &cobra.Command{
		Use:   "update <vpc>",
		Short: "Rename a VPC or change its description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.resolveVPC(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("description") {
				body["description"] = desc
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --name or --description")
			}
			c, _ := a.Client()
			var out api.VPC
			if err := c.Do(cmd.Context(), "PATCH", "/vpcs/"+v.ID, nil, body, &out); err != nil {
				return notOffered(err, "Changing VPCs from the API")
			}
			return a.printDetail(out, vpcDetail(out, nil, ""))
		},
	}
	update.Flags().StringVar(&name, "name", "", "new name")
	update.Flags().StringVar(&desc, "description", "", "a note")

	var yes bool
	remove := &cobra.Command{
		Use:     "delete <vpc>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a VPC (its instances leave it; the default VPC stays)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.resolveVPC(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if v.Default {
				return fmt.Errorf("VPC %s is the account's default: it cannot be deleted", v.Name)
			}
			if !yes {
				q := fmt.Sprintf("Delete VPC %s (%s)? [y/N] ", v.Name, orDashPtr(v.Subnet))
				if n := len(v.InstanceIDs); n > 0 {
					q = fmt.Sprintf("Delete VPC %s (%s) and its private addresses on %s? [y/N] ",
						v.Name, orDashPtr(v.Subnet), plural(n, "instance", "instances"))
				}
				ok, err := a.confirm(q, "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/vpcs/"+v.ID, nil, nil, nil); err != nil {
				return notOffered(err, "Deleting VPCs from the API")
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": v.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "VPC %s deleted.\n", v.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")

	cmd.AddCommand(get, create, update, remove, vpcAttachCmd(a, true), vpcAttachCmd(a, false))
	return cmd
}

// vpcDetail is a VPC and, when names is given, its members by name; region
// is its region's name, when known.
func vpcDetail(v api.VPC, names map[string]string, region string) detail {
	var d detail
	d.add("ID", v.ID)
	d.add("Name", v.Name)
	if v.Description != nil && *v.Description != "" {
		d.add("Description", *v.Description)
	}
	d.add("Subnet", orDashPtr(v.Subnet))
	if v.RegionID != nil {
		d.add("Region", firstNonEmpty(region, *v.RegionID))
	}
	d.add("Default", yesNo(v.Default))
	if len(v.Members) == 0 && len(v.InstanceIDs) > 0 {
		// An API without members[]: the ids alone.
		d.add("Instances", strings.Join(v.InstanceIDs, ", "))
	}
	if v.CreatedAt != nil {
		d.add("Created", whenPtr(v.CreatedAt))
	}
	switch {
	case len(v.Members) > 0:
		var b strings.Builder
		w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "INSTANCE\tPRIVATE IP\tSTATUS")
		for _, m := range v.Members {
			label := m.InstanceID
			if n := names[m.InstanceID]; n != "" {
				label = n + " (" + m.InstanceID + ")"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", label, orDashPtr(m.PrivateIP), orDash(m.Status))
		}
		_ = w.Flush()
		d.after = append(d.after, "")
		d.after = append(d.after, strings.Split(strings.TrimRight(b.String(), "\n"), "\n")...)
	case len(v.InstanceIDs) == 0:
		d.after = append(d.after, fmt.Sprintf("No instances in it: `mw vpcs attach %s <instance>`.", v.Name))
	}
	return d
}

// regionLabel is a region's name and code ("Belo Horizonte (br-bh)"), best
// effort: "" when it cannot be looked up.
func (a *App) regionLabel(ctx context.Context, id *string) string {
	if id == nil {
		return ""
	}
	c, err := a.Client()
	if err != nil {
		return ""
	}
	all, err := api.All[api.Region](ctx, c, "/regions", nil)
	if err != nil {
		return ""
	}
	for _, r := range all {
		if r.ID == *id {
			return strings.TrimSpace(r.Name + " " + parens(str(r.RegionName)))
		}
	}
	return ""
}

// instanceNames maps the account's instance ids to their names, best effort.
func (a *App) instanceNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	c, err := a.Client()
	if err != nil {
		return names
	}
	all, err := api.All[api.Instance](ctx, c, "/instances", nil)
	if err != nil {
		return names
	}
	for _, in := range all {
		if in.Name != nil && *in.Name != "" {
			names[in.ID] = *in.Name
		}
	}
	return names
}

// checkPrivateIP refuses an address the API would: not IPv4, or outside the
// VPC's subnet.
func checkPrivateIP(ip string, v *api.VPC) error {
	addr := net.ParseIP(ip)
	if addr == nil || addr.To4() == nil {
		return fmt.Errorf("--private-ip %s is not an IPv4 address", ip)
	}
	if v.Subnet != nil {
		if _, subnet, err := net.ParseCIDR(*v.Subnet); err == nil && !subnet.Contains(addr) {
			return fmt.Errorf("--private-ip %s is outside VPC %s's subnet %s", ip, v.Name, *v.Subnet)
		}
	}
	return nil
}

func vpcAttachCmd(a *App, attach bool) *cobra.Command {
	var restart, wait bool
	var privateIP string
	var timeout time.Duration
	use, short := "attach <vpc> <instance>", "Put an instance in a VPC (same region; it gets a private address in the subnet)"
	if !attach {
		use, short = "detach <vpc> <instance>", "Take an instance out of a VPC (its public address stays)"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			v, err := a.resolveVPC(ctx, args[0])
			if err != nil {
				return err
			}
			in, err := a.resolveInstance(ctx, args[1])
			if err != nil {
				return err
			}
			if privateIP != "" {
				if err := checkPrivateIP(privateIP, v); err != nil {
					return err
				}
			}
			c, _ := a.Client()
			// An API that answers before the job that plugs the private NIC in
			// exists (operation_id null) is followed through the VPC's
			// operations: note them now, to tell that job from the ones before.
			var before []string
			if attach && wait {
				if before, err = a.vpcOperationIDs(ctx, v.ID); err != nil {
					return err
				}
			}
			var acc api.OperationAccepted
			if attach {
				body := map[string]interface{}{"instance_id": in.ID}
				if restart {
					body["restart"] = true
				}
				if privateIP != "" {
					body["private_ip"] = privateIP
				}
				err = c.Do(ctx, "POST", "/vpcs/"+v.ID+"/instances", nil, body, &acc)
			} else {
				q := url.Values{}
				if restart {
					q.Set("restart", "true")
				}
				err = c.Do(ctx, "DELETE", "/vpcs/"+v.ID+"/instances/"+url.PathEscape(in.ID), q, nil, &acc)
			}
			if err != nil {
				if !attach {
					return notOffered(err, "Detaching instances from VPCs from the API")
				}
				return notOffered(err, "Attaching instances to VPCs from the API")
			}
			if wait {
				// A detach is done when it answers; an attach when its job is.
				if err := a.waitAccepted(ctx, acc, timeout, func(ctx context.Context) error {
					if !attach {
						return nil
					}
					return a.waitVPCJob(ctx, v.ID, before, timeout)
				}); err != nil {
					return err
				}
				if restart && in.PowerStateCached == "running" {
					if _, err := a.waitInstance(ctx, in.ID, timeout, "running", func(st api.InstanceStatus) bool {
						return st.PowerState == "running" && !st.Busy
					}); err != nil {
						return err
					}
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			what := fmt.Sprintf("%s → VPC %s", instanceLabel(in), v.Name)
			if !attach {
				what = fmt.Sprintf("%s out of VPC %s", instanceLabel(in), v.Name)
			}
			switch {
			case wait && attach:
				fmt.Fprintf(a.Out, "%s: done.\n", what)
			case wait || !attach:
				fmt.Fprintf(a.Out, "%s.\n", what)
			default:
				fmt.Fprintln(a.Out, acceptedText(acc, what))
			}
			if attach && !restart {
				fmt.Fprintln(a.Out, "Its private NIC comes up on the next restart (or hot-plug); --restart restarts it now.")
			}
			return nil
		},
	}
	if attach {
		cmd.Flags().BoolVar(&restart, "restart", false, "restart the instance so the private NIC comes up at once")
		cmd.Flags().StringVar(&privateIP, "private-ip", "", "its address in the VPC's subnet (default: a free one)")
	} else {
		cmd.Flags().BoolVar(&restart, "restart", false, "restart the instance afterwards")
	}
	waitFlags(cmd, &wait, &timeout, 10*time.Minute)
	return cmd
}

// vpcOperationIDs is the VPC's recent operations, newest first.
func (a *App) vpcOperationIDs(ctx context.Context, vpcID string) ([]string, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var p api.Page[api.Operation]
	q := url.Values{"resource_type": {"vpc"}, "resource_id": {vpcID}, "limit": {"20"}}
	if err := c.Do(ctx, "GET", "/operations", q, nil, &p); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(p.Data))
	for _, op := range p.Data {
		ids = append(ids, op.ID)
	}
	return ids, nil
}

// waitVPCJob waits for the first VPC operation not in before to appear, then
// for it to finish.
func (a *App) waitVPCJob(ctx context.Context, vpcID string, before []string, timeout time.Duration) error {
	seen := map[string]bool{}
	for _, id := range before {
		seen[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		ids, err := a.vpcOperationIDs(ctx, vpcID)
		if err != nil && ctx.Err() == nil {
			return err
		}
		for _, id := range ids {
			if !seen[id] {
				_, err := a.waitOperation(ctx, id, timeout)
				return err
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the job that plugs the private NIC in has not started after %s: `mw operations --resource-type vpc --resource-id %s` lists it", timeout, vpcID)
		case <-time.After(pollEvery):
		}
	}
}

func (a *App) resolveVPC(ctx context.Context, ref string) (*api.VPC, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	if digitsRe.MatchString(ref) {
		var v api.VPC
		if err := c.Do(ctx, "GET", "/vpcs/"+ref, nil, nil, &v); err != nil {
			return nil, err
		}
		return &v, nil
	}
	all, err := api.All[api.VPC](ctx, c, "/vpcs", nil)
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if strings.EqualFold(v.Name, ref) {
			v := v
			return &v, nil
		}
	}
	return nil, fmt.Errorf("no VPC %s (`mw vpcs` lists them)", ref)
}

// rdnsCmd: reverse DNS (PTR) of the account's IP addresses.
func rdnsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rdns",
		Aliases: []string{"reverse-dns", "ptr"},
		Short:   "Reverse DNS (PTR) of your instances' IP addresses",
		Long: "The name an IP address answers to in reverse lookups — what mail servers check.\n" +
			"A name is accepted only when it resolves forward to the same address.",
		Example: "  mw rdns\n  mw rdns set 203.0.113.10 mail.loja.com.br\n  mw rdns reset 203.0.113.10",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var p api.Page[api.ReverseDNS]
			if err := c.Do(cmd.Context(), "GET", "/reverse-dns", nil, nil, &p); err != nil {
				return notOffered(err, "Reverse DNS from the API")
			}
			l := listing{headers: []string{"IP", "VERSION", "NAME", "DEFAULT", "INSTANCE", "EDITABLE"}, empty: "No addresses."}
			for _, r := range p.Data {
				editable := "yes"
				if r.Editable != nil && !*r.Editable {
					editable = "from " + whenPtr(r.EditableAt)
				}
				l.add(r.IP, r.IP, fmt.Sprintf("v%d", r.Version), orDashPtr(r.Hostname), orDashPtr(r.DefaultHostname), orDashPtr(r.InstanceID), editable)
			}
			return a.printList(p.Data, l)
		},
	}
	set := &cobra.Command{
		Use:   "set <ip> <name>",
		Short: "Point an address's reverse DNS at a name (it must resolve back to the address)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var r api.ReverseDNS
			if err := c.Do(cmd.Context(), "PUT", "/reverse-dns/"+url.PathEscape(args[0]), nil, map[string]string{"hostname": strings.TrimSuffix(args[1], ".")}, &r); err != nil {
				return notOffered(err, "Reverse DNS from the API")
			}
			if a.JSON {
				return a.encode(r)
			}
			fmt.Fprintf(a.Out, "%s now answers to %s.\n", r.IP, orDashPtr(r.Hostname))
			return nil
		},
	}
	reset := &cobra.Command{
		Use:   "reset <ip>",
		Short: "Put an address's reverse DNS back to the default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var r api.ReverseDNS
			if err := c.Do(cmd.Context(), "POST", "/reverse-dns/"+url.PathEscape(args[0])+"/reset", nil, nil, &r); err != nil {
				return notOffered(err, "Reverse DNS from the API")
			}
			if a.JSON {
				return a.encode(r)
			}
			fmt.Fprintf(a.Out, "%s is back to %s.\n", firstNonEmpty(r.IP, args[0]), firstNonEmpty(str(r.Hostname), str(r.DefaultHostname), "the default"))
			return nil
		},
	}
	cmd.AddCommand(set, reset)
	return cmd
}
