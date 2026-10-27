package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// instancesCmd: VPS instances — create, see, power, rescue, reinstall,
// rename, destroy.
func instancesCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instances",
		Aliases: []string{"instance", "vps", "servers", "server"},
		Short:   "VPS instances: create, power, rescue, reinstall, destroy",
		Example: "  mw instances\n" +
			"  mw instances create --plan c2m4 --image ubuntu-24-04 --ssh-key ~/.ssh/id_ed25519.pub --wait\n" +
			"  mw instances reboot web-1 --wait\n" +
			"  mw instances delete 24377",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Instance](cmd.Context(), c, "/instances", nil)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "STATE", "REGION", "PLAN", "SPECS", "IPV4"}, empty: "No instances yet: `mw instances create --plan … --image …`."}
			for _, in := range all {
				region := ""
				if in.Region != nil {
					region = firstNonEmpty(str(in.Region.RegionName), in.Region.Name)
				}
				l.add(in.ID, in.ID, orDashPtr(in.Name), instanceState(in), orDash(region), orDashPtr(in.OfferingCode), specsText(in.Specs), orDash(primaryIPv4(in)))
			}
			return a.printList(all, l)
		},
	}
	cmd.AddCommand(
		instanceGetCmd(a), instanceCreateCmd(a), instanceUpdateCmd(a), instanceRenameCmd(a), instanceDeleteCmd(a),
		instancePowerCmd(a, "start"), instancePowerCmd(a, "stop"), instancePowerCmd(a, "reboot"),
		instanceStatusCmd(a), instanceRescueCmd(a),
		instanceReinstallCmd(a), instanceResetPasswordCmd(a),
		instanceMetricsCmd(a), instanceBandwidthCmd(a),
		instanceResizeCmd(a), instanceResizeOptionsCmd(a), instanceCloneCmd(a),
		instanceISOCmd(a), instanceSecurityCmd(a), instanceScanCmd(a), instanceScansCmd(a),
	)
	return cmd
}

func instanceState(in api.Instance) string {
	if in.Suspended {
		return "suspended"
	}
	return orDash(in.PowerStateCached)
}

// instanceDetail is an instance and, when projects is given, its project by
// name.
func instanceDetail(in api.Instance, projects map[string]string) detail {
	var d detail
	d.add("ID", in.ID)
	d.add("Name", orDashPtr(in.Name))
	if in.Description != nil && *in.Description != "" {
		d.add("Description", *in.Description)
	}
	d.add("State", instanceState(in))
	if in.Region != nil {
		d.add("Region", strings.TrimSpace(in.Region.Name+" "+parens(str(in.Region.RegionName))))
	}
	d.add("Plan", orDashPtr(in.OfferingCode))
	d.add("Specs", specsText(in.Specs))
	switch {
	case in.Image != nil && *in.Image != "":
		d.add("Image", *in.Image)
	case in.CustomImageID != nil && *in.CustomImageID != "":
		d.add("Image", "custom image "+*in.CustomImageID)
	}
	d.add("Hostname", orDashPtr(in.Hostname))
	var v4, v6 []string
	for _, ip := range in.IPAddresses {
		label := ip.Address
		if ip.Primary {
			label += " (primary)"
		}
		if ip.Version == "ipv6" {
			v6 = append(v6, label)
		} else {
			v4 = append(v4, label)
		}
	}
	d.add("IPv4", orDash(strings.Join(v4, ", ")))
	if len(v6) > 0 {
		d.add("IPv6", strings.Join(v6, ", "))
	}
	if in.VPCID != nil {
		d.add("VPC", *in.VPCID)
	}
	if in.ProjectID != nil && *in.ProjectID != "" {
		label := *in.ProjectID
		if n := projects[*in.ProjectID]; n != "" {
			label = n + " (" + *in.ProjectID + ")"
		}
		d.add("Project", label)
	}
	if in.SubscriptionID != nil && *in.SubscriptionID != "" {
		d.add("Subscription", *in.SubscriptionID)
	}
	d.add("Created", whenPtr(in.CreatedAt))
	return d
}

// projectNames maps project ids to names, when the instance is in one (best
// effort: the id alone otherwise).
func (a *App) projectNames(ctx context.Context, in *api.Instance) map[string]string {
	if in.ProjectID == nil || *in.ProjectID == "" {
		return nil
	}
	all, err := a.projects(ctx)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	for _, p := range all {
		names[p.ID] = p.Name
	}
	return names
}

func parens(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

func instanceGetCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "get <instance>",
		Short: "An instance: its plan, image, addresses, state, project and subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printDetail(in, instanceDetail(*in, a.projectNames(cmd.Context(), in)))
		},
	}
}

func instanceCreateCmd(a *App) *cobra.Command {
	var o orderFlags
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Order a new instance: shows the price, then orders it",
		Long: "Order a new instance. The configuration is priced first and shown; nothing is bought\n" +
			"until you confirm (or pass --yes). A new subscription raises an invoice, and the instance\n" +
			"is built once it is paid; added to an existing subscription (--subscription), it is paid\n" +
			"from its balance and built at once.",
		Example: "  mw instances create --plan c2m4 --image ubuntu-24-04 --region br-bh --ssh-key work --wait\n" +
			"  mw instances create --plan c4m8 --image wordpress --subscription 0193… --project loja --yes\n" +
			"  mw instances create --plan c2m4 --image debian-12 --user-data @cloud-init.yaml",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			body, err := a.orderBody(ctx, &o)
			if err != nil {
				return err
			}
			q, err := a.quote(ctx, body)
			if err != nil {
				return err
			}
			if !a.JSON {
				a.printQuote(a.Out, q)
			}
			if !q.Purchasable {
				return errors.New("this configuration cannot be ordered as it is: change the plan or the disk (see above)")
			}
			if !yes {
				ok, err := a.confirm("Order it? [y/N] ", "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing ordered.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			var ord api.Order
			if err := c.Do(ctx, "POST", "/orders", nil, body, &ord); err != nil {
				return err
			}
			if !a.JSON {
				switch ord.Status {
				case "provisioned":
					fmt.Fprintf(a.Out, "Ordered: instance %s is being built.\n", strings.Join(ord.InstanceIDs, ", "))
				case "pending_payment":
					fmt.Fprintln(a.Out, "Ordered: the instance is built once the invoice is paid.")
					if ord.Payment != nil {
						fmt.Fprintf(a.Out, "  Invoice %s: %s", ord.Payment.InvoiceID, cents(ord.Payment.AmountCents))
						if ord.Payment.PayURL != nil {
							fmt.Fprintf(a.Out, " — pay at %s", *ord.Payment.PayURL)
						}
						fmt.Fprintln(a.Out)
					}
				default:
					fmt.Fprintf(a.Out, "Order %s: %s.\n", orDashPtr(ord.ID), ord.Status)
				}
			}
			if wait {
				done, err := a.waitOrder(ctx, ord, timeout)
				if err != nil {
					return err
				}
				ord = *done
			}
			if a.JSON {
				return a.encode(ord)
			}
			if !wait && ord.ID != nil {
				fmt.Fprintf(a.Out, "Follow it: `mw orders get %s`", *ord.ID)
				if len(ord.InstanceIDs) > 0 {
					fmt.Fprintf(a.Out, ", `mw instances status %s`", ord.InstanceIDs[0])
				}
				fmt.Fprintln(a.Out, ".")
			}
			return nil
		},
	}
	o.registerOrder(cmd)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "order without asking (the price is still shown)")
	waitFlags(cmd, &wait, &timeout, 30*time.Minute)
	return cmd
}

// waitOrder waits for an order to be paid and its instances to be up.
func (a *App) waitOrder(ctx context.Context, ord api.Order, timeout time.Duration) (*api.Order, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if ord.ID != nil && ord.Status != "provisioned" {
		last := ""
		for {
			if ord.Status != last && !a.JSON {
				fmt.Fprintf(a.Err, "  order: %s\n", strings.ReplaceAll(ord.Status, "_", " "))
				last = ord.Status
			}
			if ord.Status == "failed" || ord.Status == "cancelled" {
				return &ord, fmt.Errorf("order %s %s", *ord.ID, ord.Status)
			}
			if (ord.Status == "provisioned" || ord.Status == "provisioning") && len(ord.InstanceIDs) > 0 {
				break
			}
			if time.Now().After(deadline) {
				return &ord, fmt.Errorf("order %s is still %s after %s: `mw orders get %s`", *ord.ID, ord.Status, timeout, *ord.ID)
			}
			select {
			case <-ctx.Done():
				return &ord, ctx.Err()
			case <-time.After(pollEvery):
			}
			var next api.Order
			if err := c.Do(ctx, "GET", "/orders/"+*ord.ID, nil, nil, &next); err != nil {
				return &ord, err
			}
			ord = next
		}
	}
	for _, id := range ord.InstanceIDs {
		left := time.Until(deadline)
		if left <= 0 {
			left = time.Second
		}
		if _, err := a.waitInstance(ctx, id, left, "running", func(s api.InstanceStatus) bool {
			return s.PowerState == "running" && !s.Busy && (s.DeploymentProgress == nil || *s.DeploymentProgress >= 100)
		}); err != nil {
			return &ord, err
		}
		if !a.JSON {
			fmt.Fprintf(a.Out, "Instance %s is up.\n", id)
		}
	}
	return &ord, nil
}

func instanceUpdateCmd(a *App) *cobra.Command {
	var name, description string
	cmd := &cobra.Command{
		Use:   "update <instance>",
		Short: "Change an instance's name or description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]interface{}{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("description") {
				body["description"] = description
			}
			if len(body) == 0 {
				return errors.New("nothing to change: pass --name or --description")
			}
			return a.patchInstance(cmd.Context(), args[0], body)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "its name (empty clears it)")
	cmd.Flags().StringVar(&description, "description", "", "a note (empty clears it)")
	return cmd
}

func instanceRenameCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <instance> <new-name>",
		Short: "Rename an instance",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.patchInstance(cmd.Context(), args[0], map[string]interface{}{"name": args[1]})
		},
	}
}

func (a *App) patchInstance(ctx context.Context, ref string, body map[string]interface{}) error {
	in, err := a.resolveInstance(ctx, ref)
	if err != nil {
		return err
	}
	c, _ := a.Client()
	var out api.Instance
	if err := c.Do(ctx, "PATCH", "/instances/"+in.ID, nil, body, &out); err != nil {
		return err
	}
	return a.printDetail(out, instanceDetail(out, nil))
}

func instanceDeleteCmd(a *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <instance>",
		Aliases: []string{"destroy", "rm"},
		Short:   "Destroy an instance for good: its disks and addresses go with it",
		Long: "Destroy an instance. Irreversible: its disks, snapshots and IP addresses are gone and\n" +
			"billing for it stops. You type its id to confirm (or pass --yes).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			// What removing it does to the bill, and what would make the API
			// refuse it, when the API says (to standard error under -o json).
			var preview api.RemovalPreview
			if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/removal-preview", nil, nil, &preview); err == nil {
				w := a.Out
				if a.JSON {
					w = a.Err
				}
				if preview.Summary != "" {
					fmt.Fprintln(w, preview.Summary)
				}
				if len(preview.Blockers) > 0 {
					fmt.Fprintf(w, "The deletion will be refused now: %s.\n", blockerText(preview.Blockers))
				}
			}
			if !yes {
				q := fmt.Sprintf("Destroy instance %s for good? Its disks, snapshots and addresses cannot be recovered.\nType its id (%s) to confirm: ", instanceLabel(in), in.ID)
				if err := a.typeToConfirm(q, in.ID, "--yes"); err != nil {
					return err
				}
			}
			var out api.InstanceDeleted
			if err := c.Do(ctx, "DELETE", "/instances/"+in.ID, url.Values{"confirm": {in.ID}}, nil, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(out)
			}
			if out.Deleted {
				fmt.Fprintf(a.Out, "Instance %s destroyed.\n", instanceLabel(in))
			} else {
				fmt.Fprintf(a.Out, "Instance %s: %s\n", instanceLabel(in), out.Message)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "destroy without asking")
	return cmd
}

func instancePowerCmd(a *App, action string) *cobra.Command {
	var wait bool
	var timeout time.Duration
	short := map[string]string{
		"start":  "Start a stopped instance",
		"stop":   "Shut an instance down (gracefully, then off)",
		"reboot": "Restart an instance",
	}[action]
	cmd := &cobra.Command{
		Use:   action + " <instance>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var acc api.TaskAccepted
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/power", nil, map[string]string{"action": action}, &acc); err != nil {
				return err
			}
			if wait {
				want := "running"
				if action == "stop" {
					want = "stopped"
				}
				if _, err := a.waitInstance(ctx, in.ID, timeout, want, func(s api.InstanceStatus) bool {
					return s.PowerState == want && !s.Busy
				}); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			done := map[string]string{"start": "started", "stop": "stopped", "reboot": "restarted"}[action]
			if wait {
				fmt.Fprintf(a.Out, "Instance %s %s.\n", instanceLabel(in), done)
			} else {
				fmt.Fprintf(a.Out, "Instance %s: %s requested (`mw instances status %s` follows it, or add --wait).\n", instanceLabel(in), action, in.ID)
			}
			return nil
		},
	}
	waitFlags(cmd, &wait, &timeout, 5*time.Minute)
	return cmd
}

func instanceStatusCmd(a *App) *cobra.Command {
	var cached bool
	cmd := &cobra.Command{
		Use:   "status <instance>",
		Short: "Live state: power, uptime, CPU, memory and disk in use",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			q := url.Values{}
			if !cached {
				q.Set("fresh", "true")
			}
			var st api.InstanceStatus
			if err := c.Do(cmd.Context(), "GET", "/instances/"+in.ID+"/status", q, nil, &st); err != nil {
				return err
			}
			var d detail
			d.add("Instance", instanceLabel(in))
			state := st.PowerState
			if st.Suspended {
				state += " (suspended)"
			}
			if st.Busy {
				state += " (busy: an operation is running)"
			}
			d.add("State", state)
			if st.DeploymentProgress != nil {
				d.add("Being built", fmt.Sprintf("%.0f%%", *st.DeploymentProgress))
			}
			if st.UptimeSeconds != nil {
				d.add("Up for", duration(int64(*st.UptimeSeconds)))
			}
			if st.Usage.CPU != nil {
				d.add("CPU", fmt.Sprintf("%.0f%%", *st.Usage.CPU*100))
			}
			if st.Usage.MemoryUsedBytes != nil && st.Usage.MemoryTotalBytes != nil {
				d.add("Memory", fmt.Sprintf("%s of %s", bytesText(strconv.FormatFloat(*st.Usage.MemoryUsedBytes, 'f', 0, 64)), bytesText(strconv.FormatFloat(*st.Usage.MemoryTotalBytes, 'f', 0, 64))))
			}
			switch {
			case st.Usage.DiskUsedBytes != nil && st.Usage.DiskTotalBytes != nil:
				d.add("Disk", fmt.Sprintf("%s of %s", bytesText(strconv.FormatFloat(*st.Usage.DiskUsedBytes, 'f', 0, 64)), bytesText(strconv.FormatFloat(*st.Usage.DiskTotalBytes, 'f', 0, 64))))
			case st.Usage.DiskState != nil && *st.Usage.DiskState == "agent_unavailable":
				d.add("Disk", "unknown (the guest agent does not answer)")
			}
			return a.printDetail(st, d)
		},
	}
	cmd.Flags().BoolVar(&cached, "cached", false, "accept the last reading (up to a minute old) instead of asking the hypervisor")
	return cmd
}

func instanceRescueCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rescue",
		Short: "Rescue mode: boot a rescue system to repair an instance that does not start",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	status := &cobra.Command{
		Use:   "status <instance>",
		Short: "Whether rescue mode is available, and the session if one is open",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var r api.RescueOptions
			if err := c.Do(cmd.Context(), "GET", "/instances/"+in.ID+"/rescue", nil, nil, &r); err != nil {
				return err
			}
			var d detail
			d.add("Instance", instanceLabel(in))
			if s := r.Session; s != nil {
				d.add("Session", fmt.Sprintf("%s (%s)", s.State, s.Media.Code))
				if s.Access.Host != nil {
					d.add("SSH", fmt.Sprintf("%s@%s", firstNonEmpty(str(s.Access.Username), "root"), *s.Access.Host))
				}
				for _, hk := range s.Access.HostKeys {
					d.add("Host key", hk.Type+" "+hk.SHA256)
				}
				if s.AutoExitAt != nil {
					d.add("Leaves by itself", whenPtr(s.AutoExitAt))
				}
				if s.ErrorCode != nil {
					d.add("Problem", *s.ErrorCode)
				}
			} else {
				avail := "yes"
				if !r.Available {
					avail = "no (" + strings.ReplaceAll(orDashPtr(r.Reason), "_", " ") + ")"
				}
				d.add("Available", avail)
				var media []string
				for _, m := range r.Media {
					if m.Available {
						label := m.Code
						if m.Recommended {
							label += " (recommended)"
						}
						media = append(media, label)
					}
				}
				d.add("Systems", orDash(strings.Join(media, ", ")))
			}
			return a.printDetail(r, d)
		},
	}

	var media string
	var keys []string
	var passwordAuth, snapshot, wait bool
	var timeout time.Duration
	enter := &cobra.Command{
		Use:   "enter <instance>",
		Short: "Boot the instance into the rescue system (its disks attached, untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{}
			if media != "" {
				body["media_code"] = media
			}
			if len(keys) > 0 {
				lines, err := a.publicKeyLines(ctx, keys)
				if err != nil {
					return err
				}
				body["ssh_public_keys"] = lines
			}
			if cmd.Flags().Changed("password-auth") {
				body["password_auth"] = passwordAuth
			}
			if snapshot {
				body["take_snapshot"] = true
			}
			c, _ := a.Client()
			var out api.RescueEntered
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/rescue", nil, body, &out); err != nil {
				return err
			}
			if wait && out.JobID != "" {
				if _, err := a.waitOperation(ctx, out.JobID, timeout); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintf(a.Out, "Instance %s is going into rescue mode (session %s).\n", instanceLabel(in), shortID(out.SessionID))
			if out.RootPassword != nil {
				fmt.Fprintf(a.Out, "Rescue root password (shown this once): %s\n", *out.RootPassword)
			}
			fmt.Fprintf(a.Out, "`mw instances rescue status %s` shows how to connect.\n", in.ID)
			return nil
		},
	}
	enter.Flags().StringVar(&media, "media", "", "rescue-linux or rescue-windows (default: the recommended one)")
	enter.Flags().StringSliceVar(&keys, "ssh-key", nil, "an SSH key to sign in with: name, id or .pub file (repeat for more)")
	enter.Flags().BoolVar(&passwordAuth, "password-auth", true, "allow signing in with the rescue password")
	enter.Flags().BoolVar(&snapshot, "snapshot", false, "take a snapshot first")
	waitFlags(enter, &wait, &timeout, 15*time.Minute)

	var noStart, waitExit bool
	var timeoutExit time.Duration
	exit := &cobra.Command{
		Use:   "exit <instance>",
		Short: "Leave rescue mode: the instance boots its own system again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			q := url.Values{"start": {strconv.FormatBool(!noStart)}}
			c, _ := a.Client()
			var out api.RescueTransition
			if err := c.Do(ctx, "DELETE", "/instances/"+in.ID+"/rescue", q, nil, &out); err != nil {
				return err
			}
			if waitExit && out.JobID != "" {
				if _, err := a.waitOperation(ctx, out.JobID, timeoutExit); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintf(a.Out, "Instance %s is leaving rescue mode.\n", instanceLabel(in))
			return nil
		},
	}
	exit.Flags().BoolVar(&noStart, "no-start", false, "leave it stopped")
	waitFlags(exit, &waitExit, &timeoutExit, 15*time.Minute)

	password := &cobra.Command{
		Use:   "password <instance>",
		Short: "A new root password for the open rescue session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.RescuePassword
			if err := c.Do(cmd.Context(), "POST", "/instances/"+in.ID+"/rescue/password", nil, nil, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintln(a.Out, out.RootPassword)
			return nil
		},
	}
	cmd.AddCommand(status, enter, exit, password)
	return cmd
}

// publicKeyLines turns --ssh-key values into public key lines: a .pub file,
// or a key in the account (its public half).
func (a *App) publicKeyLines(ctx context.Context, refs []string) ([]string, error) {
	var lines []string
	var account []api.SSHKey
	loaded := false
	for _, ref := range splitList(refs) {
		if looksLikeFile(ref) {
			line, _, err := readPublicKey(ref)
			if err != nil {
				return nil, err
			}
			lines = append(lines, line)
			continue
		}
		if !loaded {
			keys, err := a.sshKeys(ctx)
			if err != nil {
				return nil, err
			}
			account, loaded = keys, true
		}
		found := ""
		for _, k := range account {
			if k.ID == ref || strings.EqualFold(k.Name, ref) || k.Fingerprint == ref {
				found = k.PublicKey
			}
		}
		if found == "" {
			return nil, fmt.Errorf("no SSH key %s: `mw ssh-keys` lists them, or pass a .pub file", ref)
		}
		lines = append(lines, found)
	}
	return lines, nil
}

func instanceReinstallCmd(a *App) *cobra.Command {
	var image, customImage, password, userData string
	var passwordStdin, yes, wait bool
	var keys, inputs []string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "reinstall <instance>",
		Short: "Reinstall the operating system (or an app): the disk is wiped",
		Long: "Reinstall an instance from an image. Everything on its disk is erased; its addresses,\n" +
			"plan and id stay. You type its id to confirm (or pass --yes).",
		Example: "  mw instances reinstall web-1 --image debian-12 --ssh-key work --wait\n" +
			"  mw images   # what it can be reinstalled with",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if (image == "") == (customImage == "") {
				return errors.New("pass --image (`mw images`) or --custom-image")
			}
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{}
			if image != "" {
				body["image"] = image
			} else {
				id, err := a.customImageID(ctx, customImage)
				if err != nil {
					return err
				}
				body["custom_image_id"] = id
			}
			if passwordStdin {
				pw, err := readLine(a, "--password-stdin")
				if err != nil {
					return err
				}
				password = pw
			}
			if password != "" {
				body["password"] = password
			}
			if len(keys) > 0 {
				ids, err := a.resolveSSHKeys(ctx, keys)
				if err != nil {
					return err
				}
				body["ssh_key_ids"] = ids
			}
			if userData != "" {
				ud, err := readUserData(a.In, userData)
				if err != nil {
					return err
				}
				body["user_data"] = ud
			}
			if len(inputs) > 0 {
				m := map[string]interface{}{}
				for _, kv := range inputs {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("--app-input %q: key=value", kv)
					}
					m[strings.TrimSpace(k)] = v
				}
				body["app_inputs"] = m
			}
			if !yes {
				q := fmt.Sprintf("Reinstall %s? EVERYTHING on its disk is erased.\nType its id (%s) to confirm: ", instanceLabel(in), in.ID)
				if err := a.typeToConfirm(q, in.ID, "--yes"); err != nil {
					return err
				}
			}
			c, _ := a.Client()
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/reinstall", nil, body, &acc); err != nil {
				return notOffered(err, "Reinstalling from the API")
			}
			if wait {
				if err := a.waitAccepted(ctx, acc, timeout, func(ctx context.Context) error {
					_, err := a.waitInstance(ctx, in.ID, timeout, "running", func(s api.InstanceStatus) bool {
						return s.PowerState == "running" && !s.Busy
					})
					return err
				}); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			fmt.Fprintln(a.Out, acceptedText(acc, "Reinstall of "+instanceLabel(in)))
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "operating system or app code")
	cmd.Flags().StringVar(&customImage, "custom-image", "", "one of your own disk images, by name or id (`mw custom-images`)")
	cmd.Flags().StringVar(&password, "password", "", "the new root/Administrator password")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from standard input")
	cmd.Flags().StringSliceVar(&keys, "ssh-key", nil, "an SSH key to put on it: id, name, fingerprint or .pub file (repeat for more)")
	cmd.Flags().StringVar(&userData, "user-data", "", "cloud-config to run on first boot: @file, - or the YAML (≤ 16 KiB)")
	cmd.Flags().StringSliceVar(&inputs, "app-input", nil, "an answer to the app's setup questions, key=value")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "reinstall without asking")
	waitFlags(cmd, &wait, &timeout, 30*time.Minute)
	return cmd
}

func readLine(a *App, flag string) (string, error) {
	var buf [1]byte
	var line []byte
	for {
		n, err := a.In.Read(buf[:])
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			break
		}
	}
	s := strings.TrimRight(string(line), "\r")
	if s == "" {
		return "", fmt.Errorf("%s: nothing on standard input", flag)
	}
	return s, nil
}

func instanceResetPasswordCmd(a *App) *cobra.Command {
	var username, password string
	var stdin bool
	cmd := &cobra.Command{
		Use:   "reset-password <instance>",
		Short: "Set a new password for root/Administrator (through the guest agent, no reboot)",
		Long: "Set a new password on a running instance, through its guest agent — no reboot, nothing\n" +
			"else changes. Without --password a strong one is generated and shown once.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if stdin {
				pw, err := readLine(a, "--password-stdin")
				if err != nil {
					return err
				}
				password = pw
			}
			body := map[string]interface{}{}
			if username != "" {
				body["username"] = username
			}
			if password != "" {
				body["password"] = password
			}
			c, _ := a.Client()
			var out api.PasswordReset
			if err := c.Do(cmd.Context(), "POST", "/instances/"+in.ID+"/reset-password", nil, body, &out); err != nil {
				return notOffered(err, "Resetting the password from the API")
			}
			if a.JSON {
				return a.encode(out)
			}
			user := firstNonEmpty(out.Username, username, "root")
			if out.Password != nil {
				fmt.Fprintf(a.Out, "New password for %s on %s (shown this once): %s\n", user, instanceLabel(in), *out.Password)
			} else {
				fmt.Fprintf(a.Out, "Password of %s on %s changed.\n", user, instanceLabel(in))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "the account (default: root, or Administrator on Windows)")
	cmd.Flags().StringVar(&password, "password", "", "the new password (default: one is generated and shown)")
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "read the new password from standard input")
	return cmd
}

func instanceMetricsCmd(a *App) *cobra.Command {
	var timeframe string
	var points bool
	cmd := &cobra.Command{
		Use:   "metrics <instance>",
		Short: "CPU, memory, disk and network over the last hour, day, week, month or year",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch timeframe {
			case "hour", "day", "week", "month", "year":
			default:
				return errors.New("--timeframe is hour, day, week, month or year")
			}
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var m api.InstanceMetrics
			if err := c.Do(cmd.Context(), "GET", "/instances/"+in.ID+"/metrics", url.Values{"timeframe": {timeframe}}, nil, &m); err != nil {
				return notOffered(err, "Instance metrics")
			}
			if a.JSON {
				return a.encode(m)
			}
			if points {
				l := listing{headers: []string{"TIME", "CPU", "MEMORY", "DISK READ", "DISK WRITE", "NET IN", "NET OUT"}, empty: "No readings in this period."}
				for _, p := range m.Points {
					l.add(p.Time, when(p.Time), pct(p.CPUPct), pointMemText(p), bps(p.DiskReadBps), bps(p.DiskWriteBps), bps(p.NetInBps), bps(p.NetOutBps))
				}
				return a.printList(m, l)
			}
			type col struct {
				name string
				get  func(api.MetricPoint) *float64
				fmt  func(*float64) string
			}
			cols := []col{
				{"CPU", func(p api.MetricPoint) *float64 { return p.CPUPct }, pct},
				{"Memory", memPct, pct},
				{"Disk read", func(p api.MetricPoint) *float64 { return p.DiskReadBps }, bps},
				{"Disk write", func(p api.MetricPoint) *float64 { return p.DiskWriteBps }, bps},
				{"Network in", func(p api.MetricPoint) *float64 { return p.NetInBps }, bps},
				{"Network out", func(p api.MetricPoint) *float64 { return p.NetOutBps }, bps},
			}
			l := listing{headers: []string{"METRIC", "AVERAGE", "PEAK", "LAST"}, empty: "No readings in this period."}
			if len(m.Points) > 0 {
				for _, cl := range cols {
					var series []*float64
					for _, p := range m.Points {
						series = append(series, cl.get(p))
					}
					l.add(cl.name, cl.name, cl.fmt(avg(series)), cl.fmt(peak(series)), cl.fmt(last(series)))
				}
			}
			fmt.Fprintf(a.Out, "Instance %s, last %s (%d readings):\n", instanceLabel(in), timeframe, len(m.Points))
			return a.printList(m, l)
		},
	}
	cmd.Flags().StringVar(&timeframe, "timeframe", "hour", "hour, day, week, month or year")
	cmd.Flags().BoolVar(&points, "points", false, "every reading, not the summary")
	return cmd
}

func pct(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *p)
}

// bps is a rate in bytes per second, the way people read it.
func bps(p *float64) string {
	if p == nil {
		return "—"
	}
	return bytesText(strconv.FormatFloat(*p, 'f', 0, 64)) + "/s"
}

// memPct is a reading's memory in use, as a share of what the instance has.
func memPct(p api.MetricPoint) *float64 {
	if p.MemBytes == nil || p.MemMaxBytes == nil || *p.MemMaxBytes <= 0 {
		return nil
	}
	v := *p.MemBytes / *p.MemMaxBytes * 100
	return &v
}

func pointMemText(p api.MetricPoint) string {
	if p.MemBytes == nil {
		return "—"
	}
	used := bytesText(strconv.FormatFloat(*p.MemBytes, 'f', 0, 64))
	if p.MemMaxBytes == nil {
		return used
	}
	return used + " of " + bytesText(strconv.FormatFloat(*p.MemMaxBytes, 'f', 0, 64))
}

func instanceBandwidthCmd(a *App) *cobra.Command {
	var period string
	var points bool
	cmd := &cobra.Command{
		Use:   "bandwidth <instance>",
		Short: "Traffic in and out — this billing cycle (against what the plan includes), or the last day to year",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch period {
			case "current_cycle", "cycle", "day", "week", "month", "year":
			default:
				return errors.New("--period is cycle, day, week, month or year")
			}
			if period == "cycle" {
				period = "current_cycle"
			}
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var b api.InstanceBandwidth
			if err := c.Do(cmd.Context(), "GET", "/instances/"+in.ID+"/bandwidth", url.Values{"period": {period}}, nil, &b); err != nil {
				return notOffered(err, "Instance bandwidth")
			}
			if a.JSON {
				return a.encode(b)
			}
			gb := func(v float64) string { return bytesText(strconv.FormatFloat(v, 'f', 0, 64)) }
			if points {
				l := listing{headers: []string{"TIME", "IN", "OUT"}, empty: "No traffic in this period."}
				for _, p := range b.Series {
					l.add(p.Time, when(p.Time), gb(p.InboundBytes), gb(p.OutboundBytes))
				}
				return a.printList(b, l)
			}
			var d detail
			d.add("Instance", instanceLabel(in))
			d.add("Period", fmt.Sprintf("%s – %s", when(b.StartsAt), when(b.EndsAt)))
			d.add("In", gb(b.TotalInboundBytes))
			d.add("Out", gb(b.TotalOutboundBytes))
			if cy := b.Cycle; cy != nil {
				included := cy.IncludedGB * 1e9
				share := ""
				if included > 0 {
					share = fmt.Sprintf(" (%.0f%%)", cy.PublicOutboundBytes/included*100)
				}
				d.add("Cycle transfer", fmt.Sprintf("%s of %g GB included%s, until %s", gb(cy.PublicOutboundBytes), cy.IncludedGB, share, when(cy.EndsAt)))
			}
			return a.printDetail(b, d)
		},
	}
	cmd.Flags().StringVar(&period, "period", "current_cycle", "cycle (the billing cycle), day, week, month or year")
	cmd.Flags().BoolVar(&points, "points", false, "the traffic step by step, not the totals")
	return cmd
}

// blockerText is why the API would refuse to delete an instance now.
func blockerText(codes []string) string {
	texts := map[string]string{
		"pending_move":       "a move between subscriptions is pending",
		"pending_transfer":   "an account transfer is pending",
		"pending_conversion": "a subscription conversion is pending",
	}
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, firstNonEmpty(texts[c], c))
	}
	return strings.Join(out, "; ")
}
