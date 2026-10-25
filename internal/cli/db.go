package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/spf13/cobra"
)

// engineName is what a person calls an engine.
func engineName(engine string) string {
	switch engine {
	case "valkey":
		return "Redis"
	case "mysql":
		return "MySQL"
	case "mariadb":
		return "MariaDB"
	}
	return "PostgreSQL"
}

// engines are what `mw db create --engine` takes.
var engines = []string{"postgres", "mysql", "mariadb", "valkey"}

// group is the command a database's engine is under: `mw redis` for Redis,
// `mw db` for the SQL databases.
func group(engine string) string {
	if engine == "valkey" {
		return "redis"
	}
	return "db"
}

// isMySQL: MySQL and MariaDB share their clients, their dumps and their URLs.
func isMySQL(engine string) bool { return engine == "mysql" || engine == "mariadb" }

func dbStatus(s string) string {
	switch s {
	case "updating":
		return "applying changes"
	case "hibernating":
		return "going to sleep"
	}
	return s
}

// dbCmd is `mw db` (every engine; PostgreSQL by default) and `mw redis`
// (engine valkey): the same commands, one engine's databases.
func dbCmd(a *App, use, engine string) *cobra.Command {
	kind := "database"
	if engine == "valkey" {
		kind = "Redis database"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: "Managed PostgreSQL, MySQL, MariaDB and Redis: create, connect, resize, bind to services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := a.databases(cmd.Context(), engine)
			if err != nil {
				return err
			}
			return a.print(all, func(w *tabwriter.Writer) {
				fmt.Fprintln(w, "NAME\tENGINE\tSIZE\tSTATUS\tTHIS MONTH")
				for _, d := range all {
					fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\t%s\n", d.Name, engineName(d.Engine), d.Version,
						dbSizeText(d.Size), dbStatus(d.Status), money(d.Cost.MonthToDateMicroCents, 2))
				}
			})
		},
	}
	if engine == "valkey" {
		cmd.Short = "Managed Redis (Valkey): create, connect, resize, bind to services"
	}
	cmd.AddCommand(
		dbSizesCmd(a, engine),
		dbCreateCmd(a, engine, kind),
		dbGetCmd(a, engine),
		dbPasswordCmd(a, engine),
		dbURLCmd(a, engine),
		dbConnectCmd(a, engine),
		dbAllowCmd(a, engine, true),
		dbAllowCmd(a, engine, false),
		dbUpdateCmd(a, engine),
		dbResizeCmd(a, engine),
		dbRetryCmd(a, engine),
		dbDeleteCmd(a, engine),
		dbBindCmd(a, engine),
		dbUnbindCmd(a, engine),
		dbCACmd(a),
		dbTunnelCmd(a, engine),
	)
	if engine != "valkey" {
		cmd.AddCommand(dbExtensionsCmd(a), dbExportCmd(a), dbImportCmd(a),
			dbRestoreCmd(a), dbRestorePointsCmd(a), dbMarkCmd(a), dbHibernateCmd(a), dbWakeCmd(a), dbDumpCmd(a), dbJobsCmd(a))
	}
	return cmd
}

func dbSizeText(s api.DatabaseSize) string {
	return fmt.Sprintf("%d vCPU · %s · %d GB", s.CPUs, memText(s.MemoryMB), s.DiskGB)
}

func memText(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + " GB"
	}
	return strconv.Itoa(mb) + " MB"
}

func (a *App) databases(ctx context.Context, engine string) ([]api.Database, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var all []api.Database
	if err := c.Do(ctx, "GET", "/databases", nil, nil, &all); err != nil {
		return nil, err
	}
	if engine == "" {
		return all, nil
	}
	var out []api.Database
	for _, d := range all {
		if d.Engine == engine {
			out = append(out, d)
		}
	}
	return out, nil
}

// database reads one by name, and refuses one of another engine under
// `mw redis` (or a Redis database under a PostgreSQL-only command).
func (a *App) database(ctx context.Context, name, engine string) (*api.Database, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var d api.Database
	if err := c.Do(ctx, "GET", "/databases/"+url.PathEscape(name), nil, nil, &d); err != nil {
		return nil, err
	}
	if engine != "" && d.Engine != engine {
		return nil, fmt.Errorf("%s is a %s database: use `mw %s`", d.Name, engineName(d.Engine), group(d.Engine))
	}
	return &d, nil
}

func (a *App) databasesInfo(ctx context.Context) (*api.DatabasesInfo, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var info api.DatabasesInfo
	if err := c.Do(ctx, "GET", "/databases/info", nil, nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func dbSizesCmd(a *App, engine string) *cobra.Command {
	return &cobra.Command{
		Use:   "sizes",
		Short: "The sizes on offer, with their prices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := a.databasesInfo(cmd.Context())
			if err != nil {
				return err
			}
			var engines []api.DatabaseEngineInfo
			for _, e := range info.Engines {
				if engine == "" || e.Engine == engine {
					engines = append(engines, e)
				}
			}
			return a.print(engines, func(w *tabwriter.Writer) {
				for i, e := range engines {
					if i > 0 {
						fmt.Fprintln(w)
					}
					fmt.Fprintf(w, "%s (versions %s)\n", engineName(e.Engine), seriesList(e.Versions))
					fmt.Fprintln(w, "SIZE\tCPU\tMEMORY\tDISK\tPER HOUR\tPER MONTH")
					for _, s := range e.Sizes {
						fmt.Fprintf(w, "%s\t%d vCPU\t%s\t%d GB\t%s\t%s\n", s.Code, s.CPUs, memText(s.MemoryMB), s.DiskGB,
							moneyPtr(s.HourlyMicroCents, 4), moneyPtr(s.MonthlyMicroCents, 2))
					}
				}
				if !info.Available {
					fmt.Fprintln(w, "\nDatabases cannot be created yet: no price is published.")
				}
			})
		},
	}
}

func dbCreateCmd(a *App, engine, kind string) *cobra.Command {
	var size, project, eviction, version string
	var allow, extensions []string
	var noBackups, noProtection, detach bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a " + kind + " (ready in a few minutes; billed by the hour)",
		Example: map[string]string{
			"":       "  mw db create loja-prod --size pg-1-1 --extension vector\n  mw db create loja-wp --engine mysql\n  mw db create erp --engine mariadb --version 11.4",
			"valkey": "  mw redis create filas --size kv-1-1",
		}[engine],
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng := engine
			if eng == "" {
				eng, _ = cmd.Flags().GetString("engine")
			}
			if !contains(engines, eng) {
				return fmt.Errorf("--engine is one of %s", strings.Join(engines, ", "))
			}
			if len(extensions) > 0 && eng != "postgres" {
				return errors.New("--extension is for PostgreSQL databases")
			}
			info, err := a.databasesInfo(cmd.Context())
			if err != nil {
				return err
			}
			var offer *api.DatabaseEngineInfo
			for i := range info.Engines {
				if info.Engines[i].Engine == eng {
					offer = &info.Engines[i]
				}
			}
			if offer == nil || len(offer.Sizes) == 0 {
				return fmt.Errorf("%s databases are not on offer here", engineName(eng))
			}
			if size == "" {
				size = offer.Sizes[0].Code
			}
			body := map[string]interface{}{"name": args[0], "engine": eng, "size": size}
			if version != "" {
				body["version"] = version
			}
			if project != "" {
				p, err := a.resolveProject(cmd.Context(), project)
				if err != nil {
					return err
				}
				body["project_id"] = p.ID
			}
			if len(allow) > 0 {
				body["allowed_cidrs"] = allow
			}
			if len(extensions) > 0 {
				body["extensions"] = extensions
			}
			if eviction != "" {
				body["eviction_policy"] = eviction
			}
			if noBackups {
				body["backups"] = false
			}
			if noProtection {
				body["deletion_protection"] = false
			}
			c, _ := a.Client()
			var made api.CreatedDatabase
			if err := c.Do(cmd.Context(), "POST", "/databases", nil, body, &made); err != nil {
				return err
			}
			if a.JSON {
				return a.print(made, nil)
			}
			d := made.Database
			a.printf("%s %s is being created (%s).\n", engineName(d.Engine), d.Name, dbSizeText(d.Size))
			a.printCreatedPassword(made.Password, fmt.Sprintf("mw %s password %s", group(d.Engine), d.Name))
			if detach {
				return nil
			}
			return a.waitDatabase(cmd.Context(), d.Name, eng)
		},
	}
	if engine == "" {
		cmd.Flags().String("engine", "postgres", "postgres, mysql, mariadb or valkey (Redis)")
	}
	cmd.Flags().StringVar(&size, "size", "", "a size from `sizes` (default: the smallest)")
	cmd.Flags().StringVar(&version, "version", "", "a release series from `sizes`, e.g. 18 or 11.4 (default: the newest)")
	cmd.Flags().StringVar(&project, "project", "", "the project it belongs to")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "an address or CIDR allowed to connect, besides your services (repeat)")
	if engine != "valkey" {
		cmd.Flags().StringArrayVar(&extensions, "extension", nil, "a PostgreSQL extension to enable, e.g. vector (repeat)")
		cmd.Flags().BoolVar(&noBackups, "no-backups", false, "no continuous backups (no point-in-time restore)")
	}
	if engine != "postgres" {
		cmd.Flags().StringVar(&eviction, "eviction-policy", "", "Redis: what happens when memory is full (default noeviction)")
	}
	cmd.Flags().BoolVar(&noProtection, "no-deletion-protection", false, "create it unprotected from deletion")
	cmd.Flags().BoolVar(&detach, "detach", false, "do not wait for it to be ready")
	return cmd
}

// waitDatabase prints a new database's status until it runs or fails.
func (a *App) waitDatabase(ctx context.Context, name, engine string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	last := ""
	for {
		d, err := a.database(ctx, name, engine)
		if err != nil {
			return err
		}
		if d.Status != last {
			last = d.Status
			a.printf("  %s\n", dbStatus(d.Status))
		}
		switch d.Status {
		case "running":
			if !d.ChangesPending {
				a.printf("%s is ready: `mw %s connect %s`.\n", d.Name, group(d.Engine), d.Name)
				return nil
			}
		case "failed":
			return fmt.Errorf("%s could not be created: %s", d.Name, str(d.Failure))
		case "deleted":
			return fmt.Errorf("%s was deleted", d.Name)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s is still %s: `mw db get %s` follows it", name, dbStatus(last), name)
		case <-time.After(pollEvery):
		}
	}
}

func dbGetCmd(a *App, engine string) *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Aliases: []string{"status", "info"},
		Short:   "A database: its status, size, how to connect, health and backups",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			return a.print(d, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "Database\t%s (%s %s)\n", d.Name, engineName(d.Engine), d.Version)
				status := dbStatus(d.Status)
				if d.ChangesPending && d.Status == "running" {
					status += ", changes being applied"
				}
				fmt.Fprintf(w, "Status\t%s\n", status)
				if d.Failure != nil {
					fmt.Fprintf(w, "Failure\t%s\n", *d.Failure)
				}
				if hb := d.Hibernation; hb != nil {
					switch {
					case hb.Waking:
						fmt.Fprintln(w, "Waking\tits backups are being restored on a new machine")
					case hb.Since != nil:
						asleep := "since " + whenPtr(hb.Since) + ", its backups " + bytesText(hb.BackupBytes)
						if hb.MonthlyMicroCents != nil {
							asleep += " (" + money(*hb.MonthlyMicroCents, 2) + " a month)"
						}
						fmt.Fprintf(w, "Asleep\t%s · `mw db wake %s`\n", asleep, d.Name)
					}
				}
				if r := d.Restore; r != nil {
					target := "the latest moment"
					if r.Target == "time" {
						target = whenPtr(r.Time)
					} else if r.Target == "point" {
						target = "the point " + str(r.Point)
					}
					state := "being restored"
					if r.RestoredAt != nil {
						state = "restored " + whenPtr(r.RestoredAt)
					}
					fmt.Fprintf(w, "Restored from\t%s, to %s (%s)\n", str(r.SourceName), target, state)
				}
				fmt.Fprintf(w, "Size\t%s (%s)\n", dbSizeText(d.Size), d.Size.Code)
				fmt.Fprintf(w, "Host\t%s\n", d.Connection.Host)
				if d.Connection.PoolerPort != nil {
					fmt.Fprintf(w, "Ports\t%d (pooler, for apps) · %d (direct, for migrations)\n", *d.Connection.PoolerPort, d.Connection.Port)
				} else {
					fmt.Fprintf(w, "Port\t%d (TLS)\n", d.Connection.Port)
				}
				if d.Connection.Database != nil {
					fmt.Fprintf(w, "Database name\t%s\n", *d.Connection.Database)
				}
				fmt.Fprintf(w, "User\t%s\n", d.Connection.Username)
				fmt.Fprintf(w, "URL\t%s\n", d.Connection.URI)
				if len(d.AllowedCIDRs) > 0 {
					fmt.Fprintf(w, "Also allowed\t%s\n", strings.Join(d.AllowedCIDRs, ", "))
				}
				if len(d.Extensions) > 0 {
					fmt.Fprintf(w, "Extensions\t%s\n", strings.Join(d.Extensions, ", "))
				}
				if d.EvictionPolicy != nil {
					fmt.Fprintf(w, "Eviction\t%s\n", *d.EvictionPolicy)
				}
				h := d.Health
				health := h.State
				if h.Connections != nil && h.MaxConnections != nil {
					health += fmt.Sprintf(" · %d of %d connections", *h.Connections, *h.MaxConnections)
				}
				if h.DiskUsedBytes != nil && h.DiskTotalBytes != nil {
					health += " · disk " + bytesText(*h.DiskUsedBytes) + " of " + bytesText(*h.DiskTotalBytes)
				}
				fmt.Fprintf(w, "Health\t%s\n", health)
				if b := d.Backups; b != nil {
					fmt.Fprintf(w, "Last backup\t%s\n", whenPtr(b.LastBackupAt))
					if b.EarliestRestoreAt != nil {
						fmt.Fprintf(w, "Restorable from\t%s\n", whenPtr(b.EarliestRestoreAt))
					}
				} else if d.Engine != "valkey" && !d.BackupsEnabled {
					fmt.Fprintln(w, "Backups\toff")
				}
				protection := "on"
				if !d.DeletionProtection {
					protection = "off"
				}
				fmt.Fprintf(w, "Deletion protection\t%s\n", protection)
				m := d.Maintenance
				window := "every day"
				if m.Day != nil && *m.Day >= 0 && *m.Day < 7 {
					window = weekdays[*m.Day]
				}
				fmt.Fprintf(w, "Maintenance\t%s %02d:00 UTC (next %s)\n", window, m.HourUTC, whenPtr(&m.NextAt))
				if m.ScheduledSize != nil || m.ScheduledDiskGB != nil {
					fmt.Fprintf(w, "Scheduled\tresize to %s in the window\n", str(m.ScheduledSize))
				}
				if m.PendingUpdate != nil {
					fmt.Fprintln(w, "Update\ta minor update in the window (a short restart)")
				}
				if d.Disk.AutogrowMaxGB != nil {
					fmt.Fprintf(w, "Disk\tgrows by itself past 85%%, up to %d GB\n", *d.Disk.AutogrowMaxGB)
				}
				if len(d.Services) > 0 {
					names := make([]string, 0, len(d.Services))
					for _, s := range d.Services {
						names = append(names, s.Name)
					}
					fmt.Fprintf(w, "Services\t%s\n", strings.Join(names, ", "))
				}
				fmt.Fprintf(w, "This month\t%s\n", money(d.Cost.MonthToDateMicroCents, 2))
				if d.PurgeAfter != nil {
					fmt.Fprintf(w, "Backups kept until\t%s\n", whenPtr(d.PurgeAfter))
				}
			})
		},
	}
}

func dbPasswordCmd(a *App, engine string) *cobra.Command {
	var reset bool
	cmd := &cobra.Command{
		Use:   "password <name>",
		Short: "The owner's password (--reset: a new one; services bound to it are not affected)",
		Long: "Print the owner's password; reading it is recorded in the activity log. --reset gives\n" +
			"the owner a new one: the old one stops working within a minute.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			path := "/databases/" + url.PathEscape(d.Name) + "/reveal-password"
			if reset {
				path = "/databases/" + url.PathEscape(d.Name) + "/reset-password"
			}
			var p api.Password
			if err := c.Do(cmd.Context(), "POST", path, nil, nil, &p); err != nil {
				return err
			}
			if a.JSON {
				return a.print(p, nil)
			}
			fmt.Fprintln(a.Out, p.Password)
			if reset {
				fmt.Fprintf(a.Err, "A new password for %s: the old one stops working within a minute.\n", d.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false, "give the owner a new password")
	return cmd
}

// withPassword is a connection URL with the owner's password in it.
func withPassword(raw, user, password string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// printCreatedPassword says the new owner's password once — or, to a
// developer, who has it: secrets stay with the account's administrators.
func (a *App) printCreatedPassword(password *string, reveal string) {
	if password == nil {
		a.printf("Its owner's password stays with the account's administrators (`%s` for them);\n"+
			"services you bind to it get the connection variables on their own.\n\n", reveal)
		return
	}
	a.printf("Its owner's password, shown here once (`%s` reveals it again, recorded):\n\n  %s\n\n", reveal, *password)
}

func (a *App) revealPassword(ctx context.Context, d *api.Database) (string, error) {
	c, _ := a.Client()
	var p api.Password
	if err := c.Do(ctx, "POST", "/databases/"+url.PathEscape(d.Name)+"/reveal-password", nil, nil, &p); err != nil {
		return "", err
	}
	return p.Password, nil
}

func dbURLCmd(a *App, engine string) *cobra.Command {
	var direct bool
	cmd := &cobra.Command{
		Use:   "url <name>",
		Short: "The connection URL with the owner's password (reading it is recorded)",
		Long: "Print the connection URL with the owner's password. PostgreSQL: through the pooler, as\n" +
			"apps should connect; --direct for migrations, dumps and sessions that need one server.",
		Example: "  psql \"$(mw db url loja-prod --direct)\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			raw := d.Connection.URI
			if direct && d.Connection.DirectURI != nil {
				raw = *d.Connection.DirectURI
			}
			pw, err := a.revealPassword(cmd.Context(), d)
			if err != nil {
				return err
			}
			out, err := withPassword(raw, d.Connection.Username, pw)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, out)
			return nil
		},
	}
	if engine != "valkey" {
		cmd.Flags().BoolVar(&direct, "direct", false, "PostgreSQL: the direct port instead of the pooler")
	}
	return cmd
}

// caFile writes the CA a private environment's database certificates chain
// to, for a client to verify them; "" when the system's roots do.
func (a *App) caFile(ctx context.Context) (string, func(), error) {
	c, _ := a.Client()
	var ca api.DatabaseCA
	if err := c.Do(ctx, "GET", "/databases/ca-certificate", nil, nil, &ca); err != nil {
		return "", func() {}, err
	}
	if ca.CACertificate == nil {
		return "", func() {}, nil
	}
	f, err := os.CreateTemp("", "mw-db-ca-*.pem")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	if _, err := f.WriteString(*ca.CACertificate); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return f.Name(), cleanup, nil
}

var psqlVersionRe = regexp.MustCompile(`\b(\d+)(?:\.\d+)?`)

// libpqVerifiesSystemRoots: psql 16+ verifies a public certificate with
// sslrootcert=system; older ones only encrypt (sslmode=require).
func libpqVerifiesSystemRoots(psql string) bool {
	out, err := exec.Command(psql, "--version").Output()
	if err != nil {
		return false
	}
	m := psqlVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	return major >= 16
}

// pgSession is what a PostgreSQL client (psql, pg_dump, pg_restore) needs to
// reach a database: a connection string, TLS verified whenever the client can
// verify it, and the owner's password in the environment — never on a
// command line, where other users of the machine could read it.
type pgSession struct {
	dsn     string
	env     []string
	cleanup func()
}

func (a *App) pgSessionFor(ctx context.Context, d *api.Database, client string, pooler bool) (*pgSession, error) {
	if d.Status != "running" && d.Status != "updating" {
		return nil, fmt.Errorf("%s is %s: it takes connections once running", d.Name, dbStatus(d.Status))
	}
	pw, err := a.revealPassword(ctx, d)
	if err != nil {
		return nil, err
	}
	ca, cleanCA, err := a.caFile(ctx)
	if err != nil {
		return nil, err
	}
	r, err := a.routeTo(ctx, d, pooler)
	if err != nil {
		cleanCA()
		return nil, err
	}
	cleanup := func() {
		r.close()
		cleanCA()
	}
	conn := d.Connection
	// Through the tunnel, libpq connects to hostaddr and verifies the
	// certificate (and sends SNI) for host: the database's name.
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s", r.host, r.port, str(conn.Database), conn.Username)
	if r.tunnel {
		dsn = fmt.Sprintf("host=%s hostaddr=%s port=%d dbname=%s user=%s", r.host, r.addr, r.port, str(conn.Database), conn.Username)
	}
	switch {
	case ca != "":
		dsn += " sslmode=verify-full sslrootcert=" + ca
	case libpqVerifiesSystemRoots(client):
		dsn += " sslmode=verify-full sslrootcert=system"
	default:
		dsn += " sslmode=require"
	}
	return &pgSession{dsn: dsn, env: append(os.Environ(), "PGPASSWORD="+pw, "PGAPPNAME=mw-cli"), cleanup: cleanup}, nil
}

// pgClient finds a PostgreSQL client program, or says how to get one.
func pgClient(name string) (string, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is not installed: install the PostgreSQL client tools, or use `mw db url` with your own", name)
	}
	return bin, nil
}

// runClient runs a database client attached to the terminal; its failure
// becomes the CLI's exit code, with a hint when it could not connect.
func (a *App) runClient(ctx context.Context, d *api.Database, bin string, args, env []string, stdin io.Reader) error {
	run := exec.CommandContext(ctx, bin, args...)
	run.Env = env
	if stdin == nil {
		stdin = a.In
	}
	run.Stdin, run.Stdout, run.Stderr = stdin, a.Out, a.Err
	err := run.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		hint := ""
		if a.dbDirect && d.Engine != "valkey" && exit.ExitCode() != 0 {
			hint = fmt.Sprintf("\n  If it could not connect: is your address allowed? `mw db allow %s <your address>`, or leave out --no-tunnel", d.Name)
		}
		return &exitError{code: exit.ExitCode(), msg: hint}
	}
	return err
}

func dbConnectCmd(a *App, engine string) *cobra.Command {
	var pooler bool
	cmd := &cobra.Command{
		Use:   "connect <name> [-- client arguments]",
		Short: "Open psql, mysql, mariadb or redis-cli on a database, through the platform",
		Long: "Open the database's own client — psql (PostgreSQL), mysql (MySQL), mariadb (MariaDB)\n" +
			"or redis-cli (Redis) — with its owner's password passed in the environment or a file\n" +
			"only you can read, never on the command line. It connects through the platform, so the\n" +
			"database need not allow your address; TLS stays between the client and the database.\n" +
			"--no-tunnel connects to its address instead (`mw db allow <name> <your address>` first).\n" +
			"Arguments after -- go to the client.",
		Example: "  mw db connect loja-prod\n  mw db connect loja-prod -- -c 'select count(*) from pedidos'\n" +
			"  mw db connect loja-wp -- -e 'SHOW TABLES'\n  mw redis connect filas -- INFO keyspace",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			if d.Engine == "postgres" {
				bin, err := pgClient("psql")
				if err != nil {
					return err
				}
				pg, err := a.pgSessionFor(cmd.Context(), d, bin, pooler)
				if err != nil {
					return err
				}
				defer pg.cleanup()
				// psql exits 2 when it could not connect; 1 or 3 for a failed statement.
				err = a.runClient(cmd.Context(), d, bin, append([]string{pg.dsn}, args[1:]...), pg.env, nil)
				var exit *exitError
				if errors.As(err, &exit) && exit.code != 2 {
					exit.msg = ""
				}
				return err
			}
			if isMySQL(d.Engine) {
				return a.mysqlConnect(cmd.Context(), d, args[1:])
			}
			if d.Status != "running" && d.Status != "updating" {
				return fmt.Errorf("%s is %s: it takes connections once running", d.Name, dbStatus(d.Status))
			}
			var bin string
			for _, name := range []string{"redis-cli", "valkey-cli"} {
				if p, err := exec.LookPath(name); err == nil {
					bin = p
					break
				}
			}
			if bin == "" {
				return errors.New("redis-cli is not installed: install it, or use `mw redis url` with your own tool")
			}
			pw, err := a.revealPassword(cmd.Context(), d)
			if err != nil {
				return err
			}
			ca, cleanup, err := a.caFile(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			r, err := a.routeTo(cmd.Context(), d, false)
			if err != nil {
				return err
			}
			defer r.close()
			conn := d.Connection
			cargs := []string{"--tls", "-h", r.addr, "-p", strconv.Itoa(r.port), "--user", conn.Username, "--sni", r.host}
			if ca != "" {
				cargs = append(cargs, "--cacert", ca)
			}
			return a.runClient(cmd.Context(), d, bin, append(cargs, args[1:]...), append(os.Environ(), "REDISCLI_AUTH="+pw), nil)
		},
	}
	if engine != "valkey" {
		cmd.Flags().BoolVar(&pooler, "pooler", false, "PostgreSQL: through the pooler instead of the direct port")
	}
	noTunnelFlag(a, cmd)
	return cmd
}

// noTunnelFlag lets a command's database client connect to the database's
// own address rather than through the platform.
func noTunnelFlag(a *App, cmd *cobra.Command) {
	cmd.Flags().BoolVar(&a.dbDirect, "no-tunnel", false, "connect to the database's address (it must allow yours), not through the platform")
}

func dbExportCmd(a *App) *cobra.Command {
	var plain bool
	cmd := &cobra.Command{
		Use:   "export <name> [file]",
		Short: "Dump a SQL database to a file (pg_dump, mysqldump or mariadb-dump; from an allowed address)",
		Long: "Dump a database with your own client tools. PostgreSQL: pg_dump (as new as the database's\n" +
			"version), through the direct port, as a custom-format archive for `mw db import` or\n" +
			"pg_restore, or --sql for plain SQL. MySQL and MariaDB: one consistent snapshot as SQL,\n" +
			"with routines, triggers and events. Without a file it is named after the database and the\n" +
			"time.",
		Example: "  mw db export loja-prod\n  mw db export loja-prod backup.sql --sql\n  mw db export loja-wp wordpress.sql",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], "")
			if err != nil {
				return err
			}
			if d.Engine == "valkey" {
				return fmt.Errorf("%s is a Redis database: its data is kept on its disk, there is no dump to take", d.Name)
			}
			file := ""
			if len(args) == 2 {
				file = args[1]
			} else {
				ext := ".dump"
				if plain || isMySQL(d.Engine) {
					ext = ".sql"
				}
				file = d.Name + "-" + time.Now().Format("20060102-150405") + ext
			}
			if _, err := os.Stat(file); err == nil {
				return fmt.Errorf("%s exists: give another file name", file)
			}
			if isMySQL(d.Engine) {
				a.printf("Exporting %s to %s…\n", d.Name, file)
				if err := a.mysqlExport(cmd.Context(), d, file); err != nil {
					os.Remove(file)
					return err
				}
				if st, err := os.Stat(file); err == nil {
					a.printf("Exported %s (%s).\n", file, bytesText(strconv.FormatInt(st.Size(), 10)))
				}
				return nil
			}
			bin, err := pgClient("pg_dump")
			if err != nil {
				return err
			}
			pg, err := a.pgSessionFor(cmd.Context(), d, bin, false)
			if err != nil {
				return err
			}
			defer pg.cleanup()
			format := "--format=custom"
			if plain {
				format = "--format=plain"
			}
			a.printf("Exporting %s to %s…\n", d.Name, file)
			// Ownership and grants belong to this database's roles: left out,
			// so the dump restores anywhere.
			if err := a.runClient(cmd.Context(), d, bin, []string{format, "--no-owner", "--no-privileges", "--file", file, "--dbname", pg.dsn}, pg.env, nil); err != nil {
				os.Remove(file)
				return err
			}
			if st, err := os.Stat(file); err == nil {
				a.printf("Exported %s (%s).\n", file, bytesText(strconv.FormatInt(st.Size(), 10)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&plain, "sql", false, "plain SQL instead of pg_dump's custom format")
	noTunnelFlag(a, cmd)
	return cmd
}

func dbImportCmd(a *App) *cobra.Command {
	var clean, replace, detach bool
	var fromURL string
	cmd := &cobra.Command{
		Use:   "import <name> <file> | import <name> --from-url <url>",
		Short: "Load a dump into a SQL database (psql, pg_restore, mysql or mariadb)",
		Long: "Load a dump into a database. PostgreSQL, through its direct port: a .sql file with psql\n" +
			"(stopping at the first error), any other file as a pg_dump archive with pg_restore.\n" +
			"Objects keep the database's owner. The dump's extensions are not created by it: enable\n" +
			"them first (`mw db extensions`), as you are told if one is missing. --clean first drops\n" +
			"what an archive is about to create.\n\n" +
			"MySQL and MariaDB: a .sql or .sql.gz dump from any server (mysqldump, mariadb-dump,\n" +
			"another provider's export). What only its source server could run is left out — DEFINER\n" +
			"clauses naming other users, its GTIDs, its own database's name — and said so.\n\n" +
			"--from-url: the database copies another one straight in from its URL (one on a VPS, at\n" +
			"Heroku, Railway, RDS), nothing installed here; into an empty database, or --replace the\n" +
			"tables the source has. A restore point is marked first, so it can be undone.",
		Example: "  mw db import loja-prod loja-prod-20261024.dump\n  mw db import loja-staging dump.sql\n" +
			"  mw db import loja-wp wordpress.sql.gz\n" +
			"  mw db import loja-prod --from-url 'postgres://app:secret@old-vps.example.com:5432/loja?sslmode=require'",
		Args: func(cmd *cobra.Command, args []string) error {
			if fromURL != "" {
				return cobra.ExactArgs(1)(cmd, args)
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], "")
			if err != nil {
				return err
			}
			if d.Engine == "valkey" {
				return fmt.Errorf("%s is a Redis database: load data into it with your Redis client (`mw redis connect %s`)", d.Name, d.Name)
			}
			if fromURL != "" {
				return a.importFromURL(cmd.Context(), d.Name, fromURL, replace, detach)
			}
			if replace {
				return errors.New("--replace is for --from-url; for a file, --clean (pg_dump archives) or the dump's own DROP statements")
			}
			file := args[1]
			if _, err := os.Stat(file); err != nil {
				return err
			}
			if isMySQL(d.Engine) {
				lower := strings.ToLower(file)
				if !strings.HasSuffix(lower, ".sql") && !strings.HasSuffix(lower, ".sql.gz") {
					return fmt.Errorf("a %s dump is SQL: a .sql or .sql.gz file", engineName(d.Engine))
				}
				if clean {
					return errors.New("--clean is for pg_dump archives; a SQL dump drops what it says itself")
				}
				a.printf("Importing %s into %s…\n", file, d.Name)
				if err := a.mysqlImport(cmd.Context(), d, file); err != nil {
					var exit *exitError
					if errors.As(err, &exit) {
						exit.msg = ""
					}
					return err
				}
				a.printf("Imported into %s.\n", d.Name)
				return nil
			}
			sql := strings.HasSuffix(strings.ToLower(file), ".sql")
			if sql && clean {
				return errors.New("--clean is for pg_dump archives; a .sql file drops what it says itself")
			}
			client := "pg_restore"
			if sql {
				client = "psql"
			}
			bin, err := pgClient(client)
			if err != nil {
				return err
			}

			// What goes in, without the extensions' own statements.
			var wanted []string
			var stdin io.Reader
			var cargs []string
			if sql {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				var filtered bytes.Buffer
				wanted, err = filterSQL(f, &filtered)
				f.Close()
				if err != nil {
					return err
				}
				stdin = &filtered
			} else {
				listed, err := exec.CommandContext(cmd.Context(), bin, "--list", file).Output()
				if err != nil {
					return fmt.Errorf("%s is not a pg_dump archive pg_restore can read (a plain dump must end in .sql)", file)
				}
				var kept string
				kept, wanted = filterArchiveList(string(listed))
				list, err := os.CreateTemp("", "mw-restore-*.list")
				if err != nil {
					return err
				}
				defer os.Remove(list.Name())
				if _, err := list.WriteString(kept); err != nil {
					list.Close()
					return err
				}
				if err := list.Close(); err != nil {
					return err
				}
				cargs = []string{"--no-owner", "--no-privileges", "--exit-on-error", "--use-list", list.Name()}
				if clean {
					cargs = append(cargs, "--clean", "--if-exists")
				}
			}
			if missing := missingExtensions(wanted, d.Extensions); len(missing) > 0 {
				return fmt.Errorf("the dump uses %s, which %s does not have yet: `mw db extensions %s %s` (on within a minute), then import again",
					strings.Join(missing, ", "), d.Name, d.Name, strings.Join(missing, " "))
			}

			pg, err := a.pgSessionFor(cmd.Context(), d, bin, false)
			if err != nil {
				return err
			}
			defer pg.cleanup()
			if sql {
				cargs = []string{"--set", "ON_ERROR_STOP=1", "--quiet", "--file", "-", pg.dsn}
			} else {
				cargs = append(cargs, "--dbname", pg.dsn, file)
			}
			a.printf("Importing %s into %s…\n", file, d.Name)
			if err := a.runClient(cmd.Context(), d, bin, cargs, pg.env, stdin); err != nil {
				var exit *exitError
				if errors.As(err, &exit) {
					// The client said what failed: a statement, not the connection.
					exit.msg = ""
				}
				return err
			}
			a.printf("Imported into %s.\n", d.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&fromURL, "from-url", "", "copy another database in from its URL, on the database's side")
	cmd.Flags().BoolVar(&replace, "replace", false, "with --from-url: replace the tables the source has (else the database must be empty)")
	cmd.Flags().BoolVar(&detach, "detach", false, "with --from-url: start it and return (`mw db jobs` follows it)")
	cmd.Flags().BoolVar(&clean, "clean", false, "drop the objects the archive creates before restoring them")
	noTunnelFlag(a, cmd)
	return cmd
}

// exitError ends the CLI with a client's exit code; its own message, if
// any, is a hint for the user.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return strings.TrimSpace(e.msg) }

func dbAllowCmd(a *App, engine string, allow bool) *cobra.Command {
	use, short := "allow <name> <address>...", "Allow addresses or CIDRs to connect (your services always can)"
	if !allow {
		use, short = "disallow <name> <address>...", "Stop allowing addresses or CIDRs"
	}
	return &cobra.Command{
		Use:     use,
		Short:   short,
		Example: "  mw db allow loja-prod 203.0.113.7 198.51.100.0/24",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			current := map[string]bool{}
			var list []string
			for _, c := range d.AllowedCIDRs {
				current[c] = true
				list = append(list, c)
			}
			if allow {
				for _, c := range args[1:] {
					if !current[c] {
						list = append(list, c)
					}
				}
			} else {
				drop := map[string]bool{}
				for _, c := range args[1:] {
					drop[c] = true
					// The API stores a single address as /32 or /128.
					if !strings.Contains(c, "/") {
						drop[c+"/32"], drop[c+"/128"] = true, true
					}
				}
				list = list[:0]
				for _, c := range d.AllowedCIDRs {
					if !drop[c] {
						list = append(list, c)
					}
				}
				if len(list) == len(d.AllowedCIDRs) {
					return fmt.Errorf("%s does not allow %s", d.Name, strings.Join(args[1:], ", "))
				}
			}
			if list == nil {
				list = []string{}
			}
			out, err := a.updateDatabase(cmd.Context(), d, map[string]interface{}{"allowed_cidrs": list})
			if err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				if len(out.AllowedCIDRs) == 0 {
					fmt.Fprintf(w, "%s: only your services may connect. Applied within a minute.\n", out.Name)
					return
				}
				fmt.Fprintf(w, "%s also allows %s. Applied within a minute.\n", out.Name, strings.Join(out.AllowedCIDRs, ", "))
			})
		},
	}
}

func (a *App) updateDatabase(ctx context.Context, d *api.Database, patch map[string]interface{}) (*api.Database, error) {
	c, _ := a.Client()
	var out api.Database
	if err := c.Do(ctx, "PATCH", "/databases/"+url.PathEscape(d.Name), nil, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func dbExtensionsCmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:     "extensions <name> [extension...]",
		Short:   "List a PostgreSQL database's extensions, or enable more (never removed from here)",
		Example: "  mw db extensions loja-prod vector pg_trgm",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], "postgres")
			if err != nil {
				return err
			}
			if len(args) == 1 {
				info, err := a.databasesInfo(cmd.Context())
				if err != nil {
					return err
				}
				on := map[string]bool{}
				for _, e := range d.Extensions {
					on[e] = true
				}
				var offered []string
				for _, e := range info.Engines {
					if e.Engine == "postgres" {
						offered = e.Extensions
					}
				}
				return a.print(d.Extensions, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "EXTENSION\tENABLED")
					for _, e := range offered {
						mark := ""
						if on[e] {
							mark = "yes"
						}
						fmt.Fprintf(w, "%s\t%s\n", e, mark)
					}
				})
			}
			list := append([]string{}, d.Extensions...)
			for _, e := range args[1:] {
				if !contains(list, e) {
					list = append(list, e)
				}
			}
			out, err := a.updateDatabase(cmd.Context(), d, map[string]interface{}{"extensions": list})
			if err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s: %s. Enabled within a minute.\n", out.Name, strings.Join(out.Extensions, ", "))
			})
		},
	}
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// weekdays are the maintenance window's days as `update` takes them.
var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func dbUpdateCmd(a *App, engine string) *cobra.Command {
	var backups, protection bool
	var eviction, project, day string
	var hour, autogrow int
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Change backups, deletion protection, the maintenance window, disk growth, eviction or project",
		Example: "  mw db update loja-staging --deletion-protection=false\n" +
			"  mw db update loja-prod --maintenance-day sun --maintenance-hour 6\n" +
			"  mw db update loja-prod --disk-autogrow-max 200\n" +
			"  mw redis update cache --eviction-policy allkeys-lru",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			patch := map[string]interface{}{}
			f := cmd.Flags()
			if f.Changed("backups") {
				patch["backups"] = backups
			}
			if f.Changed("deletion-protection") {
				patch["deletion_protection"] = protection
			}
			if f.Changed("eviction-policy") {
				patch["eviction_policy"] = eviction
			}
			if f.Changed("maintenance-day") {
				switch i := indexOf(weekdays, strings.ToLower(day)); {
				case strings.ToLower(day) == "every":
					patch["maintenance_day"] = nil
				case i >= 0:
					patch["maintenance_day"] = i
				default:
					return fmt.Errorf("--maintenance-day is one of %s, or every", strings.Join(weekdays, ", "))
				}
			}
			if f.Changed("maintenance-hour") {
				patch["maintenance_hour_utc"] = hour
			}
			if f.Changed("disk-autogrow-max") {
				if autogrow <= 0 {
					patch["disk_autogrow_max_gb"] = nil
				} else {
					patch["disk_autogrow_max_gb"] = autogrow
				}
			}
			if f.Changed("project") {
				if project == "" {
					patch["project_id"] = nil
				} else {
					p, err := a.resolveProject(cmd.Context(), project)
					if err != nil {
						return err
					}
					patch["project_id"] = p.ID
				}
			}
			if len(patch) == 0 {
				return errors.New("nothing to change: pass --backups, --deletion-protection, --maintenance-day, --maintenance-hour, --disk-autogrow-max, --eviction-policy or --project")
			}
			out, err := a.updateDatabase(cmd.Context(), d, patch)
			if err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s saved.", out.Name)
				if out.ChangesPending {
					fmt.Fprint(w, " Applied on the database within a minute.")
				}
				fmt.Fprintln(w)
			})
		},
	}
	if engine != "valkey" {
		cmd.Flags().BoolVar(&backups, "backups", true, "continuous backups (PostgreSQL, MySQL, MariaDB)")
	}
	if engine != "postgres" {
		cmd.Flags().StringVar(&eviction, "eviction-policy", "", "Redis: noeviction, allkeys-lru, volatile-lru…")
	}
	cmd.Flags().BoolVar(&protection, "deletion-protection", true, "protect it from deletion")
	cmd.Flags().StringVar(&day, "maintenance-day", "", "the maintenance window's day: sun, mon … sat, or every")
	cmd.Flags().IntVar(&hour, "maintenance-hour", 6, "the maintenance window's hour, UTC (0–23)")
	cmd.Flags().IntVar(&autogrow, "disk-autogrow-max", 0, "grow the disk by itself past 85% used, up to this many GB (0: off)")
	cmd.Flags().StringVar(&project, "project", "", "the project it belongs to (\"\" for none)")
	return cmd
}

func dbResizeCmd(a *App, engine string) *cobra.Command {
	var size string
	var disk int
	var wait, inWindow bool
	cmd := &cobra.Command{
		Use:   "resize <name>",
		Short: "Change a database's size, or grow its disk (a short restart; a disk never shrinks)",
		Long: "Change the CPU and memory (--size, see `sizes`) or grow the disk (--disk, in GB). Billed\n" +
			"at the new size from when it is applied: a new size with a restart of about a minute — now,\n" +
			"or in its maintenance window (--in-window) — a bigger disk alone while it runs.",
		Example: "  mw db resize loja-prod --size pg-2-4\n  mw db resize loja-prod --size pg-2-4 --in-window\n  mw db resize loja-prod --disk 60",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if size == "" && disk == 0 {
				return errors.New("pass --size, --disk or both")
			}
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			body := map[string]interface{}{}
			if size != "" {
				body["size"] = size
			}
			if disk > 0 {
				body["disk_gb"] = disk
			}
			if inWindow {
				body["when"] = "maintenance"
			}
			c, _ := a.Client()
			var out api.Database
			if err := c.Do(cmd.Context(), "POST", "/databases/"+url.PathEscape(d.Name)+"/resize", nil, body, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.print(out, nil)
			}
			switch {
			case out.Maintenance.ScheduledSize != nil || out.Maintenance.ScheduledDiskGB != nil:
				a.printf("%s: scheduled for its maintenance window, %s.\n", out.Name, whenPtr(&out.Maintenance.NextAt))
				return nil
			case size == "" || size == d.Size.Code:
				a.printf("%s: %d GB of disk, taken while it runs (no restart).\n", out.Name, out.Size.DiskGB)
			default:
				a.printf("%s: %s from now on, applied with a short restart.\n", out.Name, dbSizeText(out.Size))
			}
			if !wait {
				return nil
			}
			return a.waitDatabase(cmd.Context(), out.Name, engine)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "the new size (CPU and memory; its disk if bigger)")
	cmd.Flags().IntVar(&disk, "disk", 0, "a bigger disk, in GB")
	cmd.Flags().BoolVar(&inWindow, "in-window", false, "apply a new size in the maintenance window, not now")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it runs at the new size")
	return cmd
}

func dbRetryCmd(a *App, engine string) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <name>",
		Short: "Try creating a database again, after it failed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			c, _ := a.Client()
			var out api.Database
			if err := c.Do(cmd.Context(), "POST", "/databases/"+url.PathEscape(d.Name)+"/retry", nil, nil, &out); err != nil {
				return err
			}
			if a.JSON {
				return a.print(out, nil)
			}
			a.printf("Creating %s again.\n", out.Name)
			return a.waitDatabase(cmd.Context(), out.Name, engine)
		},
	}
}

func dbDeleteCmd(a *App, engine string) *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a database (protection off, no service bound, name confirmed)",
		Long: "Delete a database. Its deletion protection must be off and no service may still be\n" +
			"bound to it. Its backups are kept for a few days, then removed for good.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := a.database(cmd.Context(), args[0], engine)
			if err != nil {
				return err
			}
			if confirm != d.Name {
				return fmt.Errorf("type the database's name to confirm: --confirm %s", d.Name)
			}
			c, _ := a.Client()
			var out api.Database
			if err := c.Do(cmd.Context(), "DELETE", "/databases/"+url.PathEscape(d.Name), url.Values{"confirm": {confirm}}, nil, &out); err != nil {
				return err
			}
			return a.print(out, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "%s deleted.", out.Name)
				if out.PurgeAfter != nil {
					fmt.Fprintf(w, " Its backups are kept until %s.", whenPtr(out.PurgeAfter))
				}
				fmt.Fprintln(w)
			})
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the database's name, to confirm")
	return cmd
}

func dbCACmd(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ca",
		Short: "Print the CA database certificates chain to, when your system does not trust it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.Client()
			if err != nil {
				return err
			}
			var ca api.DatabaseCA
			if err := c.Do(cmd.Context(), "GET", "/databases/ca-certificate", nil, nil, &ca); err != nil {
				return err
			}
			if a.JSON {
				return a.print(ca, nil)
			}
			if ca.CACertificate == nil {
				fmt.Fprintln(a.Err, "Database certificates are public: your system already trusts them (sslmode=verify-full works as is).")
				return nil
			}
			fmt.Fprint(a.Out, *ca.CACertificate)
			return nil
		},
	}
}

func seriesList(list []api.Series) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = string(s)
	}
	return strings.Join(out, ", ")
}
