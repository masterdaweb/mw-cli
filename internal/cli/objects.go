package cli

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func storageRmCmd(a *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <bucket> <key>...",
		Aliases: []string{"delete-objects"},
		Short:   "Delete files from a bucket",
		Example: "  mw storage rm loja-uploads fotos/1.jpg fotos/2.jpg",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys := args[1:]
			if !yes {
				what := keys[0]
				if len(keys) > 1 {
					what = fmt.Sprintf("%d files", len(keys))
				}
				ok, err := a.confirm(fmt.Sprintf("Delete %s from %s? There is no undo. [y/N] ", what, args[0]), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var out struct {
				Deleted int `json:"deleted"`
			}
			if err := c.Do(cmd.Context(), "POST", "/object-storage/buckets/"+url.PathEscape(args[0])+"/objects/delete", nil, map[string]interface{}{"keys": keys}, &out); err != nil {
				return notOffered(err, "Deleting files from the API")
			}
			if a.JSON {
				return a.encode(out)
			}
			fmt.Fprintf(a.Out, "%d file(s) deleted from %s.\n", out.Deleted, args[0])
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	return cmd
}

func storagePresignCmd(a *App) *cobra.Command {
	var upload bool
	var contentType string
	var expires time.Duration
	cmd := &cobra.Command{
		Use:   "presign <bucket> <key>",
		Short: "A temporary link to download a file (or, with --upload, to upload one)",
		Example: "  mw storage presign loja-uploads relatorio.pdf --expires 24h\n" +
			"  mw storage presign loja-uploads novo.csv --upload --content-type text/csv",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]interface{}{"key": args[1]}
			path := "download-url"
			if upload {
				// An upload link lasts as long as the API sets: it takes no expiry.
				if cmd.Flags().Changed("expires") {
					return errors.New("--expires is for download links: an upload link lasts as long as the API sets (its expiry is printed)")
				}
				path = "upload-url"
				if contentType != "" {
					body["content_type"] = contentType
				}
			} else {
				if contentType != "" {
					return errors.New("--content-type goes with --upload")
				}
				if expires < time.Minute || expires > 7*24*time.Hour {
					return errors.New("--expires is between 1m and 7 days (168h)")
				}
				body["expires_seconds"] = int(expires.Seconds())
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var out api.PresignedURL
			if err := c.Do(cmd.Context(), "POST", "/object-storage/buckets/"+url.PathEscape(args[0])+"/objects/"+path, nil, body, &out); err != nil {
				return notOffered(err, "Temporary links from the API")
			}
			if a.JSON {
				return a.encode(out)
			}
			if a.Quiet || !upload {
				fmt.Fprintln(a.Out, out.URL)
				if !a.Quiet {
					fmt.Fprintf(a.Err, "(valid until %s)\n", when(out.ExpiresAt))
				}
				return nil
			}
			// A link signed for a content type takes uploads of that type only.
			header := ""
			if contentType != "" {
				header = " -H " + shellQuote("Content-Type: "+contentType)
			}
			fmt.Fprintf(a.Out, "%s\n\nUpload it, until %s:\n  curl -X PUT%s --upload-file %s %s\n", out.URL, when(out.ExpiresAt), header, shellQuote(args[1]), shellQuote(out.URL))
			return nil
		},
	}
	cmd.Flags().BoolVar(&upload, "upload", false, "a link to upload the file (PUT) instead of downloading it")
	cmd.Flags().StringVar(&contentType, "content-type", "", "with --upload: the file's type (the upload must send the same)")
	cmd.Flags().DurationVar(&expires, "expires", time.Hour, "download links: how long it works, 1m to 168h")
	return cmd
}
