package cli

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func walletCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wallet",
		Short: "The credit services, databases and storage are paid from, and how long it lasts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var w api.Wallet
			if err := c.Do(cmd.Context(), "GET", "/serverless/wallet", nil, nil, &w); err != nil {
				return err
			}
			return a.print(w, func(t *tabwriter.Writer) {
				fmt.Fprintf(t, "Credit\t%s\n", money(w.UsableMicroCents, 2))
				switch {
				case w.SuspendedAt != nil:
					fmt.Fprintf(t, "Status\tsuspended since %s for lack of credit: services are stopped until a top-up\n", when(*w.SuspendedAt))
					if w.RetentionDeleteAt != nil {
						fmt.Fprintf(t, "Kept until\t%s: databases and files may be deleted after, if the credit stays out\n", when(*w.RetentionDeleteAt))
					}
				case w.RunwayHours != nil:
					fmt.Fprintf(t, "Lasts\t%s at the current pace\n", runway(*w.RunwayHours))
				}
				fmt.Fprintf(t, "A new service needs\t%s\n", money(w.MinimumForNewService, 2))
				fmt.Fprintf(t, "Automatic top-up\t%s\n", rechargeState(w))
				// The cap, when there is one (a token without the scope still sees the rest).
				var cap api.SpendCap
				if c.Do(cmd.Context(), "GET", "/serverless/spend-cap", nil, nil, &cap) == nil && cap.CapMicroCents != nil {
					fmt.Fprintf(t, "Spend cap\t%s of %s this period%s\n", money(cap.SpentMicroCents, 2), money(*cap.CapMicroCents, 2), capState(cap))
				}
				fmt.Fprintln(t, "\nAdd credit: `mw wallet topup <reais>`, or Credits & Plans in the console.")
			})
		},
	}

	var coupon string
	topup := &cobra.Command{
		Use:     "topup <amount in reais>",
		Short:   "Raise an invoice to add credit; the credit lands once it is paid",
		Example: "  mw wallet topup 100\n  mw wallet topup 49,90",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cents, err := parseReais(args[0])
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var w api.Wallet
			if err := c.Do(cmd.Context(), "GET", "/serverless/wallet", nil, nil, &w); err != nil {
				return err
			}
			body := map[string]interface{}{"amount_cents": cents}
			if coupon != "" {
				body["coupon_code"] = coupon
			}
			var out struct {
				InvoiceID   string `json:"invoice_id"`
				AmountCents string `json:"amount_cents"`
				PayURL      string `json:"pay_url"`
				DueAt       string `json:"due_at"`
			}
			if err := c.Do(cmd.Context(), "POST", "/subscriptions/"+w.SubscriptionID+"/top-ups", nil, body, &out); err != nil {
				return err
			}
			return a.print(out, func(t *tabwriter.Writer) {
				fmt.Fprintf(t, "Invoice %s: %s, due %s.\n", out.InvoiceID, money(out.AmountCents+"0000", 2) /* cents → µ¢ */, when(out.DueAt))
				fmt.Fprintf(t, "Pay it (Pix, boleto or card) at:\n  %s\n", out.PayURL)
				fmt.Fprintln(t, "The credit lands once it is paid.")
			})
		},
	}
	topup.Flags().StringVar(&coupon, "coupon", "", "a coupon code")
	cmd.AddCommand(topup)
	cmd.AddCommand(spendCapCmd(a))
	cmd.AddCommand(autoRechargeCmd(a))
	return cmd
}

// rechargeState says whether a top-up goes out on its own, and of how much.
func rechargeState(w api.Wallet) string {
	if !w.AutoRechargeEnabled || w.AutoRechargeAmount == nil {
		return "off — when the credit runs out, the services stop (`mw wallet recharge on <reais>`)"
	}
	return fmt.Sprintf("on: %s when the credit has 3 days left", money(*w.AutoRechargeAmount, 2))
}

func autoRechargeCmd(a *App) *cobra.Command {
	put := func(cmd *cobra.Command, body map[string]interface{}) error {
		c, err := a.Client()
		if err != nil {
			return err
		}
		var w api.Wallet
		if err := c.Do(cmd.Context(), "PUT", "/serverless/wallet/auto-recharge", nil, body, &w); err != nil {
			return err
		}
		return a.print(w, func(t *tabwriter.Writer) {
			fmt.Fprintf(t, "Automatic top-up\t%s\n", rechargeState(w))
		})
	}
	cmd := &cobra.Command{
		Use:   "recharge",
		Short: "A top-up invoice on its own when the credit has 3 days left",
		Long: "On, a top-up invoice goes out by itself when the wallet's credit has three days\n" +
			"left at the current pace: paid at once by a card saved in the client area, or by\n" +
			"Pix or boleto from the e-mail. Off, the services stop when the credit runs out.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var w api.Wallet
			if err := c.Do(cmd.Context(), "GET", "/serverless/wallet", nil, nil, &w); err != nil {
				return err
			}
			return a.print(w, func(t *tabwriter.Writer) {
				fmt.Fprintf(t, "Automatic top-up\t%s\n", rechargeState(w))
			})
		},
	}
	on := &cobra.Command{
		Use:     "on <amount in reais>",
		Short:   "Turn it on, adding this much each time (R$ 20 to R$ 50 000)",
		Example: "  mw wallet recharge on 100",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cents, err := parseReais(args[0])
			if err != nil {
				return err
			}
			if cents < 2_000 || cents > 5_000_000 {
				return errors.New("a top-up is from R$ 20 to R$ 50 000")
			}
			return put(cmd, map[string]interface{}{"enabled": true, "amount_micro_cents": fmt.Sprintf("%d", cents*10_000)})
		},
	}
	off := &cobra.Command{
		Use:   "off",
		Short: "Turn it off (the amount is kept for next time)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return put(cmd, map[string]interface{}{"enabled": false})
		},
	}
	cmd.AddCommand(on, off)
	return cmd
}

// capState says what the cap does, or is doing.
func capState(c api.SpendCap) string {
	switch {
	case c.Stopped:
		return " — reached: services stopped until the period ends or the cap is raised"
	case c.Action == "STOP":
		return " (stops the services when reached)"
	default:
		return " (tells you when reached)"
	}
}

func spendCapCmd(a *App) *cobra.Command {
	show := func(cmd *cobra.Command, c api.SpendCap) error {
		return a.print(c, func(t *tabwriter.Writer) {
			if c.CapMicroCents == nil {
				fmt.Fprintf(t, "Spend cap\tnone (%s spent this period)\n", money(c.SpentMicroCents, 2))
				fmt.Fprintln(t, "\nSet one: `mw wallet cap set <reais> [--stop]`.")
				return
			}
			fmt.Fprintf(t, "Spend cap\t%s a period\n", money(*c.CapMicroCents, 2))
			fmt.Fprintf(t, "Spent\t%s (%s to %s)\n", money(c.SpentMicroCents, 2), when(c.PeriodFrom), when(c.PeriodTo))
			if c.Stopped {
				fmt.Fprintln(t, "Status\treached: the services are stopped until the period ends or the cap is raised")
			} else if c.Action == "STOP" {
				fmt.Fprintln(t, "When reached\tthe services stop (databases and files keep running)")
			} else {
				fmt.Fprintln(t, "When reached\tyou are told; the services keep running")
			}
		})
	}
	cmd := &cobra.Command{
		Use:   "cap",
		Short: "The most serverless may cost a period, and what reaching it does",
		Long: "The spend cap counts everything serverless charges the wallet in its period —\n" +
			"services, builds, egress, databases, Object Storage. You are told at 80% and at\n" +
			"100%; with --stop, the services also stop at 100% until the period ends or the cap\n" +
			"is raised (databases and files keep running).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var cap api.SpendCap
			if err := c.Do(cmd.Context(), "GET", "/serverless/spend-cap", nil, nil, &cap); err != nil {
				return err
			}
			return show(cmd, cap)
		},
	}
	var stop bool
	set := &cobra.Command{
		Use:     "set <amount in reais>",
		Short:   "Set the cap (--stop: stop the services when it is reached)",
		Example: "  mw wallet cap set 500\n  mw wallet cap set 1200 --stop",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cents, err := parseReais(args[0])
			if err != nil {
				return err
			}
			if cents < 100 {
				return errors.New("a spend cap is at least R$ 1")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			action := "WARN"
			if stop {
				action = "STOP"
			}
			body := map[string]interface{}{"cap_micro_cents": fmt.Sprintf("%d", cents*10_000), "action": action}
			var cap api.SpendCap
			if err := c.Do(cmd.Context(), "PUT", "/serverless/spend-cap", nil, body, &cap); err != nil {
				return err
			}
			return show(cmd, cap)
		},
	}
	set.Flags().BoolVar(&stop, "stop", false, "stop the services when the cap is reached (default: only tell)")
	remove := &cobra.Command{
		Use:   "remove",
		Short: "Remove the cap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"cap_micro_cents": nil, "action": "WARN"}
			var cap api.SpendCap
			if err := c.Do(cmd.Context(), "PUT", "/serverless/spend-cap", nil, body, &cap); err != nil {
				return err
			}
			return show(cmd, cap)
		},
	}
	cmd.AddCommand(set, remove)
	return cmd
}

func runway(hours int) string {
	switch {
	case hours < 48:
		return fmt.Sprintf("about %d hours", hours)
	default:
		return fmt.Sprintf("about %d days", hours/24)
	}
}

var reaisRe = regexp.MustCompile(`^(\d{1,7})(?:[.,](\d{1,2}))?$`)

// parseReais reads "100", "49,90" or "49.90" as cents.
func parseReais(s string) (int64, error) {
	m := reaisRe.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "R$")))
	if m == nil {
		return 0, fmt.Errorf("%q is not an amount in reais, like 100 or 49,90", s)
	}
	whole, _ := new(big.Int).SetString(m[1], 10)
	frac := m[2]
	for len(frac) < 2 {
		frac += "0"
	}
	f, _ := new(big.Int).SetString(frac, 10)
	cents := whole.Int64()*100 + f.Int64()
	if cents <= 0 {
		return 0, errors.New("the amount must be more than zero")
	}
	return cents, nil
}

func usageCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "What this period has cost so far, per service, with the month's forecast",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var u api.Usage
			if err := c.Do(cmd.Context(), "GET", "/serverless/usage", nil, nil, &u); err != nil {
				return err
			}
			// Databases and storage are paid from the same wallet; a token
			// without their scopes still sees its services.
			dbs, dbErr := a.databases(cmd.Context(), "")
			var st api.StorageUsage
			stErr := c.Do(cmd.Context(), "GET", "/object-storage/usage", nil, nil, &st)
			var eg api.Egress
			egErr := c.Do(cmd.Context(), "GET", "/serverless/egress", nil, nil, &eg)
			out := struct {
				Services  api.Usage         `json:"services"`
				Egress    *api.Egress       `json:"egress,omitempty"`
				Databases []api.Database    `json:"databases,omitempty"`
				Storage   *api.StorageUsage `json:"storage,omitempty"`
			}{Services: u}
			if egErr == nil {
				out.Egress = &eg
			}
			if dbErr == nil {
				out.Databases = dbs
			}
			if stErr == nil {
				out.Storage = &st
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Period\t%s to %s\n\n", when(u.PeriodFrom), when(u.PeriodTo))
				fmt.Fprintln(w, "SERVICE\tINSTANCES\tJOBS\tBUILDS\tCOST")
				for _, s := range u.Services {
					name := str(s.Name)
					if name == "" {
						name = "(deleted)"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, duration(s.InstanceSeconds), duration(s.JobSeconds), duration(s.BuildSeconds), money(s.CostMicroCents, 2))
				}
				if len(out.Databases) > 0 {
					fmt.Fprintln(w, "\nDATABASE\tENGINE\tSIZE\t\tTHIS MONTH")
					for _, d := range out.Databases {
						if d.Status == "deleted" && d.Cost.MonthToDateMicroCents == "0" {
							continue
						}
						fmt.Fprintf(w, "%s\t%s\t%s\t\t%s\n", d.Name, engineName(d.Engine), d.Size.Code, money(d.Cost.MonthToDateMicroCents, 2))
					}
				}
				if out.Storage != nil {
					fmt.Fprintf(w, "\nObject Storage\t\t\t\t%s\n", money(out.Storage.CostMicroCents, 2))
				}
				if out.Egress != nil {
					fmt.Fprintf(w, "\nSent to visitors\t%s (`mw egress`: by service)\n", egressLine(out.Egress))
				}
				fmt.Fprintf(w, "\nServices so far\t%s\n", money(u.TotalMicroCents, 2))
				fmt.Fprintf(w, "Services forecast\t%s for the period\n", money(u.ForecastMicroCents, 2))
				if !u.Priced {
					fmt.Fprintln(w, "Some usage has no published price yet: it is not charged.")
				}
			})
		},
	}
}

func duration(seconds int64) string {
	switch {
	case seconds == 0:
		return "—"
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%dh%02dm", seconds/3600, seconds%3600/60)
	}
}
