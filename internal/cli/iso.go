package cli

import (
	"context"
	"fmt"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// instanceISOCmd: one of the account's ISOs in an instance's CD-ROM drive, to
// install a system of your own or boot a rescue tool (Vultr's "attach ISO").
func instanceISOCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iso <instance>",
		Short: "The ISO in an instance's drive: attach one to boot from it, detach it",
		Long: "Put one of your ISOs (`mw custom-images --kind iso`) in an instance's CD-ROM drive. It\n" +
			"becomes the first boot device; the instance keeps its power state, so reboot it to boot\n" +
			"from the ISO. It is ejected on its own after a while, so a forgotten ISO does not boot again.",
		Example: "  mw instances iso web-1\n" +
			"  mw instances iso attach web-1 alpine-3.20\n" +
			"  mw instances reboot web-1\n" +
			"  mw instances iso detach web-1",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return a.showISO(cmd.Context(), args[0]) },
	}
	get := &cobra.Command{
		Use:   "get <instance>",
		Short: "What is in an instance's drive",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return a.showISO(cmd.Context(), args[0]) },
	}
	attach := &cobra.Command{
		Use:   "attach <instance> <iso>",
		Short: "Put an ISO in an instance's drive (reboot to boot from it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			img, err := a.isoImage(ctx, args[1])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var st api.ISOAttachment
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/iso", nil, map[string]string{"image_id": img.ID}, &st); err != nil {
				return notOffered(err, "Attaching ISOs from the API")
			}
			if a.JSON {
				return a.encode(st)
			}
			fmt.Fprintf(a.Out, "ISO %s is in %s's drive: reboot it to boot from the ISO (`mw instances reboot %s`).\n",
				firstNonEmpty(str(st.ImageName), img.Name), instanceLabel(in), in.ID)
			if st.EjectsAt != nil {
				fmt.Fprintf(a.Out, "It is ejected on its own at %s; `mw instances iso detach %s` ejects it sooner.\n", whenPtr(st.EjectsAt), in.ID)
			}
			return nil
		},
	}
	detach := &cobra.Command{
		Use:     "detach <instance>",
		Aliases: []string{"eject"},
		Short:   "Eject the ISO: the instance boots from its disk again",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var st api.ISOAttachment
			if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/iso", nil, nil, &st); err != nil {
				return notOffered(err, "Attaching ISOs from the API")
			}
			name := str(st.ImageName)
			ejected := st.Attached
			if st.Attached {
				err := c.Do(ctx, "DELETE", "/instances/"+in.ID+"/iso", nil, nil, nil)
				switch {
				case isNotFound(err):
					// Ejected meanwhile (or on its own): the drive is empty, as asked.
					ejected = false
				case err != nil:
					return notOffered(err, "Attaching ISOs from the API")
				}
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"instance_id": in.ID, "ejected": ejected})
			}
			if !ejected {
				fmt.Fprintf(a.Out, "No ISO in %s's drive: nothing to eject.\n", instanceLabel(in))
				return nil
			}
			fmt.Fprintf(a.Out, "ISO %s ejected from %s: it boots from its disk again.\n", firstNonEmpty(name, "the"), instanceLabel(in))
			return nil
		},
	}
	cmd.AddCommand(get, attach, detach)
	return cmd
}

func (a *App) showISO(ctx context.Context, ref string) error {
	in, err := a.resolveInstance(ctx, ref)
	if err != nil {
		return err
	}
	c, _ := a.Client()
	var st api.ISOAttachment
	if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/iso", nil, nil, &st); err != nil {
		return notOffered(err, "Attaching ISOs from the API")
	}
	if a.JSON {
		return a.encode(st)
	}
	if !st.Attached {
		fmt.Fprintf(a.Out, "No ISO in %s's drive: `mw instances iso attach %s <iso>` puts one in.\n", instanceLabel(in), in.ID)
		return nil
	}
	var d detail
	d.add("Instance", instanceLabel(in))
	d.add("ISO", firstNonEmpty(str(st.ImageName), "—")+" "+parens(str(st.ImageID)))
	d.add("Attached", whenPtr(st.AttachedAt))
	d.add("Ejected on its own", whenPtr(st.EjectsAt))
	d.after = append(d.after, fmt.Sprintf("Reboot the instance to boot from it; `mw instances iso detach %s` ejects it.", in.ID))
	return a.printDetail(st, d)
}

// isoImage finds one of the account's ISOs, ready to attach.
func (a *App) isoImage(ctx context.Context, ref string) (*api.CustomImage, error) {
	img, err := a.customImage(ctx, ref)
	if err != nil {
		return nil, notOffered(err, "Custom images from the API")
	}
	if img.Kind != "iso" {
		return nil, fmt.Errorf("%s is a disk image, not an ISO: `mw custom-images --kind iso` lists the ISOs", img.Name)
	}
	if final, ready := imageFinal(img.Status); !ready {
		if final {
			return nil, fmt.Errorf("ISO %s is %s: %s", img.Name, img.Status, firstNonEmpty(str(img.FailureReason), "it cannot be used"))
		}
		return nil, fmt.Errorf("ISO %s is not ready yet (%s): `mw custom-images get %s` says when it is", img.Name, img.Status, img.Name)
	}
	return img, nil
}
