package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// snapshotNameRe is what the API takes as a snapshot's name.
var snapshotNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)

// snapshotsCmd: an instance's point-in-time copies, kept 30 days, restored in
// place.
func snapshotsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshots <instance>",
		Aliases: []string{"snapshot"},
		Short:   "Snapshots of an instance: take, restore, delete (kept 30 days)",
		Example: "  mw snapshots web-1\n" +
			"  mw snapshots create web-1 --name before-upgrade\n" +
			"  mw snapshots restore web-1 before-upgrade\n" +
			"  mw snapshots delete web-1 before-upgrade",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, list, err := a.snapshots(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "NAME", "WITH RAM", "TAKEN", "EXPIRES"}, empty: fmt.Sprintf("No snapshots of %s yet: `mw snapshots create %s --name …`.", instanceLabel(in), in.ID)}
			for _, s := range list.Data {
				l.add(s.ID, s.ID, s.Name, yesNo(s.IncludeRAM), when(s.CreatedAt), when(s.ExpiresAt))
			}
			if err := a.printList(list, l); err != nil {
				return err
			}
			if !a.JSON && !a.Quiet && list.Remaining != nil && list.MaxSnapshots != nil {
				fmt.Fprintf(a.Out, "%d of %d slots free.\n", *list.Remaining, *list.MaxSnapshots)
			}
			return nil
		},
	}

	var name, description string
	var ram bool
	create := &cobra.Command{
		Use:   "create <instance>",
		Short: "Take a snapshot now (it is ready when the command returns)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			if name == "" {
				name = "snap-" + time.Now().Format("20060102-1504")
			}
			if !snapshotNameRe.MatchString(name) {
				return errors.New("--name starts with a letter and has letters, digits, - or _ (2 to 40)")
			}
			body := map[string]interface{}{"name": name}
			if description != "" {
				body["description"] = description
			}
			if ram {
				body["include_ram"] = true
			}
			c, _ := a.Client()
			var s api.Snapshot
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/snapshots", nil, body, &s); err != nil {
				return notOffered(err, "Snapshots from the API")
			}
			if a.JSON {
				return a.encode(s)
			}
			fmt.Fprintf(a.Out, "Snapshot %s of %s taken (kept until %s).\n", s.Name, instanceLabel(in), when(s.ExpiresAt))
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "its name: letters, digits, - and _ (default: snap-<date>)")
	create.Flags().StringVar(&description, "description", "", "a note")
	create.Flags().BoolVar(&ram, "ram", false, "include the memory too (a running instance resumes where it was)")

	var yesRestore, waitRestore bool
	var timeoutRestore time.Duration
	restore := &cobra.Command{
		Use:   "restore <instance> <snapshot>",
		Short: "Put the instance back as it was at a snapshot (newer snapshots are deleted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, s, err := a.snapshot(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if !yesRestore {
				q := fmt.Sprintf("Restore %s to snapshot %s (%s)? What changed since is lost, and newer snapshots are deleted.\nType the snapshot's name (%s) to confirm: ", instanceLabel(in), s.Name, when(s.CreatedAt), s.Name)
				if err := a.typeToConfirm(q, s.Name, "--yes"); err != nil {
					return err
				}
			}
			c, _ := a.Client()
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/snapshots/"+s.ID+"/restore", nil, nil, &acc); err != nil {
				return notOffered(err, "Snapshots from the API")
			}
			if waitRestore {
				if err := a.waitAccepted(ctx, acc, timeoutRestore, func(ctx context.Context) error {
					_, err := a.waitInstance(ctx, in.ID, timeoutRestore, "idle", func(st api.InstanceStatus) bool { return !st.Busy })
					return err
				}); err != nil {
					return err
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			fmt.Fprintln(a.Out, acceptedText(acc, fmt.Sprintf("Restore of %s to %s", instanceLabel(in), s.Name)))
			return nil
		},
	}
	restore.Flags().BoolVarP(&yesRestore, "yes", "y", false, "restore without asking")
	waitFlags(restore, &waitRestore, &timeoutRestore, 30*time.Minute)

	var yesDelete bool
	remove := &cobra.Command{
		Use:     "delete <instance> <snapshot>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a snapshot (its slot is free again)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, s, err := a.snapshot(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if !yesDelete {
				ok, err := a.confirm(fmt.Sprintf("Delete snapshot %s of %s? [y/N] ", s.Name, instanceLabel(in)), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing deleted.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(ctx, "DELETE", "/instances/"+in.ID+"/snapshots/"+s.ID, nil, nil, nil); err != nil {
				return notOffered(err, "Snapshots from the API")
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": s.ID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Snapshot %s deleted.\n", s.Name)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yesDelete, "yes", "y", false, "delete without asking")

	cmd.AddCommand(create, restore, remove)
	return cmd
}

func (a *App) snapshots(ctx context.Context, ref string) (*api.Instance, *api.SnapshotList, error) {
	in, err := a.resolveInstance(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	c, _ := a.Client()
	var list api.SnapshotList
	if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/snapshots", nil, nil, &list); err != nil {
		return in, nil, notOffered(err, "Snapshots from the API")
	}
	return in, &list, nil
}

// snapshot finds one of an instance's snapshots by id or name.
func (a *App) snapshot(ctx context.Context, instance, ref string) (*api.Instance, *api.Snapshot, error) {
	in, list, err := a.snapshots(ctx, instance)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range list.Data {
		if s.ID == ref || s.Name == ref {
			s := s
			return in, &s, nil
		}
	}
	return in, nil, fmt.Errorf("%s has no snapshot %s (`mw snapshots %s` lists them)", instanceLabel(in), ref, in.ID)
}

// backupsCmd: an instance's backups (with the backup add-on, whose slots
// are how many are kept), taken now or on a schedule, restored onto it.
func backupsCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backups <instance>",
		Aliases: []string{"backup"},
		Short:   "Backups of an instance: list, take one now, restore, delete, schedule",
		Example: "  mw backups web-1\n" +
			"  mw backups run web-1 --wait\n" +
			"  mw backups restore web-1 latest\n" +
			"  mw backups schedule set web-1 --days mon,thu --time 03:30 --timezone America/Sao_Paulo",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, list, err := a.backups(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			l := listing{headers: []string{"ID", "TAKEN"}, empty: fmt.Sprintf("No backups of %s yet.", instanceLabel(in))}
			for _, b := range list.Data {
				l.add(b.ID, b.ID, whenPtr(b.CreatedAt))
			}
			if err := a.printList(list, l); err != nil {
				return err
			}
			if a.JSON || a.Quiet {
				return nil
			}
			if list.Eligible {
				fmt.Fprintf(a.Out, "%d of %s free.\n", list.AvailableSlots, plural(list.Slots, "backup slot", "backup slots"))
			} else {
				fmt.Fprintf(a.Out, "%s cannot be backed up: %s.\n", instanceLabel(in), strings.TrimSuffix(firstNonEmpty(str(list.Reason), "it needs the backup add-on"), "."))
				fmt.Fprintf(a.Out, "The backup add-on is in `mw instances resize-options %s`.\n", in.ID)
			}
			if list.Running {
				fmt.Fprintln(a.Out, "A backup is running now.")
			}
			return nil
		},
	}

	var wait bool
	var timeout time.Duration
	run := &cobra.Command{
		Use:   "run <instance>",
		Short: "Back the instance up now (it needs the backup add-on and a free slot)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in, err := a.resolveInstance(ctx, args[0])
			if err != nil {
				return err
			}
			// The backups it has now, to tell the new one apart.
			var before []api.Backup
			if wait {
				_, list, err := a.backups(ctx, in.ID)
				if err != nil {
					return err
				}
				before = list.Data
			}
			c, _ := a.Client()
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/instances/"+in.ID+"/backups", nil, nil, &acc); err != nil {
				return notOffered(err, "Backups from the API")
			}
			var taken *api.Backup
			if wait {
				if err := a.waitAccepted(ctx, acc, timeout, func(ctx context.Context) error {
					var err error
					taken, err = a.waitNewBackup(ctx, in, before, timeout)
					return err
				}); err != nil {
					return err
				}
			}
			switch {
			case a.JSON && taken != nil:
				return a.encode(taken)
			case a.JSON:
				return a.encode(acc)
			case a.Quiet:
				if taken != nil {
					fmt.Fprintln(a.Out, taken.ID)
				}
			case taken != nil:
				fmt.Fprintf(a.Out, "Backup of %s taken: %s (%s).\n", instanceLabel(in), taken.ID, whenPtr(taken.CreatedAt))
			case wait:
				fmt.Fprintf(a.Out, "Backup of %s: done.\n", instanceLabel(in))
			default:
				fmt.Fprintln(a.Out, acceptedText(acc, "Backup of "+instanceLabel(in)))
			}
			return nil
		},
	}
	waitFlags(run, &wait, &timeout, 2*time.Hour)

	var yesRestore, waitRestore bool
	var timeoutRestore time.Duration
	restore := &cobra.Command{
		Use:   "restore [<instance>] <backup-id|latest>",
		Short: "Overwrite an instance with one of its backups (what changed since is lost)",
		Long: "Restore a backup onto its instance: give the backup's id, or the instance and the\n" +
			"backup's id or `latest` (its newest).",
		Example: "  mw backups restore 8812 --wait\n" +
			"  mw backups restore web-1 latest",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var in *api.Instance
			backupID, what := args[0], "backup "+args[0]
			switch {
			case len(args) == 2:
				i, b, err := a.backup(ctx, args[0], args[1])
				if err != nil {
					return err
				}
				in, backupID, what = i, b.ID, "its backup of "+whenPtr(b.CreatedAt)
			case strings.EqualFold(args[0], "latest"):
				return errors.New("latest needs the instance: `mw backups restore <instance> latest`")
			default:
				// The backup says whose it is and when it was taken.
				i, b, err := a.backupByID(ctx, backupID)
				if err != nil {
					return err
				}
				if b != nil {
					in, what = i, "its backup of "+whenPtr(b.CreatedAt)
				}
			}
			if !yesRestore {
				q := fmt.Sprintf("Overwrite its instance with backup %s? What changed since it was taken is lost.\nType the backup id (%s) to confirm: ", backupID, backupID)
				expect := backupID
				if in != nil {
					q = fmt.Sprintf("Overwrite %s with %s? What changed since is lost.\nType the instance id (%s) to confirm: ", instanceLabel(in), what, in.ID)
					expect = in.ID
				}
				if err := a.typeToConfirm(q, expect, "--yes"); err != nil {
					return err
				}
			}
			c, err := a.Client()
			if err != nil {
				return err
			}
			var acc api.OperationAccepted
			if err := c.Do(ctx, "POST", "/backups/"+backupID+"/restore", nil, nil, &acc); err != nil {
				return notOffered(err, "Backups from the API")
			}
			// The instance it acts on: the one given, or the one the API names.
			instanceID, label := acc.InstanceID, "instance "+acc.InstanceID
			if in != nil {
				instanceID, label = in.ID, instanceLabel(in)
			}
			waited := false
			if waitRestore {
				if instanceID == "" {
					fmt.Fprintf(a.Err, "This API does not say which instance the restore acts on: to wait, give it (`mw backups restore <instance> %s --wait`).\n", backupID)
				} else {
					// The API answers once the restore has started: the
					// instance is busy until it ends.
					if err := a.waitAccepted(ctx, acc, timeoutRestore, func(ctx context.Context) error {
						_, err := a.waitInstance(ctx, instanceID, timeoutRestore, "restored", func(st api.InstanceStatus) bool { return !st.Busy })
						return err
					}); err != nil {
						return err
					}
					waited = true
				}
			}
			if a.JSON {
				return a.encode(acc)
			}
			text := "Restore of backup " + backupID
			if instanceID != "" {
				text = fmt.Sprintf("Restore of %s to %s", label, what)
			}
			if waited {
				fmt.Fprintf(a.Out, "%s: done.\n", text)
			} else {
				fmt.Fprintln(a.Out, acceptedText(acc, text))
			}
			return nil
		},
	}
	restore.Flags().BoolVarP(&yesRestore, "yes", "y", false, "restore without asking")
	waitFlags(restore, &waitRestore, &timeoutRestore, 2*time.Hour)

	var yesDelete bool
	remove := &cobra.Command{
		Use:     "delete [<instance>] <backup-id>",
		Aliases: []string{"remove", "rm"},
		Short:   "Delete a backup (its slot is free again)",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			backupID, what := args[0], "backup "+args[0]
			var in *api.Instance
			var b *api.Backup
			var err error
			if len(args) == 2 {
				in, b, err = a.backup(cmd.Context(), args[0], args[1])
			} else {
				in, b, err = a.backupByID(cmd.Context(), backupID)
			}
			if err != nil {
				return err
			}
			if b != nil {
				backupID, what = b.ID, fmt.Sprintf("the backup of %s taken %s", instanceLabel(in), whenPtr(b.CreatedAt))
			}
			if !yesDelete {
				ok, err := a.confirm(fmt.Sprintf("Delete %s? [y/N] ", what), "--yes")
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
			if err := c.Do(cmd.Context(), "DELETE", "/backups/"+backupID, nil, nil, nil); err != nil {
				return notOffered(err, "Backups from the API")
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"id": backupID, "deleted": true})
			}
			fmt.Fprintf(a.Out, "Backup %s deleted.\n", backupID)
			return nil
		},
	}
	remove.Flags().BoolVarP(&yesDelete, "yes", "y", false, "delete without asking")

	cmd.AddCommand(run, restore, remove, backupScheduleCmd(a))
	return cmd
}

func (a *App) backups(ctx context.Context, ref string) (*api.Instance, *api.BackupList, error) {
	in, err := a.resolveInstance(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	c, _ := a.Client()
	var list api.BackupList
	if err := c.Do(ctx, "GET", "/instances/"+in.ID+"/backups", nil, nil, &list); err != nil {
		return in, nil, notOffered(err, "Backups from the API")
	}
	return in, &list, nil
}

// backupByID reads one backup and the instance it is of. An API without
// GET /v1/backups/{id} answers nothing (nil, nil), not an error.
func (a *App) backupByID(ctx context.Context, id string) (*api.Instance, *api.Backup, error) {
	c, err := a.Client()
	if err != nil {
		return nil, nil, err
	}
	var b api.Backup
	if err := c.Do(ctx, "GET", "/backups/"+id, nil, nil, &b); err != nil {
		if routeMissing(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	in, err := a.resolveInstance(ctx, b.InstanceID)
	if err != nil {
		in = &api.Instance{ID: b.InstanceID}
	}
	return in, &b, nil
}

// backup finds one of an instance's backups by id, or its newest ("latest").
func (a *App) backup(ctx context.Context, instance, ref string) (*api.Instance, *api.Backup, error) {
	in, list, err := a.backups(ctx, instance)
	if err != nil {
		return nil, nil, err
	}
	if strings.EqualFold(ref, "latest") {
		if len(list.Data) == 0 {
			return in, nil, fmt.Errorf("%s has no backups yet", instanceLabel(in))
		}
		return in, &list.Data[0], nil // newest first
	}
	for i := range list.Data {
		if list.Data[i].ID == ref {
			return in, &list.Data[i], nil
		}
	}
	return in, nil, fmt.Errorf("%s has no backup %s (`mw backups %s` lists them)", instanceLabel(in), ref, in.ID)
}

// waitNewBackup waits for a backup run to end: the list no longer says one
// is running and holds a backup it did not hold before. The API answers the
// run before it starts (operation_id null), so this is how it is followed.
func (a *App) waitNewBackup(ctx context.Context, in *api.Instance, before []api.Backup, timeout time.Duration) (*api.Backup, error) {
	known := map[string]bool{}
	for _, b := range before {
		known[b.ID] = true
	}
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sawRunning, idle := false, 0
	for {
		var list api.BackupList
		err := c.Do(ctx, "GET", "/instances/"+in.ID+"/backups", nil, nil, &list)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				return nil, err
			}
		case list.Running:
			if !sawRunning && !a.JSON {
				fmt.Fprintf(a.Err, "  backup of %s: running\n", instanceLabel(in))
			}
			sawRunning = true
		default:
			for i := range list.Data {
				if !known[list.Data[i].ID] {
					return &list.Data[i], nil
				}
			}
			// Done running and nothing new, a few times over: it failed.
			if sawRunning {
				if idle++; idle >= 3 {
					return nil, fmt.Errorf("the backup of %s ended without a new backup: it may have failed (`mw backups %s` lists them)", instanceLabel(in), in.ID)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("no new backup of %s after %s: `mw backups %s` lists them", instanceLabel(in), timeout, in.ID)
		case <-time.After(pollEvery):
		}
	}
}

var (
	backupDays     = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	backupTimeRe   = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	backupZoneRe   = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+)*$`)
	backupDayNames = map[string]string{"sun": "sunday", "mon": "monday", "tue": "tuesday", "wed": "wednesday", "thu": "thursday", "fri": "friday", "sat": "saturday"}
)

// scheduleDays reads --days — day names (mon or monday), or daily, weekdays
// or weekends — into the API's day codes, in week order.
func scheduleDays(in []string) ([]string, error) {
	want := map[string]bool{}
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		switch d {
		case "daily", "everyday", "every-day", "all":
			for _, x := range backupDays {
				want[x] = true
			}
		case "weekdays":
			for _, x := range backupDays[1:6] {
				want[x] = true
			}
		case "weekends":
			want["sat"], want["sun"] = true, true
		default:
			short := d
			if len(d) > 3 {
				short = d[:3]
			}
			if full, ok := backupDayNames[short]; !ok || (d != short && d != full) {
				return nil, fmt.Errorf("--days: %q is not a day (sun…sat, or daily, weekdays, weekends)", raw)
			}
			want[short] = true
		}
	}
	var out []string
	for _, x := range backupDays {
		if want[x] {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--days needs at least one day (sun…sat, or daily, weekdays, weekends)")
	}
	return out, nil
}

// daysPhrase is "every day", "on weekdays" or "on mon, thu".
func daysPhrase(days []string) string {
	switch strings.Join(days, ",") {
	case strings.Join(backupDays, ","):
		return "every day"
	case strings.Join(backupDays[1:6], ","):
		return "on weekdays"
	}
	return "on " + strings.Join(days, ", ")
}

// noSchedule is the 404 of an instance without a backup schedule (an API
// without the route says "Cannot …" instead).
func noSchedule(err error) bool {
	var p *api.Problem
	return errors.As(err, &p) && p.Status == http.StatusNotFound && !routeMissing(err)
}

// backupScheduleCmd: automatic backups, on some days of the week at a time
// of day.
func backupScheduleCmd(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Automatic backups of an instance: on which days, at what time",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	get := &cobra.Command{
		Use:   "get <instance>",
		Short: "When the instance is backed up by itself",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var s api.BackupSchedule
			if err := c.Do(cmd.Context(), "GET", "/instances/"+in.ID+"/backup-schedule", nil, nil, &s); err != nil {
				if !noSchedule(err) {
					return notOffered(err, "Backup schedules from the API")
				}
				if a.JSON {
					return a.encode(nil)
				}
				fmt.Fprintf(a.Out, "%s has no automatic backups: `mw backups schedule set %s --days … --time …`.\n", instanceLabel(in), in.ID)
				return nil
			}
			var d detail
			d.add("Days", strings.TrimPrefix(daysPhrase(s.Days), "on "))
			d.add("Time", s.Time+" "+firstNonEmpty(str(s.Timezone), "UTC"))
			d.add("Last run", whenPtr(s.LastRunAt))
			return a.printDetail(s, d)
		},
	}
	var days []string
	var at, zone string
	set := &cobra.Command{
		Use:   "set <instance>",
		Short: "Back the instance up by itself on some days of the week, at a time of day",
		Example: "  mw backups schedule set web-1 --days daily --time 03:30 --timezone America/Sao_Paulo\n" +
			"  mw backups schedule set web-1 --days mon,thu --time 23:00",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wanted, err := scheduleDays(days)
			if err != nil {
				return err
			}
			if !backupTimeRe.MatchString(at) {
				return errors.New("--time is HH:MM, 24-hour (03:30, 23:00)")
			}
			if zone != "" && !backupZoneRe.MatchString(zone) {
				return errors.New("--timezone is an IANA zone, like America/Sao_Paulo")
			}
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			body := map[string]interface{}{"days": wanted, "time": at}
			if zone != "" {
				body["timezone"] = zone
			}
			c, _ := a.Client()
			var s api.BackupSchedule
			if err := c.Do(cmd.Context(), "PUT", "/instances/"+in.ID+"/backup-schedule", nil, body, &s); err != nil {
				return notOffered(err, "Backup schedules from the API")
			}
			if a.JSON {
				return a.encode(s)
			}
			fmt.Fprintf(a.Out, "%s is backed up %s at %s %s.\n", instanceLabel(in), daysPhrase(s.Days), s.Time, firstNonEmpty(str(s.Timezone), "UTC"))
			return nil
		},
	}
	set.Flags().StringSliceVar(&days, "days", nil, "sun…sat, comma-separated, or daily, weekdays, weekends")
	set.Flags().StringVar(&at, "time", "", "the time of day, HH:MM 24-hour")
	set.Flags().StringVar(&zone, "timezone", "", "the IANA time zone of --time, like America/Sao_Paulo (default UTC)")
	_ = set.MarkFlagRequired("days")
	_ = set.MarkFlagRequired("time")
	var yes bool
	remove := &cobra.Command{
		Use:     "delete <instance>",
		Aliases: []string{"remove", "off"},
		Short:   "Stop the automatic backups (the backups taken stay)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := a.resolveInstance(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Stop the automatic backups of %s? [y/N] ", instanceLabel(in)), "--yes")
				if err != nil || !ok {
					if err == nil {
						a.printf("Nothing changed.\n")
					}
					return err
				}
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/instances/"+in.ID+"/backup-schedule", nil, nil, nil); err != nil {
				if !noSchedule(err) {
					return notOffered(err, "Backup schedules from the API")
				}
				if !a.JSON {
					fmt.Fprintf(a.Out, "%s had no automatic backups.\n", instanceLabel(in))
					return nil
				}
			}
			if a.JSON {
				return a.encode(map[string]interface{}{"instance_id": in.ID, "scheduled": false})
			}
			fmt.Fprintf(a.Out, "Automatic backups of %s stopped.\n", instanceLabel(in))
			return nil
		},
	}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "stop without asking")
	cmd.AddCommand(get, set, remove)
	return cmd
}
