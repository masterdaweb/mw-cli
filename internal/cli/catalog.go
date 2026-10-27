package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func regionsCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "regions",
		Aliases: []string{"region", "locations"},
		Short:   "Where instances can be created",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Region](cmd.Context(), c, "/regions", nil)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "CODE", "AVAILABLE", "IPV4", "IPV6"}, empty: "No regions."}
			for _, r := range all {
				l.add(r.ID, r.ID, r.Name, orDashPtr(r.RegionName), yesNo(r.Available), yesNo(r.IPv4Available), yesNo(r.IPv6Available))
			}
			return a.printList(all, l)
		},
	}
}

func plansCmd(a *App) *cobra.Command {
	var term string
	cmd := &cobra.Command{
		Use:     "plans",
		Aliases: []string{"plan", "offerings", "sizes"},
		Short:   "Instance plans: vCPU, memory, disk and price",
		Example: "  mw plans\n  mw plans --term annual",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := parseTerm(term)
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Offering](cmd.Context(), c, "/catalog/offerings", nil)
			if err != nil {
				return err
			}
			sort.SliceStable(all, func(i, j int) bool {
				if all[i].VCPUs != all[j].VCPUs {
					return all[i].VCPUs < all[j].VCPUs
				}
				return all[i].RAMMB < all[j].RAMMB
			})
			l := listing{headers: []string{"CODE", "NAME", "VCPU", "MEMORY", "DISK", "PRICE (" + termText(t) + ")", "HOURLY"}, empty: "No plans."}
			for _, o := range all {
				rate := rateFor(o.Rates, t)
				l.add(o.Code, o.Code, o.Name, fmt.Sprintf("%g", o.VCPUs), strings.TrimSuffix(ramText(o.RAMMB), " RAM"), fmt.Sprintf("%g GB", o.BaseDiskGB), perMonth(rate), perHour(rate))
			}
			return a.printList(all, l)
		},
	}
	cmd.Flags().StringVar(&term, "term", "monthly", "the billing term to price: monthly, quarterly, semi-annual, annual or on-demand")
	return cmd
}

func imagesCmd(a *App) *cobra.Command {
	var apps, systems bool
	var family string
	cmd := &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "os"},
		Short:   "Operating systems and ready-made apps an instance can start from",
		Example: "  mw images\n  mw images --apps\n  mw images --family windows",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.CatalogImage](cmd.Context(), c, "/catalog/images", nil)
			if err != nil {
				return err
			}
			var shown []api.CatalogImage
			for _, im := range all {
				if apps && im.Type != "MARKETPLACE_APP" || systems && im.Type != "OPERATING_SYSTEM" {
					continue
				}
				if family != "" && !strings.EqualFold(im.OSFamily, family) {
					continue
				}
				shown = append(shown, im)
			}
			l := listing{headers: []string{"CODE", "NAME", "KIND", "FAMILY", "MINIMUM"}, empty: "No images match."}
			for _, im := range shown {
				kind := "os"
				if im.Type == "MARKETPLACE_APP" {
					kind = "app"
				}
				var min []string
				if im.MinVCPU != nil {
					min = append(min, num(im.MinVCPU)+" vCPU")
				}
				if im.MinRAMMB != nil {
					min = append(min, strings.TrimSuffix(ramText(*im.MinRAMMB), " RAM"))
				}
				if im.MinDiskGB != nil {
					min = append(min, num(im.MinDiskGB)+" GB")
				}
				l.add(im.Code, im.Code, im.Name, kind, im.OSFamily, orDash(strings.Join(min, " · ")))
			}
			return a.printList(shown, l)
		},
	}
	cmd.Flags().BoolVar(&apps, "apps", false, "ready-made apps only")
	cmd.Flags().BoolVar(&systems, "os", false, "operating systems only")
	cmd.Flags().StringVar(&family, "family", "", "linux, windows or other")
	return cmd
}

func addonsCmd(a *App) *cobra.Command {
	var term string
	cmd := &cobra.Command{
		Use:     "addons",
		Aliases: []string{"addon"},
		Short:   "Add-ons an instance can have: backups, extra IPs, licences…",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := parseTerm(term)
			if err != nil {
				return err
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			all, err := api.All[api.Addon](cmd.Context(), c, "/catalog/addons", nil)
			if err != nil {
				return err
			}
			l := listing{headers: []string{"CODE", "NAME", "TYPE", "QUANTITY", "PRICE (" + termText(t) + ")"}, empty: "No add-ons."}
			for _, ad := range all {
				q := "1"
				if ad.QuantityEnabled {
					q = fmt.Sprintf("%g+", ad.MinQuantity)
					if ad.MaxQuantity != nil {
						q = fmt.Sprintf("%g–%g", ad.MinQuantity, *ad.MaxQuantity)
					}
				}
				l.add(ad.Code, ad.Code, ad.Name, strings.ToLower(ad.Type), q, perMonth(rateFor(ad.Rates, t)))
			}
			return a.printList(all, l)
		},
	}
	cmd.Flags().StringVar(&term, "term", "monthly", "the billing term to price")
	return cmd
}
