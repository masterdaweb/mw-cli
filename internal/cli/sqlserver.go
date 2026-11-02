package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// sqlServerTermsURL is Microsoft's licence terms for SQL Server 2025's free
// editions, which Express is; a paid edition's key comes with its own terms.
const sqlServerTermsURL = "https://www.microsoft.com/content/dam/microsoft/usetm/documents/sql-server/sql-server-2025-developer,-express,-evaluation/retail/SQL_Server_2025_Developer_Express_and_Evaluation_Edition_English.pdf"

// sqlServerEditions are what `--edition` takes: Express is free; Standard and
// Enterprise are the customer's own licence, with its product key.
var sqlServerEditions = []string{"express", "standard", "enterprise"}

// sqlServerRestoreLicence is what a restore of a SQL Server database sends:
// the terms accepted again (the new machine installs SQL Server afresh), and
// an edition other than the original's only when asked for. A paid edition
// (the original's or the one asked for) takes the customer's statement that
// their licence covers the new machine too; the key is the original's unless
// another is given.
func (a *App) sqlServerRestoreLicence(body map[string]interface{}, src *api.Database, edition, key string, acceptTerms, attested bool) error {
	current := "express"
	if src.Licence != nil && src.Licence.Edition != "" {
		current = src.Licence.Edition
	}
	if edition != "" && !contains(sqlServerEditions, edition) {
		return fmt.Errorf("--edition is one of %s", strings.Join(sqlServerEditions, ", "))
	}
	if !acceptTerms {
		ok, err := a.confirm("SQL Server is Microsoft software; its licence terms apply to your use of it:\n  "+
			sqlServerTermsURL+"\nDo you accept them? [y/N] ", "--accept-terms")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not restored: SQL Server runs only once Microsoft's licence terms are accepted")
		}
	}
	body["accept_terms"] = true
	target := current
	if edition != "" {
		body["edition"] = edition
		target = edition
	}
	if target == "express" {
		if key != "" {
			return errors.New("--product-key is for --edition standard or enterprise: Express takes none")
		}
		return nil
	}
	if key == "-" {
		line, err := bufio.NewReader(a.In).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no product key on standard input")
		}
		key = line
	}
	if key = strings.ToUpper(strings.TrimSpace(key)); key != "" {
		body["product_key"] = key
	} else if target != current || src.Licence == nil || !src.Licence.HasProductKey {
		return fmt.Errorf("--edition %s is your own licence: give its product key with --product-key (or --product-key - to read it from standard input)", target)
	}
	if !attested {
		ok, err := a.confirm("The restored database runs your own SQL Server "+target+" licence too, under Microsoft's Flexible\n"+
			"Virtualization Benefit: it must have active Software Assurance or be a subscription, for this machine's cores as well.\n"+
			"Does yours cover it? [y/N] ", "--licence-attested")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not restored: use --edition express, or a licence with Software Assurance or a subscription")
		}
	}
	body["licence_attestation"] = true
	return nil
}

// sqlServerEditionText is a SQL Server database's edition as `get` shows it:
// what it was asked to run, and what it runs when that differs.
func sqlServerEditionText(l *api.DatabaseLicence) string {
	name := func(e string) string {
		if e == "" {
			return e
		}
		return strings.ToUpper(e[:1]) + e[1:]
	}
	text := name(l.Edition)
	if l.Edition == "express" {
		text += " (free)"
	} else {
		text += " (your own licence"
		if l.KeyApplied != nil && *l.KeyApplied {
			text += ", key in force"
		}
		text += ")"
	}
	if l.RunningEdition != nil && *l.RunningEdition != "" && *l.RunningEdition != l.Edition {
		text += " · running " + name(*l.RunningEdition)
		if l.Problem != nil && *l.Problem != "" {
			text += ": " + *l.Problem
		}
	}
	return text
}

// isSQLServer is the one engine whose client is sqlcmd and whose dumps are
// .bak files.
func isSQLServer(engine string) bool { return engine == "sqlserver" }

// sqlServerLicence turns the create flags into the request's licence fields,
// asking what the flags did not say when there is a terminal to ask on.
// Microsoft's terms are accepted only by the person, never by default.
func (a *App) sqlServerLicence(body map[string]interface{}, edition, key string, acceptTerms, attested bool) error {
	if edition == "" {
		edition = "express"
	}
	if !contains(sqlServerEditions, edition) {
		return fmt.Errorf("--edition is one of %s", strings.Join(sqlServerEditions, ", "))
	}
	if !acceptTerms {
		ok, err := a.confirm("SQL Server is Microsoft software; its licence terms apply to your use of it:\n  "+
			sqlServerTermsURL+"\nDo you accept them? [y/N] ", "--accept-terms")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not created: SQL Server runs only once Microsoft's licence terms are accepted")
		}
	}
	body["edition"] = edition
	body["accept_terms"] = true
	if edition == "express" {
		if key != "" {
			return errors.New("--product-key is for --edition standard or enterprise: Express takes none")
		}
		return nil
	}
	if key == "-" {
		line, err := bufio.NewReader(a.In).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no product key on standard input")
		}
		key = line
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		return fmt.Errorf("--edition %s is your own licence: give its product key with --product-key (or --product-key - to read it from standard input)", edition)
	}
	if !attested {
		ok, err := a.confirm("Your own SQL Server licence runs here under Microsoft's Flexible Virtualization Benefit:\n"+
			"it must have active Software Assurance or be a subscription, for at least the database's cores (4 or more).\n"+
			"Does yours? [y/N] ", "--licence-attested")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not created: use --edition express, or a licence with Software Assurance or a subscription")
		}
	}
	body["product_key"] = key
	body["licence_attestation"] = true
	return nil
}

// sqlcmdTool finds a sqlcmd that can check the database's certificate through
// the tunnel, where it connects to 127.0.0.1: one whose -F names the host the
// certificate must be for. Microsoft's ODBC sqlcmd (mssql-tools18) and the Go
// one (`brew install sqlcmd`, `winget install sqlcmd`) both do now, with the
// same flags; older ones (mssql-tools 17, a Go sqlcmd whose -F was the output
// format) are named as too old rather than reported missing.
func sqlcmdTool() (string, error) {
	old := ""
	for _, p := range []string{"sqlcmd", "/opt/mssql-tools18/bin/sqlcmd"} {
		bin, err := exec.LookPath(p)
		if err != nil {
			continue
		}
		out, _ := exec.Command(bin, "-?").CombinedOutput()
		help := strings.ToLower(string(out))
		if strings.Contains(help, "hostname in certificate") || strings.Contains(help, "host-name-in-certificate") {
			return bin, nil
		}
		if old == "" {
			old = bin
		}
	}
	if old != "" {
		return "", fmt.Errorf("%s is too old to check the database's certificate: update it (brew upgrade sqlcmd, winget upgrade sqlcmd, or Microsoft's mssql-tools18), or use `mw db url` with your own client", old)
	}
	return "", errors.New("sqlcmd is not installed: install it (brew install sqlcmd, winget install sqlcmd, or Microsoft's mssql-tools18), or use `mw db url` with your own client")
}

// sqlServerSession is what sqlcmd needs to reach a database: its arguments,
// and the password in the environment — never on a command line, where other
// users of the machine could read it.
type sqlServerSession struct {
	args    []string
	env     []string
	cleanup func()
}

func (a *App) sqlServerSessionFor(ctx context.Context, d *api.Database) (*sqlServerSession, error) {
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
	r, err := a.routeTo(ctx, d, false)
	if err != nil {
		cleanCA()
		return nil, err
	}
	cleanup := func() {
		r.close()
		cleanCA()
	}
	conn := d.Connection
	// Through the tunnel sqlcmd connects to 127.0.0.1 and checks the
	// certificate against the database's name (-F); directly, the name is
	// the address. A private environment's CA cannot be handed to sqlcmd
	// (it trusts the system's store), so there it only encrypts.
	args := []string{"-S", "tcp:" + r.addr + "," + strconv.Itoa(r.port), "-U", conn.Username, "-d", str(conn.Database), "-N"}
	if ca != "" {
		args = append(args, "-C")
	} else if r.tunnel {
		args = append(args, "-F", r.host)
	}
	return &sqlServerSession{args: args, env: append(os.Environ(), "SQLCMDPASSWORD="+pw), cleanup: cleanup}, nil
}

// sqlServerConnect opens sqlcmd on the database.
func (a *App) sqlServerConnect(ctx context.Context, d *api.Database, extra []string) error {
	bin, err := sqlcmdTool()
	if err != nil {
		return err
	}
	s, err := a.sqlServerSessionFor(ctx, d)
	if err != nil {
		return err
	}
	defer s.cleanup()
	return a.runClient(ctx, d, bin, append(append([]string{}, s.args...), extra...), s.env, nil)
}
