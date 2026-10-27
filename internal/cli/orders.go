package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// maxUserData is the most cloud-config an instance takes (16 KiB).
const maxUserData = 16 * 1024

// orderFlags is everything an instance is ordered with. The quote takes the
// configuration; the order takes the rest too (payment, name, password…).
type orderFlags struct {
	plan, image, customImage, region, term                  string
	subscription, payment, initialCredit, coupon, trialCode string
	name, password, vpc, project, userData                  string
	byoISO, private, passwordStdin                          bool
	diskExtraGB                                             int
	extraDisks                                              []int
	addons, appInputs, sshKeys                              []string
}

func (o *orderFlags) registerConfig(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.plan, "plan", "", "plan code (`mw plans`)")
	f.StringVar(&o.image, "image", "", "operating system or app code (`mw images`)")
	f.StringVar(&o.customImage, "custom-image", "", "one of your own disk images, by name or id, instead of --image (`mw custom-images`)")
	f.BoolVar(&o.byoISO, "byo-iso", false, "an empty disk, to install from your own ISO, instead of --image")
	f.StringVar(&o.region, "region", "", "region id, name or code (`mw regions`); the only one when there is one")
	f.StringVar(&o.term, "term", "monthly", "billing term: monthly, quarterly, semi-annual, annual or on-demand (hourly, prepaid)")
	f.IntVar(&o.diskExtraGB, "disk-extra-gb", 0, "GB on the main disk beyond the plan's")
	f.IntSliceVar(&o.extraDisks, "extra-disk", nil, "an extra disk of this many GB (repeat for more)")
	f.StringSliceVar(&o.addons, "addon", nil, "an add-on code, or code=quantity (`mw addons`; repeat for more)")
}

func (o *orderFlags) registerOrder(cmd *cobra.Command) {
	o.registerConfig(cmd)
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "", "the instance's name")
	f.StringVar(&o.password, "password", "", "root/Administrator password (prefer --password-stdin: a flag is seen by other users of this computer)")
	f.BoolVar(&o.passwordStdin, "password-stdin", false, "read the password from standard input")
	f.StringSliceVar(&o.sshKeys, "ssh-key", nil, "an SSH key to put on it: id, name, fingerprint or a .pub file (repeat for more; Linux images)")
	f.StringVar(&o.userData, "user-data", "", "cloud-config to run on first boot: @file, - for standard input, or the YAML itself (≤ 16 KiB; Linux images)")
	f.StringSliceVar(&o.appInputs, "app-input", nil, "an answer to the app's setup questions, key=value (repeat for more; `mw images` → inputs)")
	f.StringVar(&o.vpc, "vpc", "", "attach it to this VPC (id or name)")
	f.BoolVar(&o.private, "private", false, "private only: no public IP (needs --vpc)")
	f.StringVar(&o.project, "project", "", "put the new instance in this project (name or id)")
	f.StringVar(&o.subscription, "subscription", "", "add it to this subscription (its term and balance) instead of starting a new one")
	f.StringVar(&o.payment, "payment", "", "auto (default), balance (never invoice: fail when short) or invoice")
	f.StringVar(&o.initialCredit, "initial-credit", "", "new on-demand subscription: prepaid credit in reais, e.g. 100 (default: the minimum)")
	f.StringVar(&o.coupon, "coupon", "", "coupon code (a new committed subscription)")
	f.StringVar(&o.trialCode, "trial-code", "", "redeem a trial offer")
}

// imageBody is the order's image.
func (o *orderFlags) imageBody() (map[string]interface{}, error) {
	set := 0
	for _, b := range []bool{o.image != "", o.customImage != "", o.byoISO} {
		if b {
			set++
		}
	}
	switch {
	case set == 0:
		return nil, errors.New("what does it start from? pass --image (`mw images`), --custom-image or --byo-iso")
	case set > 1:
		return nil, errors.New("--image, --custom-image and --byo-iso: one of them")
	case o.customImage != "":
		return map[string]interface{}{"type": "client_image", "image_id": o.customImage}, nil
	case o.byoISO:
		return map[string]interface{}{"type": "byo_iso"}, nil
	default:
		return map[string]interface{}{"type": "template", "code": o.image}, nil
	}
}

// configBody is the quote's body: the configuration that is priced.
func (a *App) configBody(ctx context.Context, o *orderFlags, regionID string) (map[string]interface{}, error) {
	if o.plan == "" {
		return nil, errors.New("which plan? pass --plan (`mw plans`)")
	}
	term, err := parseTerm(o.term)
	if err != nil {
		return nil, err
	}
	img, err := o.imageBody()
	if err != nil {
		return nil, err
	}
	if o.customImage != "" {
		// By name as well as id.
		id, err := a.customImageID(ctx, o.customImage)
		if err != nil {
			return nil, err
		}
		img["image_id"] = id
	}
	body := map[string]interface{}{"offering_code": o.plan, "term": term, "image": img}
	if o.diskExtraGB > 0 {
		body["main_disk_extra_gb"] = o.diskExtraGB
	}
	if len(o.extraDisks) > 0 {
		var disks []map[string]int
		for _, gb := range o.extraDisks {
			if gb <= 0 {
				return nil, errors.New("--extra-disk takes a size in GB")
			}
			disks = append(disks, map[string]int{"size_gb": gb})
		}
		body["extra_disks"] = disks
	}
	if len(o.addons) > 0 {
		var addons []map[string]interface{}
		for _, raw := range splitList(o.addons) {
			code, qty, hasQty := strings.Cut(raw, "=")
			ad := map[string]interface{}{"code": strings.TrimSpace(code)}
			if hasQty {
				n, err := strconv.Atoi(strings.TrimSpace(qty))
				if err != nil || n <= 0 {
					return nil, fmt.Errorf("--addon %s: the quantity is a whole number", raw)
				}
				ad["quantity"] = n
			}
			addons = append(addons, ad)
		}
		body["addons"] = addons
	}
	if regionID != "" {
		n, err := strconv.Atoi(regionID)
		if err != nil {
			return nil, fmt.Errorf("region id %q is not a number", regionID)
		}
		body["region_id"] = n
	}
	return body, nil
}

// orderBody is the order's body: the configuration and how it is bought.
func (a *App) orderBody(ctx context.Context, o *orderFlags) (map[string]interface{}, error) {
	region, err := a.resolveRegion(ctx, o.region)
	if err != nil {
		return nil, err
	}
	body, err := a.configBody(ctx, o, region.ID)
	if err != nil {
		return nil, err
	}
	if o.trialCode != "" {
		if o.subscription != "" || o.payment != "" || o.coupon != "" || o.initialCredit != "" {
			return nil, errors.New("--trial-code goes alone: no --subscription, --payment, --coupon or --initial-credit")
		}
		body["trial_code"] = o.trialCode
	}
	if o.subscription != "" {
		body["subscription_id"] = o.subscription
	}
	switch o.payment {
	case "":
	case "auto", "balance", "invoice":
		body["payment"] = o.payment
	default:
		return nil, errors.New("--payment is auto, balance or invoice")
	}
	if o.initialCredit != "" {
		v, err := parseReais(o.initialCredit)
		if err != nil {
			return nil, fmt.Errorf("--initial-credit: %w", err)
		}
		body["initial_credit_cents"] = strconv.FormatInt(v, 10)
	}
	if o.coupon != "" {
		body["coupon_code"] = o.coupon
	}
	if o.name != "" {
		body["name"] = o.name
	}
	password := o.password
	if o.passwordStdin {
		line, err := bufio.NewReader(a.In).ReadString('\n')
		if err != nil && line == "" {
			return nil, errors.New("--password-stdin: nothing on standard input")
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if password != "" {
		body["password"] = password
	}
	if o.vpc != "" || o.private {
		if o.vpc == "" {
			return nil, errors.New("--private needs --vpc: a private instance is reached through its VPC")
		}
		v, err := a.resolveVPC(ctx, o.vpc)
		if err != nil {
			return nil, err
		}
		n, _ := strconv.Atoi(v.ID)
		mode := "public"
		if o.private {
			mode = "private"
		}
		body["networking"] = map[string]interface{}{"mode": mode, "vpc_id": n}
	}
	if len(o.appInputs) > 0 {
		inputs := map[string]interface{}{}
		for _, kv := range o.appInputs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("--app-input %q: key=value", kv)
			}
			inputs[strings.TrimSpace(k)] = v
		}
		body["app_inputs"] = inputs
	}
	if o.project != "" {
		p, err := a.resolveProject(ctx, o.project)
		if err != nil {
			return nil, err
		}
		body["project_id"] = p.ID
	}
	if len(o.sshKeys) > 0 {
		ids, err := a.resolveSSHKeys(ctx, o.sshKeys)
		if err != nil {
			return nil, err
		}
		body["ssh_key_ids"] = ids
	}
	if o.userData != "" {
		ud, err := readUserData(a.In, o.userData)
		if err != nil {
			return nil, err
		}
		body["user_data"] = ud
	}
	return body, nil
}

// readUserData takes @file, - (standard input) or the text itself.
func readUserData(in io.Reader, v string) (string, error) {
	var raw []byte
	var err error
	switch {
	case v == "-":
		raw, err = io.ReadAll(io.LimitReader(in, maxUserData+1))
	case strings.HasPrefix(v, "@"):
		raw, err = os.ReadFile(expandHome(v[1:]))
	default:
		raw = []byte(v)
	}
	if err != nil {
		return "", fmt.Errorf("--user-data: %w", err)
	}
	if len(raw) > maxUserData {
		return "", fmt.Errorf("--user-data is %d bytes: the most is %d (16 KiB)", len(raw), maxUserData)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", errors.New("--user-data is empty")
	}
	return string(raw), nil
}

// quote prices a configuration.
func (a *App) quote(ctx context.Context, body map[string]interface{}) (*api.Quote, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	q := map[string]interface{}{}
	for _, k := range []string{"offering_code", "term", "image", "main_disk_extra_gb", "extra_disks", "addons", "region_id"} {
		if v, ok := body[k]; ok {
			q[k] = v
		}
	}
	var out api.Quote
	if err := c.Do(ctx, "POST", "/orders/quote", nil, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// printQuote shows what a configuration costs, line by line.
func (a *App) printQuote(w io.Writer, q *api.Quote) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Plan %s, %s:\n", q.OfferingCode, termText(q.Term))
	for _, li := range q.LineItems {
		cycle := ""
		if li.CycleCents != nil {
			cycle = cents(*li.CycleCents)
		}
		qty := ""
		if li.Quantity != 1 {
			qty = fmt.Sprintf(" ×%g", li.Quantity)
		}
		note := ""
		if li.RejectedReason != nil {
			note = "  (" + *li.RejectedReason + ")"
		}
		fmt.Fprintf(tw, "  %s%s\t%s\t%s%s\n", li.Label, qty, perHour(&li.RateMicroCentsPerHour), cycle, note)
	}
	tw.Flush()
	rate := q.HourlyRateMicroCents
	fmt.Fprintf(w, "Total: %s (about %s)", perHour(&rate), perMonth(&rate))
	if q.TermTotalCents != nil {
		fmt.Fprintf(w, "; %s for the %s term", cents(*q.TermTotalCents), termText(q.Term))
	}
	fmt.Fprintln(w, ".")
	for _, v := range q.Violations {
		fmt.Fprintf(w, "  ✗ the image needs %s %g (this has %g): %s\n", strings.TrimPrefix(v.Kind, "min_"), v.Required, v.Actual, strings.ReplaceAll(v.Remedy, "_", " "))
	}
	for _, warn := range q.Warnings {
		fmt.Fprintf(w, "  ! %s\n", warn)
	}
}

func ordersCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "orders",
		Aliases: []string{"order"},
		Short:   "Price a configuration, or see where an order stands",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var o orderFlags
	quote := &cobra.Command{
		Use:     "quote",
		Short:   "What a configuration costs, without ordering it",
		Example: "  mw orders quote --plan c2m4 --image ubuntu-24-04 --term annual",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			regionID := ""
			if o.region != "" {
				r, err := a.resolveRegion(cmd.Context(), o.region)
				if err != nil {
					return err
				}
				regionID = r.ID
			}
			body, err := a.configBody(cmd.Context(), &o, regionID)
			if err != nil {
				return err
			}
			q, err := a.quote(cmd.Context(), body)
			if err != nil {
				return err
			}
			if a.JSON {
				return a.encode(q)
			}
			a.printQuote(a.Out, q)
			if !q.Purchasable {
				return errors.New("this configuration cannot be ordered as it is (see above)")
			}
			return nil
		},
	}
	o.registerConfig(quote)

	get := &cobra.Command{
		Use:   "get <order-id>",
		Short: "Where an order stands: payment, provisioning, the instances it made",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var ord api.Order
			if err := c.Do(cmd.Context(), "GET", "/orders/"+args[0], nil, nil, &ord); err != nil {
				return err
			}
			return a.printDetail(ord, orderDetail(ord))
		},
	}
	cmd.AddCommand(quote, get)
	return cmd
}

func orderDetail(o api.Order) detail {
	var d detail
	d.add("Order", orDashPtr(o.ID))
	d.add("Status", strings.ReplaceAll(o.Status, "_", " "))
	if o.SubscriptionID != nil {
		d.add("Subscription", *o.SubscriptionID)
	}
	if len(o.InstanceIDs) > 0 {
		d.add("Instances", strings.Join(o.InstanceIDs, ", "))
	}
	if p := o.Payment; p != nil {
		d.add("Invoice", fmt.Sprintf("%s · %s · %s", p.InvoiceID, cents(p.AmountCents), strings.ToLower(orDashPtr(p.Status))))
		if p.PayURL != nil {
			d.add("Pay at", *p.PayURL)
		}
		if p.BalanceAppliedCents != nil {
			d.add("Balance used", cents(*p.BalanceAppliedCents))
		}
	}
	if o.ExpiresAt != nil {
		d.add("Expires", whenPtr(o.ExpiresAt))
	}
	return d
}
