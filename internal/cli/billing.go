package cli

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// subscriptionsCmd: what instances are billed under — committed (monthly,
// annual…) or on-demand (prepaid credit, hourly) — and their balances.
func subscriptionsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subscriptions",
		Aliases: []string{"subscription", "subs"},
		Short:   "Subscriptions instances are billed under, and their balances",
		Example: "  mw subscriptions\n" +
			"  mw subscriptions balance 0193f0e2\n" +
			"  mw subscriptions top-up 0193f0e2 --amount 200",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Subscription](cmd.Context(), c, "/subscriptions", nil)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "STATUS", "KIND", "TERM", "CYCLE ENDS", "USABLE"}, empty: "No subscriptions yet: ordering an instance starts one."}
			for _, s := range all {
				usable := "—"
				if s.Balance != nil {
					usable = cents(s.Balance.UsableCents)
				}
				kind := "committed"
				if s.CommitmentType == "ON_DEMAND" {
					kind = "on-demand"
				}
				l.add(s.ID, s.ID, orDashPtr(s.Name), s.Status, kind, termText(str(s.Term)), whenPtr(s.CurrentCycleEndsAt), usable)
			}
			return a.printList(all, l)
		},
	}

	get := &cobra.Command{
		Use:   "get <subscription-id>",
		Short: "A subscription: its term, cycle and balance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var s api.Subscription
			if err := c.Do(cmd.Context(), "GET", "/subscriptions/"+args[0], nil, nil, &s); err != nil {
				return err
			}
			var d detail
			d.add("ID", s.ID)
			d.add("Name", orDashPtr(s.Name))
			d.add("Status", s.Status)
			d.add("Kind", strings.ToLower(strings.ReplaceAll(s.CommitmentType, "_", "-")))
			if s.Term != nil {
				d.add("Term", termText(*s.Term))
			}
			if s.CurrentCycleStartsAt != nil || s.CurrentCycleEndsAt != nil {
				d.add("Cycle", fmt.Sprintf("%s – %s", whenPtr(s.CurrentCycleStartsAt), whenPtr(s.CurrentCycleEndsAt)))
			}
			if b := s.Balance; b != nil {
				d.add("Usable", cents(b.UsableCents))
			}
			if s.Trial != nil {
				d.add("Trial ends", whenPtr(s.Trial.TrialEndsAt))
			}
			return a.printDetail(s, d)
		},
	}

	balance := &cobra.Command{
		Use:   "balance <subscription-id>",
		Short: "A subscription's credit: available, committed, temporary and owed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var b api.Balance
			if err := c.Do(cmd.Context(), "GET", "/subscriptions/"+args[0]+"/balance", nil, nil, &b); err != nil {
				return err
			}
			var d detail
			d.add("Usable", cents(b.UsableCents))
			d.add("Available", cents(b.AvailableCents))
			d.add("Committed", cents(b.CommittedCents))
			d.add("Temporary", cents(b.TemporaryCents)+" (expires at the end of the cycle; spent first)")
			d.add("Owed", cents(b.DebtCents))
			return a.printDetail(b, d)
		},
	}

	var limit int
	ledger := &cobra.Command{
		Use:   "ledger <subscription-id>",
		Short: "Every credit and debit of a subscription, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			entries, err := firstN[api.LedgerEntry](cmd.Context(), c, "/subscriptions/"+args[0]+"/ledger", limit)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"WHEN", "AMOUNT", "BUCKET", "REASON", "REFERENCE"}, empty: "Nothing yet."}
			for _, e := range entries {
				amount := cents(e.AmountCents)
				if e.IsCredit {
					amount = "+" + amount
				} else if !strings.HasPrefix(amount, "-") {
					amount = "-" + amount
				}
				l.add(e.ID, when(e.OccurredAt), amount, strings.ToLower(e.Bucket), strings.ToLower(strings.ReplaceAll(e.Reason, "_", " ")), orDashPtr(e.ReferenceID))
			}
			return a.printList(entries, l)
		},
	}
	ledger.Flags().IntVar(&limit, "limit", 50, "how many entries (0: all)")

	var amount, coupon string
	var yes bool
	topUp := &cobra.Command{
		Use:     "top-up <subscription-id>",
		Aliases: []string{"topup", "recharge"},
		Short:   "Add credit: raises an invoice to pay (Pix, boleto or card)",
		Example: "  mw subscriptions top-up 0193f0e2 --amount 200",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if amount == "" {
				return fmt.Errorf("how much? pass --amount in reais, e.g. --amount 200")
			}
			v, err := parseReais(amount)
			if err != nil {
				return fmt.Errorf("--amount: %w", err)
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Raise an invoice of %s to add credit to subscription %s? [y/N] ", cents(strconv.FormatInt(v, 10)), args[0]), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing raised.\n")
					}
					return err
				}
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"amount_cents": v}
			if coupon != "" {
				body["coupon_code"] = coupon
			}
			var t api.TopUp
			if err := c.Do(cmd.Context(), "POST", "/subscriptions/"+args[0]+"/top-ups", nil, body, &t); err != nil {
				return err
			}
			var d detail
			d.add("Invoice", t.InvoiceID)
			d.add("Amount", cents(t.AmountCents))
			if t.PayURL != nil {
				d.add("Pay at", *t.PayURL)
			}
			if t.DueAt != nil {
				d.add("Due", whenPtr(t.DueAt))
			}
			d.after = []string{"The credit is added once the invoice is paid."}
			return a.printDetail(t, d)
		},
	}
	topUp.Flags().StringVar(&amount, "amount", "", "how much, in reais (e.g. 200 or 49,90)")
	topUp.Flags().StringVar(&coupon, "coupon", "", "a top-up coupon")
	topUp.Flags().BoolVarP(&yes, "yes", "y", false, "raise the invoice without asking")

	cmd.AddCommand(get, balance, ledger, topUp)
	return cmd
}

func invoicesCmd(a *App) *cobra.Command {
	var limit int
	var open bool
	cmd := &cobra.Command{
		Use:     "invoices",
		Aliases: []string{"invoice"},
		Short:   "Invoices: what is open, overdue or paid",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := firstN[api.Invoice](cmd.Context(), c, "/invoices", limit)
			if err != nil {
				return err
			}
			var shown []api.Invoice
			for _, inv := range all {
				if open && inv.Status != "OPEN" {
					continue
				}
				shown = append(shown, inv)
			}
			l := listing{headers: []string{"ID", "AMOUNT", "STATUS", "PURPOSE", "DUE", "PAID"}, empty: "No invoices."}
			for _, inv := range shown {
				status := strings.ToLower(inv.Status)
				if inv.Overdue {
					status += " (overdue)"
				}
				l.add(inv.ID, firstNonEmpty(str(inv.ExternalID), inv.ID), cents(inv.AmountCents), status, strings.ToLower(strings.ReplaceAll(orDashPtr(inv.Purpose), "_", " ")), whenPtr(inv.DueAt), whenPtr(inv.PaidAt))
			}
			return a.printList(shown, l)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "how many (0: all)")
	cmd.Flags().BoolVar(&open, "open", false, "open invoices only")
	return cmd
}

// firstN reads a list up to n items (0: all), following its cursor.
// topUpLine is what to run when a subscription's balance does not cover
// something: the amount missing, as `mw subscriptions top-up --amount` takes it.
func topUpLine(subscription, shortfallCents string) string {
	sub := firstNonEmpty(subscription, "<subscription>")
	amount := reaisArg(shortfallCents)
	if amount == "" {
		return fmt.Sprintf("the subscription's balance does not cover it: add credit with `mw subscriptions top-up %s --amount <reais>`", sub)
	}
	line := fmt.Sprintf("the subscription's balance is short by %s: add it with `mw subscriptions top-up %s --amount %s`, then run this again", cents(shortfallCents), sub, amount)
	if subscription == "" {
		line += " (`mw subscriptions` lists them)"
	}
	return line
}

// reaisArg is an amount in cents as `--amount` takes it ("12,34"), or ""
// when it is not more than zero.
func reaisArg(raw string) string {
	v, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok || v.Sign() <= 0 {
		return ""
	}
	whole, frac := new(big.Int).QuoRem(v, big.NewInt(100), new(big.Int))
	return fmt.Sprintf("%s,%02d", whole, frac.Int64())
}

func firstN[T any](ctx context.Context, c *api.Client, path string, n int) ([]T, error) {
	if n <= 0 {
		return api.All[T](ctx, c, path, nil)
	}
	var out []T
	q := url.Values{}
	size := n
	if size > 100 {
		size = 100
	}
	q.Set("limit", strconv.Itoa(size))
	for len(out) < n {
		var p api.Page[T]
		if err := c.Do(ctx, "GET", path, q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if !p.HasMore || p.NextCursor == "" {
			break
		}
		q.Set("cursor", p.NextCursor)
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func operationsCmd(a *App) *cobra.Command {
	var resourceType, resourceID, status string
	var limit int
	cmd := &cobra.Command{
		Use:     "operations",
		Aliases: []string{"operation", "ops"},
		Short:   "Background work (deploys, firewall commits, …) and how it went",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if resourceType != "" {
				q.Set("resource_type", resourceType)
			}
			if resourceID != "" {
				q.Set("resource_id", resourceID)
			}
			if status != "" {
				q.Set("status", status)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			var p api.Page[api.Operation]
			if err := c.Do(cmd.Context(), "GET", "/operations", q, nil, &p); err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "TYPE", "STATUS", "RESOURCE", "PROGRESS", "STARTED"}, empty: "No operations."}
			for _, op := range p.Data {
				res := ""
				if op.ResourceType != nil {
					res = *op.ResourceType + " " + str(op.ResourceID)
				}
				prog := "—"
				if op.Progress != nil {
					prog = fmt.Sprintf("%.0f%%", *op.Progress)
				}
				l.add(op.ID, op.ID, op.Type, op.Status, orDash(res), prog, opStarted(op))
			}
			return a.printList(p.Data, l)
		},
	}
	cmd.Flags().StringVar(&resourceType, "resource-type", "", "instance, firewall, vpc, snapshot, backup or order")
	cmd.Flags().StringVar(&resourceID, "resource-id", "", "the resource's id")
	cmd.Flags().StringVar(&status, "status", "", "active (still running), pending, in_progress, completed, failed or cancelled")
	cmd.Flags().IntVar(&limit, "limit", 25, "how many")

	get := &cobra.Command{
		Use:   "get <operation-id>",
		Short: "One operation: where it is, and why it failed if it did",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var op api.Operation
			if err := c.Do(cmd.Context(), "GET", "/operations/"+args[0], nil, nil, &op); err != nil {
				return err
			}
			return a.printDetail(op, operationDetail(op))
		},
	}

	var timeout time.Duration
	wait := &cobra.Command{
		Use:   "wait <operation-id>",
		Short: "Wait until an operation ends (fails when it fails)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			op, err := a.waitOperation(cmd.Context(), args[0], timeout)
			if err != nil {
				if op != nil && a.JSON {
					_ = a.encode(op)
				}
				return err
			}
			return a.printDetail(op, operationDetail(*op))
		},
	}
	wait.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long to wait")

	cmd.AddCommand(get, wait)
	return cmd
}

func operationDetail(op api.Operation) detail {
	var d detail
	d.add("ID", op.ID)
	d.add("Type", op.Type)
	d.add("Status", op.Status)
	if op.ResourceType != nil {
		d.add("Resource", *op.ResourceType+" "+str(op.ResourceID))
	}
	if op.Progress != nil {
		d.add("Progress", fmt.Sprintf("%.0f%%", *op.Progress))
	}
	if op.Error != nil && *op.Error != "" {
		d.add("Error", *op.Error)
	}
	d.add("Started", opStarted(op))
	if op.CompletedAt != nil {
		d.add("Ended", whenPtr(op.CompletedAt))
	}
	return d
}

// opStarted is when an operation started, else when it was queued.
func opStarted(op api.Operation) string {
	if op.StartedAt != nil && *op.StartedAt != "" {
		return when(*op.StartedAt)
	}
	return when(op.CreatedAt)
}
