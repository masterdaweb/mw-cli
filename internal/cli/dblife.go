package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// A SQL database's life beyond connecting: restoring it into a new one (to
// the latest moment, an instant, a restore point), marking restore points,
// putting it to sleep and waking it, a dump the database makes for you to
// download, and importing another database from its URL.

func dbPath(name string, rest ...string) string {
	return "/databases/" + url.PathEscape(name) + strings.Join(rest, "")
}

// waitFor prints a database's status until it reaches one of `want`.
func (a *App) waitFor(ctx context.Context, name, engine string, want []string, failed string) (*api.Database, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	last := ""
	for {
		d, err := a.database(ctx, name, engine)
		if err != nil {
			return nil, err
		}
		if d.Status != last {
			last = d.Status
			a.printf("  %s\n", dbStatus(d.Status))
		}
		for _, w := range want {
			if d.Status == w && !(w == "running" && d.ChangesPending) {
				return d, nil
			}
		}
		if d.Status == "failed" || d.Status == "deleted" || (d.Notice != nil && *d.Notice == "hibernate_failed") {
			return d, fmt.Errorf("%s %s: %s", d.Name, failed, str(d.Failure))
		}
		select {
		case <-ctx.Done():
			return d, fmt.Errorf("%s is still %s: `mw db get %s` follows it", name, dbStatus(last), name)
		case <-time.After(pollEvery):
		}
	}
}

func pointText(p api.DatabaseRestorePoint) string {
	switch p.Reason {
	case "release":
		if p.DeploymentNumber != nil {
			return fmt.Sprintf("before deployment #%d of %s", *p.DeploymentNumber, str(p.ServiceName))
		}
		return "before a release"
	case "deleted":
		return "before deletion"
	case "hibernated":
		return "before hibernating"
	case "import":
		return "before an import"
	}
	return "marked by hand"
}

func dbRestoreCmd(a *App) *cobra.Command {
	var to, at, point, size, location string
	var detach bool
	cmd := &cobra.Command{
		Use:   "restore <name> --to <new-name>",
		Short: "Restore a database into a new one: to now, an instant, or a restore point",
		Long: "Restore a PostgreSQL, MySQL or MariaDB database into a NEW database (never over it):\n" +
			"to the latest moment its backups hold (the default), to an instant (--time) or to a\n" +
			"restore point (--point, see `restore-points`). The new database has its own name,\n" +
			"address and owner password, the same database and user names; your services keep\n" +
			"using the original until you switch them (the console's restored database page).",
		Example: "  mw db restore loja-prod --to loja-ontem --time 2026-10-24T18:30:00-03:00\n" +
			"  mw db restore loja-prod --to loja-antes --point deploy-web-12\n" +
			"  mw db restore loja-prod --to loja-us --location dallas",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return errors.New("name the new database: --to <new-name>")
			}
			if at != "" && point != "" {
				return errors.New("restore to --time or to --point, not both")
			}
			body := map[string]interface{}{"name": to, "target": "latest"}
			if at != "" {
				t, err := time.Parse(time.RFC3339, at)
				if err != nil {
					return fmt.Errorf("--time is an instant with its zone, like 2026-10-24T18:30:00-03:00")
				}
				body["target"], body["time"] = "time", t.UTC().Format(time.RFC3339)
			}
			if point != "" {
				body["target"], body["point"] = "point", point
			}
			if size != "" {
				body["size"] = size
			}
			// Default: where the original runs.
			if location != "" {
				id, err := a.resolveLocation(cmd.Context(), dbLocations, "databases", location)
				if err != nil {
					return err
				}
				body["datacenter_id"] = id
			}
			c, _ := a.Client()
			var made api.CreatedDatabase
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/restore"), nil, body, &made); err != nil {
				return err
			}
			if a.JSON {
				return a.print(made, nil)
			}
			d := made.Database
			a.printf("Restoring %s into %s (%s).\n", args[0], d.Name, dbSizeText(d.Size))
			a.printCreatedPassword(made.Password, "mw db password "+d.Name)
			if detach {
				return nil
			}
			if _, err := a.waitFor(cmd.Context(), d.Name, "", []string{"running"}, "could not be restored"); err != nil {
				return err
			}
			a.printf("%s is restored: check it (`mw db connect %s`), then switch your services to it in the console.\n", d.Name, d.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the new database's name")
	cmd.Flags().StringVar(&at, "time", "", "an instant inside the backups' window (RFC 3339, with its zone)")
	cmd.Flags().StringVar(&point, "point", "", "a restore point's name (see `restore-points`)")
	cmd.Flags().StringVar(&size, "size", "", "a size of the same engine (default: the original's)")
	cmd.Flags().StringVar(&location, "location", "", "where the new database runs: a location's id, name or code (default: the original's; `mw regions`)")
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for it to be ready")
	return cmd
}

func dbRestorePointsCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "restore-points <name>",
		Aliases: []string{"points"},
		Short:   "The named instants a restore can stop at, newest first",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _ := a.Client()
			var points []api.DatabaseRestorePoint
			if err := c.Do(cmd.Context(), "GET", dbPath(args[0], "/restore-points"), nil, nil, &points); err != nil {
				return err
			}
			return a.print(points, func(w *tabwriter.Writer) {
				if len(points) == 0 {
					fmt.Fprintln(w, "No restore points yet: `mw db mark` makes one.")
					return
				}
				fmt.Fprintln(w, "POINT\tWHAT\tWHEN")
				for _, p := range points {
					fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, pointText(p), whenPtr(&p.CreatedAt))
				}
			})
		},
	}
}

func dbMarkCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "mark <name>",
		Short: "Mark this instant, to restore to it later (before a risky change)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _ := a.Client()
			var p api.DatabaseRestorePoint
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/restore-points"), nil, nil, &p); err != nil {
				return err
			}
			return a.print(p, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Marked %s at %s: `mw db restore %s --to <new-name> --point %s` stops exactly here.\n",
					p.Name, whenPtr(&p.CreatedAt), args[0], p.Name)
			})
		},
	}
}

func dbHibernateCmd(a *App) *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "hibernate <name>",
		Short: "Put a database to sleep: only its backups are billed until it wakes",
		Long: "Closed to connections, a last full backup and a restore point at its end, then its\n" +
			"machine released. It keeps its name, address, password and settings; bound services\n" +
			"cannot reach it until `mw db wake`. For staging and paused projects.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _ := a.Client()
			var d api.Database
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/hibernate"), nil, nil, &d); err != nil {
				return err
			}
			if a.JSON {
				return a.print(d, nil)
			}
			a.printf("%s is going to sleep.\n", d.Name)
			if detach {
				return nil
			}
			out, err := a.waitFor(cmd.Context(), d.Name, "", []string{"hibernated"}, "could not hibernate")
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("%s is asleep: its backups (%s) are all it is billed for", out.Name, bytesText(out.Hibernation.BackupBytes))
			if out.Hibernation.MonthlyMicroCents != nil {
				msg += fmt.Sprintf(", about %s a month", money(*out.Hibernation.MonthlyMicroCents, 2))
			}
			a.printf("%s. `mw db wake %s` wakes it.\n", msg, out.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for it to be asleep")
	return cmd
}

func dbWakeCmd(a *App) *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "wake <name>",
		Short: "Wake a hibernated database: its backups restored on a new machine, same name and password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _ := a.Client()
			var d api.Database
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/wake"), nil, nil, &d); err != nil {
				return err
			}
			if a.JSON {
				return a.print(d, nil)
			}
			a.printf("%s is waking.\n", d.Name)
			if detach {
				return nil
			}
			if _, err := a.waitFor(cmd.Context(), d.Name, "", []string{"running"}, "could not wake"); err != nil {
				return err
			}
			a.printf("%s is awake: `mw db connect %s`.\n", d.Name, d.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for it to be awake")
	return cmd
}

func jobState(j api.DatabaseJob) string {
	switch j.Status {
	case "succeeded":
		if j.Kind == "export" {
			return "ready, " + bytesText(str(j.SizeBytes)) + ", until " + whenPtr(j.ExpiresAt)
		}
		if j.Report != nil {
			return fmt.Sprintf("done, %d table(s)", j.Report.Tables)
		}
		return "done"
	case "failed":
		return "failed: " + str(j.Error)
	}
	return j.Status
}

func dbJobsCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "jobs <name>",
		Short: "A database's dumps and imports, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobs, err := a.jobs(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.print(jobs, func(w *tabwriter.Writer) {
				if len(jobs) == 0 {
					fmt.Fprintln(w, "No dumps or imports yet.")
					return
				}
				fmt.Fprintln(w, "ID\tKIND\tSTARTED\tSTATE")
				for _, j := range jobs {
					kind := "dump"
					if j.Kind == "import" {
						kind = "import from " + str(j.Source)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", j.ID, kind, whenPtr(&j.CreatedAt), jobState(j))
				}
			})
		},
	}
}

func (a *App) jobs(ctx context.Context, name string) ([]api.DatabaseJob, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var jobs []api.DatabaseJob
	return jobs, c.Do(ctx, "GET", dbPath(name, "/jobs"), nil, nil, &jobs)
}

// waitJob follows one job until it ends.
func (a *App) waitJob(ctx context.Context, name, id string) (*api.DatabaseJob, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	last := ""
	for {
		jobs, err := a.jobs(ctx, name)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			j := &jobs[i]
			if j.ID != id {
				continue
			}
			if j.Status != last {
				last = j.Status
				a.printf("  %s\n", j.Status)
			}
			switch j.Status {
			case "succeeded":
				return j, nil
			case "failed", "expired":
				return j, fmt.Errorf("%s", jobState(*j))
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("still %s: `mw db jobs %s` follows it", last, name)
		case <-time.After(pollEvery):
		}
	}
}

func dbDumpCmd(a *App) *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "dump <name> [file]",
		Short: "Have the database make a dump, and download it (nothing to install)",
		Long: "The database makes a dump of itself (PostgreSQL: its custom format, for pg_restore;\n" +
			"MySQL and MariaDB: gzipped SQL), one consistent snapshot while it keeps serving, kept\n" +
			"7 days; then it is downloaded here. `mw db export` makes one with your own tools instead.",
		Example: "  mw db dump loja-prod\n  mw db dump loja-prod backup.dump",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _ := a.Client()
			var job api.DatabaseJob
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/exports"), nil, nil, &job); err != nil {
				return err
			}
			if detach {
				return a.print(job, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "The dump of %s is being made: `mw db dump %s` downloads it when ready.\n", args[0], args[0])
				})
			}
			a.printf("Making the dump of %s…\n", args[0])
			done, err := a.waitJob(cmd.Context(), args[0], job.ID)
			if err != nil {
				return err
			}
			var link api.DatabaseExportLink
			if err := c.Do(cmd.Context(), "POST", dbPath(args[0], "/exports/", url.PathEscape(done.ID), "/link"), nil, nil, &link); err != nil {
				return err
			}
			file := link.Filename
			if len(args) == 2 {
				file = args[1]
			}
			n, err := download(cmd.Context(), link.URL, file)
			if err != nil {
				return err
			}
			if a.JSON {
				return a.print(map[string]interface{}{"file": file, "bytes": n, "job": done}, nil)
			}
			a.printf("Downloaded %s (%s).\n", file, bytesText(fmt.Sprint(n)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", false, "ask for the dump and return; run again to download it")
	return cmd
}

// download writes a link's body to a file, beside it until complete.
func download(ctx context.Context, link, file string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return 0, err
	}
	// No timeout of its own: a dump takes as long as it is big.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return 0, fmt.Errorf("the download was refused (%d): %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	part := file + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && res.ContentLength >= 0 && n != res.ContentLength {
		err = fmt.Errorf("the download stopped at %d of %d bytes", n, res.ContentLength)
	}
	if err != nil {
		os.Remove(part)
		return n, err
	}
	return n, os.Rename(part, file)
}

// importFromURL is `mw db import <name> --from-url <url>`: the database
// reads the source itself.
func (a *App) importFromURL(ctx context.Context, name, source string, replace, detach bool) error {
	c, err := a.Client()
	if err != nil {
		return err
	}
	var job api.DatabaseJob
	if err := c.Do(ctx, "POST", dbPath(name, "/import"), nil, map[string]interface{}{"source_url": source, "replace": replace}, &job); err != nil {
		return err
	}
	if detach {
		return a.print(job, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "Importing %s into %s: `mw db jobs %s` follows it.\n", str(job.Source), name, name)
		})
	}
	a.printf("Importing %s into %s (a restore point was marked before it, to undo it)…\n", str(job.Source), name)
	done, err := a.waitJob(ctx, name, job.ID)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.print(done, nil)
	}
	if r := done.Report; r != nil {
		a.printf("Imported: %d table(s) in %s.\n", r.Tables, name)
		if r.LeftOut > 0 {
			a.printf("%d statement(s) could not run and were left out:\n", r.LeftOut)
			for _, n := range r.Notes {
				a.printf("  %s\n", n)
			}
		}
		if r.DefinersRemoved > 0 {
			a.printf("Left out %d DEFINER clause(s): those views, routines and triggers now run as the owner.\n", r.DefinersRemoved)
		}
	}
	return nil
}
