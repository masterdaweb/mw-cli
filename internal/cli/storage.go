package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

func storageCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "storage",
		Aliases: []string{"buckets"},
		Short:   "Object Storage (S3-compatible): buckets, access keys, bind a bucket to a service",
		Long: "Object Storage is S3-compatible: once a bucket and a key exist, any S3 tool works with\n" +
			"them (AWS CLI, rclone, the SDKs) — `mw storage info` prints the endpoint and region.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := a.buckets(cmd.Context())
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tACCESS\tSIZE\tOBJECTS\tLOCATION\tSTATUS")
				for _, b := range all {
					access := "private"
					if b.Public {
						access = "public"
					}
					status := b.Status
					if b.DeletedAt != nil {
						status = "deleted, restorable until " + whenPtr(b.PurgeAfter)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Name, access, bytesText(b.SizeBytes), b.Objects, b.Location, status)
				}
			})
		},
	}
	cmd.AddCommand(storageInfoCmd(a), storageCreateCmd(a), storageGetCmd(a), storageUpdateCmd(a),
		storageDeleteCmd(a), storageRestoreCmd(a), storageLsCmd(a), storageRmCmd(a), storagePresignCmd(a),
		keysCmd(a), storageBindCmd(a), storageUnbindCmd(a))
	return cmd
}

func (a *App) buckets(ctx context.Context) ([]api.Bucket, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var all []api.Bucket
	err = c.Do(ctx, "GET", "/object-storage/buckets", nil, nil, &all)
	return all, err
}

func (a *App) bucket(ctx context.Context, name string) (*api.Bucket, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var b api.Bucket
	if err := c.Do(ctx, "GET", "/object-storage/buckets/"+url.PathEscape(name), nil, nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func storageInfoCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "The S3 endpoint and region, where the files are, and how to point a tool at them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var info api.StorageInfo
			if err := c.Do(cmd.Context(), "GET", "/object-storage/info", nil, nil, &info); err != nil {
				return err
			}
			return a.print(info, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Endpoint\t%s\n", info.Endpoint)
				fmt.Fprintf(w, "Region\t%s\n", info.Region)
				if info.Location != nil {
					fmt.Fprintf(w, "Files are kept in\t%s\n", *info.Location)
				}
				fmt.Fprintf(w, "Limits\t%d buckets, %d access keys\n", info.BucketLimit, info.KeyLimit)
				fmt.Fprintln(w)
				fmt.Fprintln(w, "With a key from `mw storage keys create`:")
				fmt.Fprintf(w, "  aws s3 ls s3://<bucket> --endpoint-url %s --region %s\n", info.Endpoint, info.Region)
				fmt.Fprintf(w, "  rclone: type = s3, provider = Other, endpoint = %s, region = %s\n", info.Endpoint, info.Region)
			})
		},
	}
}

func storageCreateCmd(a *App) *cobra.Command {
	var public, noProtection bool
	var project string
	cmd := &cobra.Command{
		Use:   "create <bucket>",
		Short: "Create a bucket (private unless --public; its name is unique on the whole platform)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"name": args[0], "public": public}
			if noProtection {
				body["deletion_protection"] = false
			}
			var b api.Bucket
			if err := c.Do(cmd.Context(), "POST", "/object-storage/buckets", nil, body, &b); err != nil {
				return err
			}
			if err := a.print(b, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Bucket %s created (%s).\n", b.Name, b.Location)
				if b.PublicURL != nil {
					fmt.Fprintf(w, "Its files are public at %s/<file>.\n", strings.TrimRight(*b.PublicURL, "/"))
				}
				fmt.Fprintln(w, "Next: `mw storage keys create <name> --bucket "+b.Name+"`, or bind it to a service.")
			}); err != nil {
				return err
			}
			if project != "" {
				return a.putInProject(cmd.Context(), project, "BUCKET", b.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&public, "public", false, "anyone may read its files (never write them)")
	cmd.Flags().BoolVar(&noProtection, "no-deletion-protection", false, "create it unprotected from deletion")
	cmd.Flags().StringVar(&project, "project", "", "put the new bucket in this project (name or id)")
	return cmd
}

func storageGetCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"info-bucket"},
		Short:   "A bucket: access, size, quota, protection",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := a.bucket(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.print(b, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Bucket\t%s\n", b.Name)
				fmt.Fprintf(w, "Location\t%s\n", b.Location)
				access := "private"
				if b.Public {
					access = "public, read-only"
				}
				fmt.Fprintf(w, "Access\t%s\n", access)
				if b.PublicURL != nil {
					fmt.Fprintf(w, "Public address\t%s\n", *b.PublicURL)
				}
				fmt.Fprintf(w, "Size\t%s in %s objects\n", bytesText(b.SizeBytes), b.Objects)
				if b.QuotaBytes != nil {
					fmt.Fprintf(w, "Quota\t%s (%s when reached)\n", bytesText(*b.QuotaBytes), b.QuotaMode)
				}
				protection := "on"
				if !b.DeletionProtection {
					protection = "off"
				}
				fmt.Fprintf(w, "Deletion protection\t%s\n", protection)
				if b.DeletedAt != nil {
					fmt.Fprintf(w, "Deleted\t%s (restorable until %s)\n", whenPtr(b.DeletedAt), whenPtr(b.PurgeAfter))
				}
			})
		},
	}
}

func storageUpdateCmd(a *App) *cobra.Command {
	var public, protection bool
	cmd := &cobra.Command{
		Use:     "update <bucket>",
		Short:   "Make a bucket public or private, or change its deletion protection",
		Example: "  mw storage update loja-imagens --public\n  mw storage update loja-tmp --deletion-protection=false",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]interface{}{}
			if cmd.Flags().Changed("public") {
				patch["public"] = public
			}
			if cmd.Flags().Changed("deletion-protection") {
				patch["deletion_protection"] = protection
			}
			if len(patch) == 0 {
				return errors.New("nothing to change: pass --public or --deletion-protection")
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var b api.Bucket
			if err := c.Do(cmd.Context(), "PATCH", "/object-storage/buckets/"+url.PathEscape(args[0]), nil, patch, &b); err != nil {
				return err
			}
			return a.print(b, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s saved.", b.Name)
				if b.PublicURL != nil && b.Public {
					fmt.Fprintf(w, " Public at %s.", *b.PublicURL)
				}
				fmt.Fprintln(w)
			})
		},
	}
	cmd.Flags().BoolVar(&public, "public", false, "anyone may read its files")
	cmd.Flags().BoolVar(&protection, "deletion-protection", true, "protect it from deletion")
	return cmd
}

func storageDeleteCmd(a *App) *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "delete <bucket>",
		Short: "Delete a bucket (protection off, no service bound, name confirmed; restorable for a few days)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if confirm != args[0] {
				return fmt.Errorf("type the bucket's name to confirm: --confirm %s", args[0])
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var b api.Bucket
			if err := c.Do(cmd.Context(), "DELETE", "/object-storage/buckets/"+url.PathEscape(args[0]), url.Values{"confirm": {confirm}}, nil, &b); err != nil {
				return err
			}
			return a.print(b, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s deleted: its keys stopped at once; `mw storage restore %s` brings it back until %s.\n", b.Name, b.Name, whenPtr(b.PurgeAfter))
			})
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the bucket's name, to confirm")
	return cmd
}

func storageRestoreCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <bucket>",
		Short: "Bring back a deleted bucket, with its files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var b api.Bucket
			if err := c.Do(cmd.Context(), "POST", "/object-storage/buckets/"+url.PathEscape(args[0])+"/restore", nil, nil, &b); err != nil {
				return err
			}
			return a.print(b, func(w *tabwriter.Writer) { fmt.Fprintf(w, "%s restored.\n", b.Name) })
		},
	}
}

func storageLsCmd(a *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "ls <bucket>[/prefix]",
		Short:   "List a bucket's files, by folder",
		Example: "  mw storage ls loja-uploads\n  mw storage ls loja-uploads/produtos/",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, prefix, _ := strings.Cut(args[0], "/")
			c, err := a.Client()
			if err != nil {
				return err
			}
			var out api.ObjectListing
			q := url.Values{}
			if prefix != "" {
				q.Set("prefix", prefix)
			}
			for {
				var page api.ObjectListing
				if err := c.Do(cmd.Context(), "GET", "/object-storage/buckets/"+url.PathEscape(bucket)+"/objects", q, nil, &page); err != nil {
					return err
				}
				out.Prefixes = append(out.Prefixes, page.Prefixes...)
				out.Objects = append(out.Objects, page.Objects...)
				out.Cursor = page.Cursor
				if !all || page.Cursor == nil {
					break
				}
				q.Set("cursor", *page.Cursor)
			}
			return a.print(out, func(w *tabwriter.Writer) {
				for _, p := range out.Prefixes {
					fmt.Fprintf(w, "%s\t\t\n", p)
				}
				for _, o := range out.Objects {
					fmt.Fprintf(w, "%s\t%s\t%s\n", o.Key, bytesText(fmt.Sprint(o.Size)), whenPtr(o.LastModified))
				}
				if out.Cursor != nil {
					fmt.Fprintln(w, "… more: pass --all")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every page, not just the first")
	return cmd
}

func keysCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Access keys: their scope, expiry and last use (never their secret)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keys, err := a.keys(cmd.Context())
			if err != nil {
				return err
			}
			return a.print(keys, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tACCESS KEY ID\tACCESS\tBUCKETS\tEXPIRES\tLAST USED")
				for _, k := range keys {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.Name, k.AccessKeyID, keyAccess(k.Access), keyBuckets(k.Buckets), whenPtr(k.ExpiresAt), whenPtr(k.LastUsedAt))
				}
			})
		},
	}

	var readOnly bool
	var buckets []string
	var expires string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an access key; its secret is shown this once",
		Example: "  mw storage keys create backup-job --bucket loja-backups\n" +
			"  mw storage keys create cdn-reader --read-only --expires 2027-01-31",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]interface{}{"name": args[0], "access": "read_write"}
			if readOnly {
				body["access"] = "read_only"
			}
			if len(buckets) > 0 {
				body["buckets"] = buckets
			}
			if expires != "" {
				t, err := time.ParseInLocation("2006-01-02", expires, time.Local)
				if err != nil {
					return errors.New("--expires is a date, like 2027-01-31")
				}
				// The end of that day, where the user is.
				body["expires_at"] = t.Add(24*time.Hour - time.Second).UTC().Format(time.RFC3339)
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var k api.NewAccessKey
			if err := c.Do(cmd.Context(), "POST", "/object-storage/keys", nil, body, &k); err != nil {
				return err
			}
			return a.print(k, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Key %s (%s, %s).\n", k.Key.Name, keyAccess(k.Key.Access), keyBuckets(k.Key.Buckets))
				fmt.Fprintln(w, "Its secret is shown this once: keep it somewhere safe.")
				fmt.Fprintf(w, "  Access key ID\t%s\n", k.Key.AccessKeyID)
				fmt.Fprintf(w, "  Secret access key\t%s\n", k.SecretAccessKey)
			})
		},
	}
	create.Flags().BoolVar(&readOnly, "read-only", false, "read only (default: read and write)")
	create.Flags().StringArrayVar(&buckets, "bucket", nil, "a bucket it may use (repeat; default: all of them)")
	create.Flags().StringVar(&expires, "expires", "", "the last day it works, e.g. 2027-01-31")

	revoke := &cobra.Command{
		Use:   "revoke <name or access key id>",
		Short: "Revoke an access key: it stops working within seconds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := a.keys(cmd.Context())
			if err != nil {
				return err
			}
			var found []api.AccessKey
			for _, k := range keys {
				if k.ID == args[0] || k.AccessKeyID == args[0] || k.Name == args[0] {
					found = append(found, k)
				}
			}
			switch len(found) {
			case 0:
				return fmt.Errorf("no access key %s", args[0])
			case 1:
			default:
				return fmt.Errorf("%d keys are named %s: pass the access key id", len(found), args[0])
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/object-storage/keys/"+found[0].ID, nil, nil, nil); err != nil {
				return err
			}
			a.printf("Key %s (%s) revoked: it stops working within seconds.\n", found[0].Name, found[0].AccessKeyID)
			return nil
		},
	}
	cmd.AddCommand(create, revoke)
	return cmd
}

func (a *App) keys(ctx context.Context) ([]api.AccessKey, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var keys []api.AccessKey
	err = c.Do(ctx, "GET", "/object-storage/keys", nil, nil, &keys)
	return keys, err
}

func keyAccess(access string) string {
	if access == "read_only" {
		return "read only"
	}
	return "read and write"
}

func keyBuckets(b []string) string {
	if len(b) == 0 {
		return "all buckets"
	}
	return strings.Join(b, ", ")
}

func storageBindCmd(a *App) *cobra.Command {
	o := &bindOptions{}
	cmd := &cobra.Command{
		Use:   "bind <bucket>",
		Short: "Give a service this bucket as variables, with a key of its own",
		Long: "Bind a bucket to a service: it gets a key of its own (this bucket only) and the\n" +
			"variables its code reads. Layouts: prefixed (MW_STORAGE_*, the default; --prefix to\n" +
			"change it) or framework (AWS_*, as Laravel's s3 disk and the AWS SDKs read them).",
		Example: "  mw storage bind loja-uploads --service web --layout framework",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.bind(cmd, "BUCKET", args[0], o)
		},
	}
	o.flags(cmd, "prefixed or framework", true)
	return cmd
}

func storageUnbindCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "unbind <bucket>",
		Short: "Stop giving a service this bucket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.unbind(cmd, "BUCKET", args[0], service, project)
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}
