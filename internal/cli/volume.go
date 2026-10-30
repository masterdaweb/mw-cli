package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// settingsChange is what changing a service's settings answers: the service
// as saved, and the deployment applying it (nil when none starts).
type settingsChange struct {
	Service    api.Service     `json:"service"`
	Deployment *api.Deployment `json:"deployment"`
}

func volumeCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:     "volume",
		Aliases: []string{"volumes"},
		Short:   "A service's persistent volume: its use, backups, restores and files",
		Long: "A persistent volume is a disk that stays with a service across deployments and restarts,\n" +
			"mounted at the path you choose: uploads, files, an SQLite database. A service with a volume\n" +
			"runs one instance. It is backed up every day (kept 7 days); `mw volume backup` makes a backup\n" +
			"kept until you delete it. Paths in the volume are from its root: uploads/2026, not\n" +
			"/data/uploads.",
		Example: "  mw volume add --path /data --size 5\n" +
			"  mw volume\n" +
			"  mw volume backup --wait\n" +
			"  mw volume upload ./uploads --to uploads",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.showVolume(cmd.Context(), service, project)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.AddCommand(volumeShowCmd(a), volumeAddCmd(a), volumeGrowCmd(a), volumeRemoveCmd(a),
		volumeBackupsCmd(a), volumeBackupCmd(a), volumeRestoreCmd(a),
		volumeLsCmd(a), volumeDownloadCmd(a), volumeUploadCmd(a), volumeRmCmd(a))
	return cmd
}

func volumeShowCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Where the volume is mounted, its size and how full it is",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.showVolume(cmd.Context(), service, project)
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

func (a *App) showVolume(ctx context.Context, service, project string) error {
	s, err := a.resolveService(ctx, service, project)
	if err != nil {
		return err
	}
	mount := str(s.Settings.VolumeMountPath)
	var uncovered []string
	if s.Kind != "CRON" {
		uncovered = uncoveredVolumes(s.ImageVolumes, mount)
	}
	return a.print(s, func(w *tabwriter.Writer) {
		switch {
		case s.Settings.VolumeSizeGB != nil:
			fmt.Fprintf(w, "Service\t%s (%s)\n", s.Name, s.ProjectName)
			fmt.Fprintf(w, "Mounted at\t%s\n", mount)
			fmt.Fprintf(w, "Size\t%d GB\n", *s.Settings.VolumeSizeGB)
			fmt.Fprintf(w, "Use\t%s\n", usageText(s.VolumeUsage, s.Settings.VolumeSizeGB))
		case s.Kind == "CRON":
			fmt.Fprintf(w, "%s is a cron job: cron jobs have no volume (give one to the service that keeps the data).\n", s.Name)
		case len(uncovered) == 0:
			fmt.Fprintf(w, "%s has no volume: what it writes is lost when it restarts or sleeps. `mw volume add --path /data --size 5` gives it one.\n", s.Name)
		}
		for _, p := range uncovered {
			fmt.Fprintln(w, imageVolumeHint(s.Name, p, mount))
		}
	})
}

// usageText is how full a volume was when last measured: of the size bought
// (its filesystem, a little smaller, gives the percentage).
func usageText(u *api.VolumeUsage, sizeGB *int) string {
	if u == nil {
		return "not measured yet (it shows a few minutes after the service runs with the volume)"
	}
	of := bytesBrief(u.CapacityBytes)
	if sizeGB != nil {
		of = fmt.Sprintf("%d GB", *sizeGB)
	}
	text := bytesBrief(u.UsedBytes) + " used of " + of
	used, _ := strconv.ParseFloat(u.UsedBytes, 64)
	capacity, _ := strconv.ParseFloat(u.CapacityBytes, 64)
	if capacity > 0 {
		text += fmt.Sprintf(" (%.0f%%)", math.Min(100, math.Round(used/capacity*100)))
	}
	return text + ", measured " + when(u.MeasuredAt)
}

// bytesBrief is bytesText without a whole number's ".0": 10 GB, 2.1 GB.
func bytesBrief(raw string) string {
	return strings.Replace(bytesText(raw), ".0 ", " ", 1)
}

// uncoveredVolumes are the paths an image keeps data in that the volume
// mounted at mount ("": none) does not hold.
func uncoveredVolumes(declared []string, mount string) []string {
	var out []string
	for _, p := range declared {
		p = path.Clean(p)
		if mount != "" && (p == mount || strings.HasPrefix(p, strings.TrimSuffix(mount, "/")+"/")) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// imageVolumeHint says what is lost in a path the image keeps data in that no
// volume holds, and, for a service without one, how to give it one there.
func imageVolumeHint(service, dir, mount string) string {
	if mount != "" {
		return fmt.Sprintf("The image keeps data in %s, outside the volume at %s: what it writes there is lost on every restart.", dir, mount)
	}
	return fmt.Sprintf("The image keeps data in %s: without a volume there it is lost on every restart. Add one: `mw volume add --service %s --path %s --size 5`.", dir, service, dir)
}

// imageVolumeHints are imageVolumeHint for each path an image keeps data in
// that the volume at mount does not hold; none when the image cannot be read.
func (a *App) imageVolumeHints(ctx context.Context, image map[string]string, service, mount string) []string {
	c, err := a.Client()
	if err != nil {
		return nil
	}
	var info api.ImageInspection
	if err := c.Do(ctx, "POST", "/serverless/images/inspect", nil, image, &info); err != nil {
		return nil
	}
	var hints []string
	for _, p := range uncoveredVolumes(info.Volumes, mount) {
		hints = append(hints, imageVolumeHint(service, p, mount))
	}
	return hints
}

// parseVolume reads --volume PATH:GB.
func parseVolume(v string) (string, int, error) {
	i := strings.LastIndex(v, ":")
	size, err := strconv.Atoi(v[i+1:])
	if i <= 0 || err != nil || size < 1 {
		return "", 0, fmt.Errorf("--volume is PATH:GB, like /data:5 (not %q)", v)
	}
	return v[:i], size, nil
}

// oneInstance is what keeps a service at one instance, as a volume needs:
// web and private services scale to one at most and take a new version all
// at once; a worker runs one (a stopped one stays stopped).
func oneInstance(kind string, s api.ServiceSettings) map[string]interface{} {
	out := map[string]interface{}{}
	switch kind {
	case "WEB", "PRIVATE":
		out["max_instances"] = 1
		if s.MinInstances > 1 {
			out["min_instances"] = 1
		}
		if s.RolloutPercent != nil && *s.RolloutPercent > 0 {
			out["rollout_percent"] = nil
		}
	case "WORKER":
		if s.Instances > 1 {
			out["instances"] = 1
		}
	}
	return out
}

func volumeAddCmd(a *App) *cobra.Command {
	var service, project, mount string
	var size int
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Give a service a volume (made by a deployment; the service then runs one instance)",
		Long: "Give a web, private or worker service a persistent volume, mounted at --path, of --size GB\n" +
			"(it can grow later, never shrink). It is made by a deployment that starts now. A service with\n" +
			"a volume runs one instance: on each deployment the running version stops before the new one\n" +
			"starts (about half a minute down), so two never write to it at once.",
		Example: "  mw volume add --path /data --size 5\n" +
			"  mw volume add --service ghost --path /var/lib/ghost/content --size 10",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mount == "" || !cmd.Flags().Changed("size") {
				return errors.New("pass --path (where it is mounted, like /data) and --size (in GB)")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if s.Kind == "CRON" {
				return fmt.Errorf("%s is a cron job, which cannot have a volume: give one to the service that keeps the data", s.Name)
			}
			if s.Settings.VolumeSizeGB != nil {
				return fmt.Errorf("%s has a volume already (%d GB at %s): `mw volume grow --size <GB>` makes it bigger",
					s.Name, *s.Settings.VolumeSizeGB, str(s.Settings.VolumeMountPath))
			}
			settings := oneInstance(s.Kind, s.Settings)
			settings["volume_mount_path"] = mount
			settings["volume_size_gb"] = size
			c, _ := a.Client()
			var out settingsChange
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/settings", nil,
				map[string]interface{}{"settings": settings, "deploy": true}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				if d := out.Deployment; d != nil {
					fmt.Fprintf(w, "%s gets a %d GB volume at %s with deployment #%d (`mw deployments` follows it).\n", s.Name, size, mount, d.Number)
				} else {
					fmt.Fprintf(w, "%s gets a %d GB volume at %s with its first deployment.\n", s.Name, size, mount)
				}
				fmt.Fprintln(w, "It runs one instance from then on: each deployment stops the running version before the new one starts.")
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&mount, "path", "", "where the volume is mounted, an absolute path like /data")
	cmd.Flags().IntVar(&size, "size", 0, "its size in GB (it can grow later, never shrink)")
	return cmd
}

func volumeGrowCmd(a *App) *cobra.Command {
	var service, project string
	var size int
	cmd := &cobra.Command{
		Use:     "grow",
		Short:   "Make the volume bigger, at once and without a restart (it never shrinks)",
		Example: "  mw volume grow --size 20",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("size") {
				return errors.New("pass --size: the volume's new size, in GB")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			now := s.Settings.VolumeSizeGB
			if now == nil {
				return fmt.Errorf("%s has no volume: `mw volume add --path /data --size %d` gives it one", s.Name, size)
			}
			if size <= *now {
				return fmt.Errorf("a volume never shrinks: %s's has %d GB, so give --size more than %d", s.Name, *now, *now)
			}
			c, _ := a.Client()
			var out settingsChange
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/settings", nil,
				map[string]interface{}{"settings": map[string]interface{}{"volume_size_gb": size}, "deploy": false}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s's volume has %d GB now: applied at once, without a restart.\n", s.Name, size)
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().IntVar(&size, "size", 0, "the volume's new size in GB, more than it has")
	return cmd
}

func volumeRemoveCmd(a *App) *cobra.Command {
	var service, project, confirm string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Take the volume away and delete its data (a last backup is kept 7 days)",
		Long: "Deploy the service without its volume: once that version is live, the volume's data is\n" +
			"deleted. A last backup is made first and kept 7 days: `mw volume backups --deleted` lists it,\n" +
			"and `mw volume restore` brings it back, into this or another service of the project.",
		Example: "  mw volume remove --confirm web",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if s.Settings.VolumeSizeGB == nil {
				return fmt.Errorf("%s has no volume", s.Name)
			}
			mount := str(s.Settings.VolumeMountPath)
			if confirm != s.Name {
				return fmt.Errorf("removing the volume deletes the data at %s (a last backup is kept 7 days): confirm with --confirm %s", mount, s.Name)
			}
			c, _ := a.Client()
			var out settingsChange
			if err := c.Do(cmd.Context(), "PATCH", "/serverless/services/"+s.ID+"/settings", nil, map[string]interface{}{
				"settings":      map[string]interface{}{"volume_mount_path": nil, "volume_size_gb": nil},
				"deploy":        true,
				"delete_volume": true,
			}, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				if d := out.Deployment; d != nil {
					fmt.Fprintf(w, "%s is deployed without its volume (deployment #%d); the data at %s is deleted once that version is live.\n", s.Name, d.Number, mount)
				} else {
					fmt.Fprintf(w, "%s has no volume from its next deployment; the data at %s is deleted once that version is live.\n", s.Name, mount)
				}
				fmt.Fprintln(w, "A last backup is made first and kept 7 days: `mw volume backups --deleted` lists it.")
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&confirm, "confirm", "", "the service's name, to confirm")
	return cmd
}

// backupKinds are a backup's kinds in words.
var backupKinds = map[string]string{"DAILY": "daily", "MANUAL": "manual", "BEFORE_RESTORE": "before restore", "FINAL": "final"}

func backupKind(kind string) string {
	if k, ok := backupKinds[kind]; ok {
		return k
	}
	return strings.ToLower(kind)
}

// backupWhen is when a backup was taken: when it finished, else when asked.
func backupWhen(b api.VolumeBackup) string {
	return when(firstNonEmpty(str(b.CompletedAt), b.CreatedAt))
}

// backupKept is until when a backup is kept.
func backupKept(b api.VolumeBackup) string {
	if b.ExpiresAt == nil {
		return "you delete it"
	}
	return when(*b.ExpiresAt)
}

// volumeProject is the project whose volumes that went are listed: the one
// --project names alone, else the service's.
func (a *App) volumeProject(ctx context.Context, service, project string) (*api.Project, error) {
	if service == "" && project != "" {
		return a.resolveProject(ctx, project)
	}
	s, err := a.resolveService(ctx, service, project)
	if err != nil {
		return nil, err
	}
	return &api.Project{ID: s.ProjectID, Name: s.ProjectName}, nil
}

func volumeBackupsCmd(a *App) *cobra.Command {
	var service, project string
	var deleted bool
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "The volume's backups, newest first (--deleted: those of the project's volumes that went)",
		Long: "A volume is backed up every day (kept 7 days) and when asked (`mw volume backup`, kept until\n" +
			"deleted). --deleted lists the backups of the project's volumes that went — a deleted service's,\n" +
			"a volume taken away — restorable into a service of the project for 7 days; --project alone\n" +
			"names the project.",
		Example: "  mw volume backups\n  mw volume backups --deleted --project loja\n  mw volume backups delete <backup-id>",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var list api.List[api.VolumeBackup]
			l := listing{headers: []string{"WHEN", "KIND", "STATUS", "DATA", "KEPT UNTIL", "ID"}}
			if deleted {
				p, err := a.volumeProject(cmd.Context(), service, project)
				if err != nil {
					return err
				}
				if err := c.Do(cmd.Context(), "GET", "/serverless/projects/"+p.ID+"/volume-backups", nil, nil, &list); err != nil {
					return err
				}
				l.headers = []string{"WHEN", "SERVICE", "KIND", "STATUS", "DATA", "KEPT UNTIL", "ID"}
				l.empty = fmt.Sprintf("No backups of volumes that went in %s.", p.Name)
			} else {
				s, err := a.resolveService(cmd.Context(), service, project)
				if err != nil {
					return err
				}
				if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/volume/backups", nil, nil, &list); err != nil {
					return err
				}
				l.empty = fmt.Sprintf("No backups of %s's volume yet: the first is made overnight, or now with `mw volume backup`.", s.Name)
				if s.Settings.VolumeSizeGB == nil {
					l.empty = fmt.Sprintf("%s has no volume, nor backups of one (`mw volume backups --deleted` lists those of volumes that went).", s.Name)
				}
			}
			for _, b := range list.Data {
				row := []string{backupWhen(b)}
				if deleted {
					row = append(row, fmt.Sprintf("%s (%s)", b.ServiceName, b.MountPath))
				}
				row = append(row, backupKind(b.Kind), strings.ToLower(b.Status), sizePtr(b.SizeBytes), backupKept(b), b.ID)
				l.add(b.ID, row...)
			}
			return a.printList(list.Data, l)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().BoolVar(&deleted, "deleted", false, "the backups of the project's volumes that went, instead")
	cmd.AddCommand(volumeBackupDeleteCmd(a))
	return cmd
}

// sizePtr is a byte count the API may not know yet.
func sizePtr(raw *string) string {
	if raw == nil {
		return "—"
	}
	return bytesBrief(*raw)
}

func volumeBackupDeleteCmd(a *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <backup-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a backup (manual ones are kept until deleted, 10 a volume at most)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !uuidRe.MatchString(args[0]) {
				return errors.New("give the backup's id, as `mw volume backups` lists it")
			}
			if !yes {
				ok, err := a.confirm(fmt.Sprintf("Delete backup %s? It cannot be restored afterwards. [y/N] ", args[0]), "--yes")
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
			var b api.VolumeBackup
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/volume-backups/"+args[0], nil, nil, &b); err != nil {
				return err
			}
			return a.print(b, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Backup %s of %s's volume (%s, %s) deleted.\n", b.ID, b.ServiceName, backupKind(b.Kind), backupWhen(b))
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	return cmd
}

func volumeBackupCmd(a *App) *cobra.Command {
	var service, project string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "backup",
		Short:   "Back the volume up now (kept until you delete it; --wait waits until it is done)",
		Example: "  mw volume backup --wait",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := a.resolveService(ctx, service, project)
			if err != nil {
				return err
			}
			if s.Settings.VolumeSizeGB == nil {
				return fmt.Errorf("%s has no volume to back up", s.Name)
			}
			c, _ := a.Client()
			var b api.VolumeBackup
			if err := c.Do(ctx, "POST", "/serverless/services/"+s.ID+"/volume/backups", nil, nil, &b); err != nil {
				return err
			}
			if !wait {
				return a.print(b, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "Backing up %s's volume: `mw volume backups` shows it when it is done. It is kept until you delete it.\n", s.Name)
				})
			}
			a.printf("Backing up %s's volume…\n", s.Name)
			done, err := a.waitVolumeBackup(ctx, s, b, timeout)
			if err != nil {
				return err
			}
			return a.print(done, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Backup %s of %s's volume done (%s), kept until you delete it.\n", done.ID, s.Name, sizePtr(done.SizeBytes))
			})
		},
	}
	serviceFlag(cmd, &service, &project)
	waitFlags(cmd, &wait, &timeout, 2*time.Hour)
	return cmd
}

// waitVolumeBackup follows a backup in the volume's list until it is done or
// has failed.
func (a *App) waitVolumeBackup(ctx context.Context, s *api.Service, b api.VolumeBackup, timeout time.Duration) (*api.VolumeBackup, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stillText := func() error {
		return fmt.Errorf("the backup is still %s after %s: `mw volume backups` shows when it is done", strings.ToLower(b.Status), timeout)
	}
	last := ""
	for {
		if b.Status != last {
			last = b.Status
			a.printf("  %s\n", strings.ToLower(b.Status))
		}
		switch b.Status {
		case "COMPLETED":
			return &b, nil
		case "FAILED":
			return &b, fmt.Errorf("the backup of %s's volume failed: try again, and if it fails again tell support its id, %s", s.Name, b.ID)
		}
		select {
		case <-ctx.Done():
			return nil, stillText()
		case <-time.After(pollEvery):
		}
		var list api.List[api.VolumeBackup]
		if err := c.Do(ctx, "GET", "/serverless/services/"+s.ID+"/volume/backups", nil, nil, &list); err != nil {
			if ctx.Err() != nil {
				return nil, stillText()
			}
			return nil, err
		}
		for _, x := range list.Data {
			if x.ID == b.ID {
				b = x
			}
		}
	}
}

func volumeRestoreCmd(a *App) *cobra.Command {
	var service, project, confirm string
	var detach bool
	cmd := &cobra.Command{
		Use:   "restore <backup-id>",
		Short: "Replace the volume's data with a backup's (what it holds now is backed up first, kept 7 days)",
		Long: "Restore a backup into the service's volume: the backup's data is made into a volume while the\n" +
			"service keeps serving, then swapped in (about half a minute down, as at any deployment of a\n" +
			"service with a volume). What the volume holds now is backed up first and kept 7 days, so a\n" +
			"restore can be undone. A backup of another service of the project works too — one whose volume\n" +
			"went (`mw volume backups --deleted`), or a copy for staging; a service whose volume was taken\n" +
			"away gets one again, at the backup's path and size.",
		Example: "  mw volume restore 0193f0e2-5555-7000-8000-000000000001 --confirm web",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !uuidRe.MatchString(args[0]) {
				return errors.New("give the backup's id, as `mw volume backups` lists it")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if confirm != s.Name {
				return fmt.Errorf("restoring replaces the data on %s's volume (what it holds now is backed up first, kept 7 days): confirm with --confirm %s", s.Name, s.Name)
			}
			c, _ := a.Client()
			var d api.Deployment
			if err := c.Do(cmd.Context(), "POST", "/serverless/services/"+s.ID+"/volume/restore", nil,
				map[string]string{"backup_id": args[0], "confirm_name": confirm}, &d); err != nil {
				return err
			}
			if a.JSON || detach {
				return a.print(d, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "Restoring backup %s into %s's volume with deployment #%d (`mw deployments` follows it).\n", args[0], s.Name, d.Number)
				})
			}
			a.printf("Restoring backup %s into %s's volume (deployment #%d):\n", args[0], s.Name, d.Number)
			return a.followDeployment(cmd.Context(), &d)
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&confirm, "confirm", "", "the service's name, to confirm")
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for the restore to finish")
	return cmd
}

func volumeLsCmd(a *App) *cobra.Command {
	var service, project string
	cmd := &cobra.Command{
		Use:     "ls [path]",
		Short:   "List a folder of the volume (its root by default)",
		Example: "  mw volume ls\n  mw volume ls uploads/2026",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			q := url.Values{}
			if len(args) == 1 {
				q.Set("path", args[0])
			}
			c, _ := a.Client()
			var files api.VolumeFiles
			if err := c.Do(cmd.Context(), "GET", "/serverless/services/"+s.ID+"/volume/files", q, nil, &files); err != nil {
				return err
			}
			// Folders first, then by name.
			sort.SliceStable(files.Entries, func(i, j int) bool {
				x, y := files.Entries[i], files.Entries[j]
				if (x.Type == "dir") != (y.Type == "dir") {
					return x.Type == "dir"
				}
				return x.Name < y.Name
			})
			l := listing{headers: []string{"NAME", "SIZE", "MODIFIED"}, empty: files.Path + " is empty."}
			if files.Path == "" {
				l.empty = fmt.Sprintf("%s's volume is empty.", s.Name)
			}
			for _, e := range files.Entries {
				name, size := e.Name, bytesBrief(strconv.FormatInt(e.SizeBytes, 10))
				switch e.Type {
				case "dir":
					name, size = name+"/", "—"
				case "link":
					name += "@"
				}
				l.add(path.Join(files.Path, e.Name), name, size, when(e.ModifiedAt))
			}
			if err := a.printList(files, l); err != nil {
				return err
			}
			if files.Truncated && !a.JSON && !a.Quiet {
				fmt.Fprintln(a.Out, "The folder holds more than is listed: `mw volume download` gets all of it.")
			}
			return nil
		},
	}
	serviceFlag(cmd, &service, &project)
	return cmd
}

func volumeDownloadCmd(a *App) *cobra.Command {
	var service, project, file string
	cmd := &cobra.Command{
		Use:   "download [path]",
		Short: "Download a file or folder of the volume (all of it by default) as a .tar.gz",
		Long: "Download a file or folder of the volume, or all of it, as a .tar.gz read from the service's\n" +
			"instance as it goes (one is started while the service sleeps). It is saved here under the name\n" +
			"the API gives it, or as -f FILE (a folder: in it); -f - writes it to standard output.",
		Example: "  mw volume download\n" +
			"  mw volume download uploads -f uploads.tar.gz\n" +
			"  mw volume download -f - | tar -xzf - -C ./copy",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			q := url.Values{}
			what := "all of " + s.Name + "'s volume"
			if len(args) == 1 {
				q.Set("path", args[0])
				what = args[0] + " of " + s.Name + "'s volume"
			}
			c, _ := a.Client()
			route := "/serverless/services/" + s.ID + "/volume/archive"
			if file == "-" {
				_, err := c.Download(cmd.Context(), route, q, a.Out)
				return err
			}
			// Saved beside where it goes until it is whole: then under its name.
			dir, target := a.Dir, file
			if st, err := os.Stat(file); file != "" && err == nil && st.IsDir() {
				dir, target = file, ""
			} else if file != "" {
				dir = filepath.Dir(file)
			}
			part, err := os.CreateTemp(dir, ".mw-volume-*.part")
			if err != nil {
				return err
			}
			defer os.Remove(part.Name())
			a.printf("Downloading %s…\n", what)
			name, err := c.Download(cmd.Context(), route, q, part)
			if cerr := part.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if target == "" {
				target = filepath.Join(dir, firstNonEmpty(name, s.Name+"-volume.tar.gz"))
			}
			st, err := os.Stat(part.Name())
			if err != nil {
				return err
			}
			if err := os.Rename(part.Name(), target); err != nil {
				return err
			}
			shown := target
			if rel, err := filepath.Rel(a.Dir, target); err == nil && !strings.HasPrefix(rel, "..") {
				shown = rel
			}
			if a.JSON {
				return a.print(map[string]interface{}{"file": target, "bytes": st.Size()}, nil)
			}
			a.printf("Downloaded %s (%s).\n", shown, bytesText(fmt.Sprint(st.Size())))
			return nil
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVarP(&file, "file", "f", "", "where to save the .tar.gz (default: here, under the name the API gives it); - writes it to standard output")
	return cmd
}

// volumeHasFolder says whether the volume has a folder at where (a file
// there, or nothing, is not one).
func volumeHasFolder(ctx context.Context, a *App, serviceID, where string) (bool, error) {
	c, err := a.Client()
	if err != nil {
		return false, err
	}
	var files api.VolumeFiles
	err = c.Do(ctx, "GET", "/serverless/services/"+serviceID+"/volume/files", url.Values{"path": {where}}, nil, &files)
	var p *api.Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

func volumeUploadCmd(a *App) *cobra.Command {
	var service, project, to string
	var extract bool
	cmd := &cobra.Command{
		Use:   "upload <file|folder>",
		Short: "Upload a file or a folder into the volume (--extract: a .tar.gz, unpacked)",
		Long: "Upload into the volume through the service's instance (one is started while it sleeps). A file\n" +
			"is written at --to (default: its name, at the volume's root; a --to ending in /, or naming a\n" +
			"folder the volume has, is the folder to put it in). A folder's files and folders — not its links —\n" +
			"go into the folder --to (default: the root), packed as a .tar.gz on the way. --extract unpacks\n" +
			"a .tar.gz of yours into --to. What the volume has under the same names is replaced.",
		Example: "  mw volume upload app.db --to data/\n" +
			"  mw volume upload ./uploads --to uploads\n" +
			"  mw volume upload site.tar.gz --extract --to public",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			local := args[0]
			st, err := os.Stat(local)
			if err != nil {
				return err
			}
			if extract && st.IsDir() {
				return fmt.Errorf("--extract unpacks a .tar.gz, and %s is a folder: upload it without --extract", local)
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if s.Settings.VolumeSizeGB == nil {
				return fmt.Errorf("%s has no volume: `mw volume add --path /data --size 5` gives it one", s.Name)
			}
			body, as, contentType, files := local, "file", "application/octet-stream", 0
			dest := to
			switch {
			case st.IsDir():
				a.printf("Packing %s…\n", local)
				packed, n, err := packFolder(local)
				if err != nil {
					return err
				}
				defer os.Remove(packed)
				body, as, contentType, files = packed, "archive", "application/gzip", n
			case extract:
				as, contentType = "archive", "application/gzip"
			case dest == "" || strings.HasSuffix(dest, "/"):
				dest += filepath.Base(local)
			default:
				// A folder the volume has is where the file goes, under its own
				// name — as cp puts a file in a folder it is given.
				folder, err := volumeHasFolder(cmd.Context(), a, s.ID, dest)
				if err != nil {
					return err
				}
				if folder {
					dest += "/" + filepath.Base(local)
				}
			}
			sent, err := os.Stat(body)
			if err != nil {
				return err
			}
			size := bytesText(fmt.Sprint(sent.Size()))
			a.printf("Uploading %s (%s) to %s's volume…\n", local, size, s.Name)
			c, _ := a.Client()
			q := url.Values{"path": {dest}, "as": {as}}
			if err := c.SendFile(cmd.Context(), "PUT", "/serverless/services/"+s.ID+"/volume/files", q, body, contentType, nil); err != nil {
				return err
			}
			if a.JSON {
				return a.print(map[string]interface{}{"path": dest, "as": as, "bytes": sent.Size()}, nil)
			}
			into := "its root"
			if dest != "" {
				into = strings.TrimSuffix(dest, "/") + "/"
			}
			switch {
			case st.IsDir():
				a.printf("Uploaded %s (%s, %s packed) to %s's volume, into %s.\n", local, plural(files, "file", "files"), size, s.Name, into)
			case extract:
				a.printf("Unpacked %s (%s) in %s's volume, into %s.\n", local, size, s.Name, into)
			default:
				a.printf("Uploaded %s (%s) to %s's volume, at %s.\n", local, size, s.Name, dest)
			}
			return nil
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&to, "to", "", "where it goes in the volume: a file's path, or the folder a folder or .tar.gz goes into (default: the root)")
	cmd.Flags().BoolVar(&extract, "extract", false, "the file is a .tar.gz: unpack it into --to")
	return cmd
}

// packFolder writes a folder's files and folders, named from inside it, as a
// .tar.gz in the system's temporary directory (the caller removes it), and
// says how many files went in. Links, sockets and devices are left out.
func packFolder(dir string) (string, int, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", 0, err
	}
	out, err := os.CreateTemp("", "mw-volume-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	files, entries := 0, 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			h.Name += "/"
		}
		// Whose they were here means nothing there; the API keeps no owners.
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		h.ModTime = info.ModTime().Truncate(time.Second)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		entries++
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		files++
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && entries == 0 {
		err = fmt.Errorf("nothing to upload: %s is empty", dir)
	}
	if err != nil {
		os.Remove(out.Name())
		return "", 0, err
	}
	return out.Name(), files, nil
}

func volumeRmCmd(a *App) *cobra.Command {
	var service, project, confirm string
	cmd := &cobra.Command{
		Use:     "rm <path>",
		Short:   "Delete a file or folder from the volume (a folder with all it holds)",
		Example: "  mw volume rm uploads/tmp --confirm web",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.Trim(args[0], "/. ") == "" {
				return errors.New("give the file or folder to delete: `mw volume remove` takes the volume itself away")
			}
			s, err := a.resolveService(cmd.Context(), service, project)
			if err != nil {
				return err
			}
			if confirm != s.Name {
				return fmt.Errorf("deleting %s from %s's volume cannot be undone, short of restoring a backup: confirm with --confirm %s", args[0], s.Name, s.Name)
			}
			c, _ := a.Client()
			if err := c.Do(cmd.Context(), "DELETE", "/serverless/services/"+s.ID+"/volume/files", url.Values{"path": {args[0]}}, nil, nil); err != nil {
				return err
			}
			a.printf("%s deleted from %s's volume.\n", args[0], s.Name)
			return nil
		},
	}
	serviceFlag(cmd, &service, &project)
	cmd.Flags().StringVar(&confirm, "confirm", "", "the service's name, to confirm")
	return cmd
}
