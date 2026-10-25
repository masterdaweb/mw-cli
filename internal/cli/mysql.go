package cli

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// MySQL and MariaDB from the terminal: their own clients (mysql, mariadb,
// mysqldump, mariadb-dump), the owner's password in a client options file
// only the user can read — never on a command line, where other users of the
// machine could see it — and TLS verified whenever the client can verify it.

// mysqlTool finds a MySQL-family program for an engine: its own flavor's
// first (`mariadb-dump` for MariaDB), the other's when that is all there is.
// It says whether the one found is MariaDB's: the two spell their TLS options
// differently.
func mysqlTool(engine string, mysqlName, mariadbName string) (bin string, mariadb bool, err error) {
	order := []string{mysqlName, mariadbName}
	if engine == "mariadb" {
		order = []string{mariadbName, mysqlName}
	}
	for _, name := range order {
		if p, err := exec.LookPath(name); err == nil {
			out, _ := exec.Command(p, "--version").CombinedOutput()
			return p, strings.Contains(string(out), "MariaDB"), nil
		}
	}
	return "", false, fmt.Errorf("%s is not installed: install the %s client tools, or use `mw db url` with your own",
		order[0], engineName(engine))
}

// systemCABundles are where the system's trusted roots live, by platform.
var systemCABundles = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian, Ubuntu, Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL, Fedora
	"/etc/ssl/cert.pem",                  // macOS, BSDs
	"/opt/homebrew/etc/ca-certificates/cert.pem",
	"/usr/local/etc/ca-certificates/cert.pem",
}

func systemCABundle() string {
	for _, p := range systemCABundles {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p
		}
	}
	return ""
}

// mysqlSession is what a MySQL-family client needs to reach a database: the
// options (first of them the file holding the password) and a cleanup.
type mysqlSession struct {
	args    []string
	cleanup func()
}

func (a *App) mysqlSessionFor(ctx context.Context, d *api.Database, mariadbClient bool) (*mysqlSession, error) {
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
	opts, err := os.CreateTemp("", "mw-db-*.cnf")
	if err != nil {
		cleanCA()
		return nil, err
	}
	r, err := a.routeTo(ctx, d, false)
	if err != nil {
		cleanCA()
		os.Remove(opts.Name())
		return nil, err
	}
	cleanup := func() {
		r.close()
		os.Remove(opts.Name())
		cleanCA()
	}
	// CreateTemp makes the file 0600; the password never leaves it.
	if _, err := fmt.Fprintf(opts, "[client]\npassword=%s\n", pw); err != nil {
		opts.Close()
		cleanup()
		return nil, err
	}
	if err := opts.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if ca == "" {
		ca = systemCABundle()
	}
	conn := d.Connection
	// --defaults-extra-file must come first, or the client ignores it. The
	// character set is said outright: a client left to guess takes it from
	// the terminal's locale, and one without a UTF-8 locale (a minimal
	// container, a Windows console) stores "pão" as latin1 mojibake.
	args := []string{"--defaults-extra-file=" + opts.Name(),
		"-h", r.addr, "-P", strconv.Itoa(r.port), "-u", conn.Username, "--default-character-set=utf8mb4"}
	switch {
	// Through the tunnel the client connects to 127.0.0.1, so it cannot
	// check the certificate's name against the database's. MariaDB's checks
	// the name whenever it has a CA, whatever it is told: it pins the very
	// certificate the database serves instead. MySQL's checks the chain.
	case r.tunnel && mariadbClient && r.fingerprint != "":
		args = append(args, "--ssl-fp="+r.fingerprint)
	case r.tunnel && mariadbClient:
		args = append(args, "--ssl", "--skip-ssl-verify-server-cert")
	case r.tunnel && ca != "":
		args = append(args, "--ssl-mode=VERIFY_CA", "--ssl-ca="+ca)
	case r.tunnel:
		args = append(args, "--ssl-mode=REQUIRED")
	case mariadbClient && ca != "":
		args = append(args, "--ssl", "--ssl-verify-server-cert", "--ssl-ca="+ca)
	case mariadbClient:
		args = append(args, "--ssl", "--ssl-verify-server-cert")
	case ca != "":
		args = append(args, "--ssl-mode=VERIFY_IDENTITY", "--ssl-ca="+ca)
	default:
		// No roots to verify with: encrypted all the same.
		args = append(args, "--ssl-mode=REQUIRED")
	}
	return &mysqlSession{args: args, cleanup: cleanup}, nil
}

// mysqlConnect opens the engine's client on the database.
func (a *App) mysqlConnect(ctx context.Context, d *api.Database, extra []string) error {
	bin, mariadb, err := mysqlTool(d.Engine, "mysql", "mariadb")
	if err != nil {
		return err
	}
	s, err := a.mysqlSessionFor(ctx, d, mariadb)
	if err != nil {
		return err
	}
	defer s.cleanup()
	args := append(append([]string{}, s.args...), extra...)
	return a.runClient(ctx, d, bin, append(args, str(d.Connection.Database)), os.Environ(), nil)
}

// mysqlExport dumps the database with its flavor's dump tool: one consistent
// snapshot (InnoDB's), routines, triggers and events included, and nothing
// only the source server could take back (its GTIDs, its tablespaces).
func (a *App) mysqlExport(ctx context.Context, d *api.Database, file string) error {
	bin, mariadb, err := mysqlTool(d.Engine, "mysqldump", "mariadb-dump")
	if err != nil {
		return err
	}
	s, err := a.mysqlSessionFor(ctx, d, mariadb)
	if err != nil {
		return err
	}
	defer s.cleanup()
	args := append(append([]string{}, s.args...),
		"--single-transaction", "--quick", "--routines", "--triggers", "--events", "--no-tablespaces", "--hex-blob")
	if !mariadb {
		args = append(args, "--set-gtid-purged=OFF")
		if d.Engine == "mariadb" {
			// MySQL's mysqldump asks MariaDB for statistics it does not keep.
			args = append(args, "--column-statistics=0")
		}
	}
	args = append(args, "--result-file="+file, str(d.Connection.Database))
	return a.runClient(ctx, d, bin, args, os.Environ(), nil)
}

// mysqlImport loads a .sql (or .sql.gz) dump into the database, filtered of
// what a managed database refuses from any dump (see filterMySQLDump).
func (a *App) mysqlImport(ctx context.Context, d *api.Database, file string) error {
	bin, mariadb, err := mysqlTool(d.Engine, "mysql", "mariadb")
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var in io.Reader = f
	if strings.HasSuffix(strings.ToLower(file), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("%s is not a gzip file: %w", file, err)
		}
		defer gz.Close()
		in = gz
	}
	s, err := a.mysqlSessionFor(ctx, d, mariadb)
	if err != nil {
		return err
	}
	defer s.cleanup()
	pr, pw := io.Pipe()
	report := make(chan dumpReport, 1)
	go func() {
		r, err := filterMySQLDump(in, pw)
		report <- r
		pw.CloseWithError(err)
	}()
	args := append(append([]string{}, s.args...), str(d.Connection.Database))
	runErr := a.runClient(ctx, d, bin, args, os.Environ(), pr)
	pr.Close()
	r := <-report
	if r.otherDatabase != "" {
		a.printf("The dump names its own database (%s): its tables went into %s.\n", r.otherDatabase, str(d.Connection.Database))
	}
	if r.definers > 0 {
		a.printf("Left out %d DEFINER clause(s): those views, routines and triggers now run as you.\n", r.definers)
	}
	if r.serverOnly > 0 {
		a.printf("Left out %d statement(s) only the source server could run (its GTIDs, its binary log switch).\n", r.serverOnly)
	}
	return runErr
}

// dumpReport is what filterMySQLDump took out, to tell the user.
type dumpReport struct {
	definers      int
	serverOnly    int
	otherDatabase string
}

var (
	// A definer as dumps write one: `user`@`host`, 'user'@'host', CURRENT_USER.
	definerRe = regexp.MustCompile("DEFINER\\s*=\\s*(?:`[^`]*`@`[^`]*`|'[^']*'@'[^']*'|[A-Za-z0-9_.$-]+@[A-Za-z0-9_.%$-]+|CURRENT_USER(?:\\(\\))?)\\s*")
	useRe     = regexp.MustCompile("^USE\\s+`([^`]+)`\\s*;")
	createDB  = regexp.MustCompile("(?i)^CREATE DATABASE\\b.*?`([^`]+)`")
)

// filterMySQLDump copies a mysqldump/mariadb-dump stream, leaving out what no
// managed database takes from a customer (no provider grants SUPER):
//
//   - DEFINER clauses naming another user — the objects are created as the
//     importing user instead;
//   - the source's GTID set and its switching off of the binary log;
//   - CREATE DATABASE / USE of the dump's own database name: everything
//     goes into the database being imported into;
//   - MariaDB 11's sandbox-mode guard, which MySQL's client cannot read.
//
// It works line by line on a stream (a dump's INSERT lines are as long as
// the server's packet limit), never holding the whole dump.
func filterMySQLDump(in io.Reader, out io.Writer) (dumpReport, error) {
	var rep dumpReport
	r := bufio.NewReaderSize(in, 1<<20)
	w := bufio.NewWriterSize(out, 1<<20)
	skipping := false // inside a multi-line SET @@GLOBAL.GTID_PURGED
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimSpace(line)
			switch {
			case skipping:
				if strings.HasSuffix(trimmed, ";") {
					skipping = false
				}
				line = ""
			case strings.HasPrefix(trimmed, "SET @@GLOBAL.GTID_PURGED"):
				rep.serverOnly++
				skipping = !strings.HasSuffix(trimmed, ";")
				line = ""
			case strings.HasPrefix(trimmed, "SET @@SESSION.SQL_LOG_BIN"), strings.HasPrefix(trimmed, "SET @MYSQLDUMP_TEMP_LOG_BIN"):
				rep.serverOnly++
				line = ""
			case strings.HasPrefix(trimmed, "/*M!999999\\- enable the sandbox mode */"):
				line = ""
			case useRe.MatchString(trimmed):
				rep.otherDatabase = useRe.FindStringSubmatch(trimmed)[1]
				line = ""
			case createDB.MatchString(trimmed):
				rep.otherDatabase = createDB.FindStringSubmatch(trimmed)[1]
				line = ""
			// Only statements define objects: a row's data is never rewritten.
			case strings.Contains(line, "DEFINER") && !strings.HasPrefix(trimmed, "INSERT ") && !strings.HasPrefix(trimmed, "REPLACE "):
				n := len(definerRe.FindAllStringIndex(line, -1))
				if n > 0 {
					rep.definers += n
					line = definerRe.ReplaceAllString(line, "")
				}
			}
			if line != "" {
				if _, werr := w.WriteString(line); werr != nil {
					return rep, werr
				}
			}
		}
		if err == io.EOF {
			return rep, w.Flush()
		}
		if err != nil {
			return rep, err
		}
	}
}
