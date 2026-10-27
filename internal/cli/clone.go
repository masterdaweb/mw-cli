package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// instanceCloneCmd copies one instance's disks onto another of the account's
// instances — as it is now, or as one of its backups was. The quote comes
// first; the destination's id is typed to confirm.
func instanceCloneCmd(a *App) *cobra.Command {
	var fromBackup string
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "clone <origin> <destination>",
		Short: "Copy an instance onto another one (the destination's disks are replaced)",
		Long: "Clone an instance onto another of yours: the destination's disks are replaced by the\n" +
			"origin's — as it is now, or as one of its backups was — and its addresses and plan stay.\n" +
			"What changes (disks, price) is shown first; then you type the destination's id to confirm.",
		Example: "  mw instances clone web-1 web-2\n" +
			"  mw instances clone web-1 staging --from-backup latest --wait",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			origin, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			dest, err := a.resolveInstance(ctx, args[1])
			if err != nil {
				return err
			}
			if origin.ID == dest.ID {
				return errors.New("the origin and the destination are the same instance")
			}
			body := map[string]interface{}{"destination_instance_id": dest.ID, "source": "current"}
			from := "as it is now"
			if fromBackup != "" {
				_, b, err := a.backup(ctx, origin.ID, fromBackup)
				if err != nil {
					return err
				}
				body["source"], body["backup_id"] = "backup", b.ID
				from = "as its backup of " + whenPtr(b.CreatedAt) + " was"
			}
			c, _ := a.Client()
			var chk api.CloneCheck
			if err := c.Do(ctx, "POST", "/instances/"+origin.ID+"/clone/quote", nil, body, &chk); err != nil {
				return notOffered(err, "Cloning from the API")
			}
			if !a.JSON && !a.Quiet {
				if err := a.printCloneCheck(origin, dest, from, chk); err != nil {
					return err
				}
			}
			if !chk.CanClone {
				if a.JSON {
					if err := a.encode(chk); err != nil {
						return err
					}
				}
				return cloneRefused(origin, dest, chk)
			}
			if !yes {
				q := fmt.Sprintf("This replaces the disks of %s with %s's: what is on %s now is lost.\nType the destination's id (%s) to confirm: ",
					instanceLabel(dest), instanceLabel(origin), instanceLabel(dest), dest.ID)
				if err := a.typeToConfirm(q, dest.ID, "--yes"); err != nil {
					return err
				}
			}
			body["confirm"] = dest.ID
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/instances/"+origin.ID+"/clone", nil, body, &acc); err != nil {
				var p *api.Problem
				if errors.As(err, &p) && p.Code == "insufficient_balance" {
					sub := p.SubscriptionID
					if sub == "" && chk.Billing != nil {
						sub = chk.Billing.SubscriptionID
					}
					return fmt.Errorf("nothing changed: %s", topUpLine(sub, p.ShortfallCents.String()))
				}
				return err
			}
			if wait {
				target := firstNonEmpty(acc.InstanceID, dest.ID)
				if err := a.waitAccepted(ctx, acc, timeout, func(ctx context.Context) error {
					_, err := a.waitInstance(ctx, target, timeout, "cloned", func(st api.InstanceStatus) bool { return !st.Busy })
					return err
				}); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			what := fmt.Sprintf("Clone of %s onto %s", instanceLabel(origin), instanceLabel(dest))
			if wait {
				fmt.Fprintf(a.Out, "%s: done.\n", what)
			} else {
				fmt.Fprintln(a.Out, acceptedText(acc, what))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromBackup, "from-backup", "", "copy one of the origin's backups (its id, or latest) instead of the origin as it is now")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "clone without asking")
	waitFlags(cmd, &wait, &timeout, 2*time.Hour)
	return cmd
}

func (a *App) printCloneCheck(origin, dest *api.Instance, from string, chk api.CloneCheck) error {
	var d detail
	d.add("Clone", fmt.Sprintf("%s → %s, %s", instanceLabel(origin), instanceLabel(dest), from))
	if dc := chk.DiskChanges; dc != nil {
		d.add("Main disk", fmt.Sprintf("%s → %s GB", fnum(dc.MainDiskBeforeGB), fnum(dc.MainDiskAfterGB)))
		if len(dc.ExtraDisksBeforeGB) > 0 {
			d.add("Data disks lost", gbList(dc.ExtraDisksBeforeGB))
		}
		if len(dc.ExtraDisksAfterGB) > 0 {
			d.add("Data disks after", gbList(dc.ExtraDisksAfterGB))
		}
	}
	if b := chk.Billing; b != nil {
		cur, next := b.CurrentHourlyRateMicroCents, b.NewHourlyRateMicroCents
		if cur == next {
			d.add("Destination price", "unchanged")
		} else {
			d.add("Destination price", fmt.Sprintf("%s → %s (%s → %s)", perHour(&cur), perHour(&next), perMonth(&cur), perMonth(&next)))
		}
		if reaisArg(b.ChargeNowCents) != "" {
			d.add("Charged now", cents(b.ChargeNowCents)+", for the rest of this cycle")
		}
		if reaisArg(b.CreditCents) != "" {
			d.add("Credit", cents(b.CreditCents)+" back, for the rest of this cycle")
		}
	}
	for _, w := range chk.Warnings {
		d.after = append(d.after, "Note: "+w)
	}
	for _, v := range chk.Violations {
		d.after = append(d.after, "Not allowed: "+v.Message)
	}
	if short := cloneShortfall(chk); short != "" {
		d.after = append(d.after, "The subscription's balance is short by "+cents(short)+".")
	}
	return a.printDetail(chk, d)
}

// cloneRefused says why a clone cannot happen; when only the balance stands
// in the way, how to top it up.
func cloneRefused(origin, dest *api.Instance, chk api.CloneCheck) error {
	short := cloneShortfall(chk)
	if len(chk.Violations) == 0 && short != "" {
		return fmt.Errorf("nothing changed: %s", topUpLine(chk.Billing.SubscriptionID, short))
	}
	var why []string
	if r := str(chk.Reason); r != "" {
		why = append(why, r)
	}
	for _, v := range chk.Violations {
		why = append(why, v.Message)
	}
	if short != "" {
		why = append(why, "the subscription's balance is short by "+cents(short))
	}
	if len(why) == 0 {
		why = append(why, "the API gave no reason")
	}
	return fmt.Errorf("%s cannot be cloned onto %s:\n  %s", instanceLabel(origin), instanceLabel(dest), strings.Join(why, "\n  "))
}

func cloneShortfall(chk api.CloneCheck) string {
	if b := chk.Billing; b != nil && b.ShortfallCents != nil && reaisArg(*b.ShortfallCents) != "" {
		return *b.ShortfallCents
	}
	return ""
}

// gbList is "100 GB, 50 GB".
func gbList(sizes []float64) string {
	out := make([]string, len(sizes))
	for i, s := range sizes {
		out[i] = fnum(s) + " GB"
	}
	return strings.Join(out, ", ")
}
