package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// Resizing an instance by plan, the way DigitalOcean sizes and Vultr plans
// work: `resize-options` lists the plans it can move to with their prices,
// `resize` prices the change (the API's quote changes nothing), asks, and
// applies it.

func (a *App) resizeOptions(ctx context.Context, in *api.Instance) (*api.ResizeOptions, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var o api.ResizeOptions
	if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/resize-options", nil, nil, &o); err != nil {
		return nil, notOffered(err, "Resizing from the API")
	}
	return &o, nil
}

func instanceResizeOptionsCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "resize-options <instance>",
		Aliases: []string{"sizes"},
		Short:   "The plans an instance can move to, with prices, and its add-ons and disks",
		Example: "  mw instances resize-options web-1\n" +
			"  mw instances resize-options web-1 -q       # the plan codes alone",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			o, err := a.resizeOptions(cmd.Context(), in)
			if err != nil {
				return err
			}
			if a.JSON {
				return a.encode(o)
			}
			if a.Quiet {
				for _, off := range o.Offerings {
					fmt.Fprintln(a.Out, off.Code)
				}
				return nil
			}
			return a.printResizeOptions(in, o)
		},
	}
}

func (a *App) printResizeOptions(in *api.Instance, o *api.ResizeOptions) error {
	cur := o.Current
	rate := cur.HourlyRateMicroCents
	billing := "on demand"
	if o.CommitmentType == "COMMITTED" {
		billing = "committed"
		if o.Term != nil {
			billing += " (" + termText(*o.Term) + ")"
		}
		if o.CycleEndsAt != nil {
			billing += " until " + whenPtr(o.CycleEndsAt)
		}
	}
	fmt.Fprintf(a.Out, "%s: plan %s (%s), %s (%s), %s.\n", instanceLabel(in), cur.OfferingCode,
		planSpecs(cur.VCPU, cur.RAMMB, cur.MainDiskGB), perHour(&rate), perMonth(&rate), billing)
	if !o.Resizable {
		fmt.Fprintf(a.Out, "It cannot be resized now: %s.\n", strings.TrimSuffix(firstNonEmpty(str(o.Reason), "the API gave no reason"), "."))
	}
	if !cur.OfferingOnSale {
		fmt.Fprintln(a.Out, "Its plan is no longer sold: it can be kept, not picked again.")
	}
	if cur.RateIsGrandfathered {
		fmt.Fprintln(a.Out, "Its rate is from an older price list: changing the plan re-prices it at today's.")
	}
	if o.CommitmentType == "COMMITTED" {
		fmt.Fprintln(a.Out, "On a commitment an upgrade is charged now for the rest of the cycle; a downgrade comes back as credit.")
	}

	fmt.Fprintln(a.Out)
	if len(o.Offerings) == 0 {
		fmt.Fprintln(a.Out, "No other plan is available for it now.")
	} else {
		w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
		if !a.NoHeaders {
			fmt.Fprintln(w, "PLAN\tNAME\tVCPU\tRAM\tDISK\tIPV4\tPER HOUR\tPER MONTH\t")
		}
		for _, off := range o.Offerings {
			r := off.HourlyRateMicroCents
			mark := ""
			if off.Current {
				mark = "← current"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s GB\t%s\t%s\t%s\t%s\n", off.Code, orDash(off.Name), fnum(off.VCPU),
				strings.TrimSuffix(ramText(off.RAMMB), " RAM"), fnum(off.MainDiskBaseGB), fnum(off.IncludedIPv4),
				strings.TrimSuffix(perHour(&r), "/h"), strings.TrimSuffix(perMonth(&r), "/mo"), mark)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}

	if len(o.Addons) > 0 {
		fmt.Fprintln(a.Out)
		w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
		if !a.NoHeaders {
			fmt.Fprintln(w, "ADD-ON\tNAME\tNOW\tALLOWED\tPER HOUR")
		}
		for _, ad := range o.Addons {
			r := ad.HourlyRateMicroCents
			price := strings.TrimSuffix(perHour(&r), "/h") + " each"
			if ad.QuantityFollowsVCPU {
				price = strings.TrimSuffix(perHour(&r), "/h") + " per vCPU"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ad.Code, orDash(ad.Name), addonNow(ad), addonRange(ad), price)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if !o.Capabilities.Addons {
		fmt.Fprintln(a.Out, "Its add-ons cannot change now.")
	}
	if len(o.ExtraDisks) > 0 {
		labels := make([]string, 0, len(o.ExtraDisks))
		for _, d := range o.ExtraDisks {
			labels = append(labels, firstNonEmpty(d.Label, d.ID))
		}
		fmt.Fprintf(a.Out, "\nExtra disks: %s.\n", strings.Join(labels, ", "))
	}
	if !o.Capabilities.Storage {
		fmt.Fprintln(a.Out, "Its disks cannot change now.")
	}
	fmt.Fprintf(a.Out, "\nResize with `mw instances resize %s --plan <code>`: the price is shown before anything changes.\n", in.ID)
	return nil
}

func instanceResizeCmd(a *App) *cobra.Command {
	var plan string
	var diskExtra int
	var addons []string
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "resize <instance>",
		Short: "Move an instance to another plan, grow its disk or change add-ons (priced before it applies)",
		Long: "Resize an instance by plan: `mw instances resize-options` lists the plans with their prices.\n" +
			"The change is priced first: the new rate, what a commitment charges now for the rest of the\n" +
			"cycle or gives back as credit, and whether it restarts. A CPU, memory or disk change powers it\n" +
			"off and on again; a grown disk needs its partition grown inside the guest afterwards.",
		Example: "  mw instances resize web-1 --plan c4m8\n" +
			"  mw instances resize web-1 --disk-extra-gb 50 --yes --wait\n" +
			"  mw instances resize web-1 --addon backup-slot=2",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			diskSet := cmd.Flags().Changed("disk-extra-gb")
			if plan == "" && !diskSet && len(addons) == 0 {
				return errors.New("pass --plan, --disk-extra-gb or --addon (`mw instances resize-options <instance>` lists what it can become)")
			}
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			o, err := a.resizeOptions(ctx, in)
			if err != nil {
				return err
			}
			if !o.Resizable {
				return fmt.Errorf("%s cannot be resized now: %s", instanceLabel(in), firstNonEmpty(str(o.Reason), "the API gave no reason"))
			}
			body, target, changes, err := resizeBody(o, plan, diskSet, diskExtra, addons)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var q api.ResizeQuote
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/resize/quote", nil, body, &q); err != nil {
				return err
			}
			if !q.HasChanges {
				if a.JSON {
					return a.encode(q)
				}
				fmt.Fprintf(a.Out, "Nothing to change: %s already has that configuration.\n", instanceLabel(in))
				return nil
			}
			if !a.JSON && !a.Quiet {
				if err := a.printResizeQuote(o, target, changes, q); err != nil {
					return err
				}
			}
			if !q.CanApply {
				if a.JSON {
					if err := a.encode(q); err != nil {
						return err
					}
				}
				return resizeRefused(in, q)
			}
			if !yes {
				// Losing an address or a disk is not undone: the id is typed.
				if len(q.Removes) > 0 {
					prompt := fmt.Sprintf("This resize removes %s for good, with what is on it.\nType the instance id (%s) to confirm: ",
						plural(len(q.Removes), "address or disk", "addresses or disks"), in.ID)
					if err := a.typeToConfirm(prompt, in.ID, "--yes"); err != nil {
						return err
					}
				} else {
					ok, err := a.confirm(fmt.Sprintf("Resize %s? [y/N] ", instanceLabel(in)), "--yes")
					if err != nil || !ok {
						if err == nil {
							a.printf("Nothing changed.\n")
						}
						return err
					}
				}
			}
			var acc api.ResizeAccepted
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/resize", nil, body, &acc); err != nil {
				var p *api.Problem
				if errors.As(err, &p) && p.Code == "insufficient_balance" {
					return fmt.Errorf("nothing changed: %s", topUpFor(in, p.SubscriptionID, p.ShortfallCents.String()))
				}
				return err
			}
			if wait {
				if err := a.waitAccepted(ctx, acc.OperationAccepted, timeout, func(ctx context.Context) error {
					_, err := a.waitInstance(ctx, in.ID, timeout, "idle", func(s api.InstanceStatus) bool { return !s.Busy })
					return err
				}); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			if a.Quiet {
				if acc.OperationID != nil {
					fmt.Fprintln(a.Out, *acc.OperationID)
				}
				return nil
			}
			what := "Resize of " + instanceLabel(in)
			if target != nil && target.Code != o.Current.OfferingCode {
				what += " to " + target.Code
			}
			if wait {
				fmt.Fprintf(a.Out, "%s: done.\n", what)
			} else {
				fmt.Fprintln(a.Out, acceptedText(acc.OperationAccepted, what))
			}
			for _, line := range resizeAfter(acc) {
				fmt.Fprintln(a.Out, line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&plan, "plan", "", "the plan's code from resize-options (default: the current plan, to change only the disk or add-ons)")
	cmd.Flags().IntVar(&diskExtra, "disk-extra-gb", 0, "GB on the main disk beyond the plan's base, in all (the disk only grows)")
	cmd.Flags().StringArrayVar(&addons, "addon", nil, "code=quantity, repeatable: 0 removes it; on or off for one priced per vCPU")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "resize without asking")
	waitFlags(cmd, &wait, &timeout, 30*time.Minute)
	return cmd
}

// resizeBody is the resize request: the plan (the current one unless --plan
// names another) and only what the flags change. The API refuses a field it
// does not declare, and an extra_disks list would replace the disks, so it is
// never sent. changes says what moves, for the quote.
func resizeBody(o *api.ResizeOptions, plan string, diskSet bool, diskExtra int, addons []string) (map[string]interface{}, *api.ResizeOffering, []string, error) {
	code := o.Current.OfferingCode
	if plan != "" {
		code = plan
	}
	var target *api.ResizeOffering
	for i := range o.Offerings {
		if strings.EqualFold(o.Offerings[i].Code, code) {
			target = &o.Offerings[i]
		}
	}
	switch {
	case target != nil:
		code = target.Code
	case !strings.EqualFold(code, o.Current.OfferingCode):
		codes := make([]string, 0, len(o.Offerings))
		for _, off := range o.Offerings {
			codes = append(codes, off.Code)
		}
		return nil, nil, nil, fmt.Errorf("no plan %s for this instance; it can move to: %s", plan, orDash(strings.Join(codes, ", ")))
	}
	body := map[string]interface{}{"offering_code": code}
	var changes []string
	if diskSet {
		if diskExtra < 0 {
			return nil, nil, nil, errors.New("--disk-extra-gb cannot be negative")
		}
		body["main_disk_extra_gb"] = diskExtra
		if target != nil {
			changes = append(changes, fmt.Sprintf("main disk %s → %s GB", fnum(o.Current.MainDiskGB), fnum(target.MainDiskBaseGB+float64(diskExtra))))
		}
	}
	if len(addons) > 0 {
		list := make([]map[string]interface{}, 0, len(addons))
		for _, spec := range addons {
			name, value, ok := strings.Cut(spec, "=")
			name, value = strings.TrimSpace(name), strings.TrimSpace(value)
			if !ok || name == "" || value == "" {
				return nil, nil, nil, fmt.Errorf("--addon %q: write code=quantity, like backup-slot=2", spec)
			}
			var ad *api.ResizeAddon
			for i := range o.Addons {
				if strings.EqualFold(o.Addons[i].Code, name) {
					ad = &o.Addons[i]
				}
			}
			if ad == nil {
				codes := make([]string, 0, len(o.Addons))
				for _, x := range o.Addons {
					codes = append(codes, x.Code)
				}
				return nil, nil, nil, fmt.Errorf("no add-on %s for this instance; it can have: %s", name, orDash(strings.Join(codes, ", ")))
			}
			qty, err := addonQuantity(ad, value)
			if err != nil {
				return nil, nil, nil, err
			}
			list = append(list, map[string]interface{}{"code": ad.Code, "quantity": qty})
			next := strconv.Itoa(qty)
			if ad.QuantityFollowsVCPU {
				next = map[bool]string{true: "on", false: "off"}[qty > 0]
			}
			changes = append(changes, fmt.Sprintf("%s %s → %s", ad.Code, addonNow(*ad), next))
		}
		body["addons"] = list
	}
	return body, target, changes, nil
}

// addonQuantity reads an add-on's wanted quantity: a whole number, or on or
// off for one priced per vCPU (the API sets that one's quantity from the plan).
func addonQuantity(ad *api.ResizeAddon, v string) (int, error) {
	switch strings.ToLower(v) {
	case "on", "yes":
		return 1, nil
	case "off", "no":
		return 0, nil
	}
	if ad.QuantityFollowsVCPU {
		return 0, fmt.Errorf("%s follows the plan's vCPU count: set it on or off", ad.Code)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--addon %s=%s: the quantity is a whole number (0 removes it)", ad.Code, v)
	}
	return n, nil
}

func (a *App) printResizeQuote(o *api.ResizeOptions, target *api.ResizeOffering, changes []string, q api.ResizeQuote) error {
	var d detail
	from := o.Current.OfferingCode
	if target != nil && target.Code != from {
		d.add("Plan", fmt.Sprintf("%s → %s (%s)", from, target.Code, planSpecs(target.VCPU, target.RAMMB, target.MainDiskBaseGB)))
	} else {
		d.add("Plan", from+" (unchanged)")
	}
	for _, ch := range changes {
		d.add("Change", ch)
	}
	cur, next := q.CurrentHourlyRateMicroCents, q.NewHourlyRateMicroCents
	d.add("Price", fmt.Sprintf("%s → %s (%s → %s)", perHour(&cur), perHour(&next), perMonth(&cur), perMonth(&next)))
	if q.NewCycleCents != nil {
		d.add("Per cycle", cents(*q.NewCycleCents))
	}
	if reaisArg(q.ChargeNowCents) != "" {
		d.add("Charged now", cents(q.ChargeNowCents)+", for the rest of this cycle")
	}
	if reaisArg(q.CreditCents) != "" {
		d.add("Credit", cents(q.CreditCents)+" back, for the rest of this cycle")
	}
	if q.RequiresRestart {
		d.add("Restart", "yes: it powers off and on again")
	} else {
		d.add("Restart", "no")
	}
	for _, r := range q.Removes {
		d.after = append(d.after, fmt.Sprintf("Removes %s %s: %s", removedKind(r.Type), r.Label, r.Warning))
	}
	for _, w := range q.Warnings {
		d.after = append(d.after, "Note: "+w.Message)
	}
	for _, v := range q.Violations {
		d.after = append(d.after, "Not allowed: "+v.Message)
	}
	if q.ShortfallCents != nil && reaisArg(*q.ShortfallCents) != "" {
		d.after = append(d.after, "The subscription's balance is short by "+cents(*q.ShortfallCents)+".")
	}
	return a.printDetail(q, d)
}

// resizeRefused says why a quoted resize cannot be applied, and — when only
// the balance stands in the way — how to top it up.
func resizeRefused(in *api.Instance, q api.ResizeQuote) error {
	short := q.ShortfallCents != nil && reaisArg(*q.ShortfallCents) != ""
	if len(q.Violations) == 0 && short {
		return fmt.Errorf("nothing changed: %s", topUpFor(in, str(q.SubscriptionID), *q.ShortfallCents))
	}
	why := make([]string, 0, len(q.Violations)+1)
	for _, v := range q.Violations {
		why = append(why, v.Message)
	}
	if short {
		why = append(why, "the subscription's balance is short by "+cents(*q.ShortfallCents))
	}
	if len(why) == 0 {
		why = append(why, "the API gave no reason")
	}
	return fmt.Errorf("%s cannot be resized like that:\n  %s", instanceLabel(in), strings.Join(why, "\n  "))
}

// topUpFor is what is missing and the command that adds it, on the
// subscription the answer named or, failing that, the instance's own.
func topUpFor(in *api.Instance, subscription, shortfallCents string) string {
	return topUpLine(firstNonEmpty(subscription, str(in.SubscriptionID)), shortfallCents)
}

// resizeAfter is what an accepted resize leaves to know or to do.
func resizeAfter(acc api.ResizeAccepted) []string {
	var out []string
	if acc.NewHourlyRateMicroCents != nil {
		out = append(out, fmt.Sprintf("New price: %s (%s).", perHour(acc.NewHourlyRateMicroCents), perMonth(acc.NewHourlyRateMicroCents)))
	}
	if reaisArg(acc.CreditCents) != "" {
		out = append(out, cents(acc.CreditCents)+" comes back to the subscription as credit.")
	}
	if p := acc.Planned; p != nil {
		if p.MainDiskGrowGB > 0 {
			out = append(out, fmt.Sprintf("The main disk grows by %s GB: grow the partition and file system inside the guest afterwards (growpart and resize2fs or xfs_growfs; Disk Management on Windows).", fnum(p.MainDiskGrowGB)))
		}
		if p.RebootRequired {
			out = append(out, "Reboot it (or renew its DHCP lease) for the guest to pick up its new main IPv4.")
		}
	}
	return out
}

// planSpecs is "2 vCPU · 4 GB RAM · 80 GB disk".
func planSpecs(vcpu, ramMB, diskGB float64) string {
	return fmt.Sprintf("%s vCPU · %s · %s GB disk", fnum(vcpu), ramText(ramMB), fnum(diskGB))
}

func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func addonNow(ad api.ResizeAddon) string {
	if ad.QuantityFollowsVCPU {
		return map[bool]string{true: "on", false: "off"}[ad.CurrentQuantity > 0]
	}
	return fnum(ad.CurrentQuantity)
}

func addonRange(ad api.ResizeAddon) string {
	if ad.QuantityFollowsVCPU {
		return "on or off"
	}
	r := fnum(ad.MinQuantity) + " or more"
	if ad.MaxQuantity != nil {
		r = fnum(ad.MinQuantity) + "–" + fnum(*ad.MaxQuantity)
	}
	if ad.QuantityStep > 1 {
		r += ", step " + fnum(ad.QuantityStep)
	}
	if !ad.Removable {
		r += ", only up"
	}
	return r
}

func removedKind(t string) string {
	switch t {
	case "ipv4":
		return "IPv4 address"
	case "ipv6":
		return "IPv6 address"
	case "extra_disk":
		return "disk"
	}
	return t
}
