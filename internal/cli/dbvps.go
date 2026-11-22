package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// A database used from the account's own VPS instances: picked by name or id
// rather than typed as addresses — the database then admits the addresses
// each instance holds NOW, follows them, and stops admitting one the
// instance gives up (a typed address stays until removed, whoever holds it
// next) — and connection URLs that verify the certificate with what this
// machine's clients can read.

// accountInstances is the account's VPS instances with the public addresses
// each holds now (GET /v1/databases/instances).
func (a *App) accountInstances(ctx context.Context) ([]api.DatabaseInstance, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var out []api.DatabaseInstance
	if err := c.Do(ctx, "GET", "/databases/instances", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// instanceName is how an instance is named: its name, its hostname, or its id.
func instanceName(s api.DatabaseInstance) string {
	switch {
	case s.Name != nil && *s.Name != "":
		return *s.Name
	case s.Hostname != nil && *s.Hostname != "":
		return *s.Hostname
	}
	return "instance " + s.ID
}

// instanceText names an instance with the addresses it holds now.
func instanceText(s api.DatabaseInstance) string {
	if len(s.Addresses) == 0 {
		return instanceName(s) + " (no public address)"
	}
	return instanceName(s) + " (" + strings.Join(s.Addresses, ", ") + ")"
}

// findInstance is the instance an --instance names: its id, or its name or
// hostname whatever the case; refused with the candidates when it names several.
func findInstance(instances, allowed []api.DatabaseInstance, ref string) (api.DatabaseInstance, error) {
	ref = strings.TrimSpace(ref)
	all := append(append([]api.DatabaseInstance{}, instances...), allowed...)
	for _, s := range all {
		if s.ID == ref {
			return s, nil
		}
	}
	var found []api.DatabaseInstance
	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.ID] {
			continue
		}
		if (s.Name != nil && strings.EqualFold(*s.Name, ref)) || (s.Hostname != nil && strings.EqualFold(*s.Hostname, ref)) {
			found = append(found, s)
			seen[s.ID] = true
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return api.DatabaseInstance{}, fmt.Errorf("no instance %q in this account (`mw instances` lists them)", ref)
	}
	ids := make([]string, 0, len(found))
	for _, s := range found {
		ids = append(ids, s.ID)
	}
	return api.DatabaseInstance{}, fmt.Errorf("%d instances are called %q: name one by its id (%s)", len(found), ref, strings.Join(ids, ", "))
}

// busiestLocation is the deployable location running most of the account's
// instances (the first of them on a tie), or nil when it runs none in one.
func busiestLocation(all []location) *location {
	var best *location
	for i := range all {
		l := &all[i]
		if !l.Deployable || l.Instances == 0 {
			continue
		}
		if best == nil || l.Instances > best.Instances {
			best = l
		}
	}
	return best
}

var (
	sslRootCertRe = regexp.MustCompile(`([?&]sslrootcert=)[^&]*`)
	sslCARe       = regexp.MustCompile(`([?&]ssl-ca=)[^&]*`)
)

// privateCAFile is the name `mw db ca` output is saved as, and the one the
// API's URLs carry where the certificate is this environment's own.
const privateCAFile = "database-ca.pem"

// What this machine has, for the URL's roots: swapped in tests.
var (
	localPsqlMajor = psqlMajor
	localCABundle  = systemCABundle
)

// psqlMajor is the major version of the psql on PATH, or 0 when there is none.
func psqlMajor() int {
	bin, err := exec.LookPath("psql")
	if err != nil {
		return 0
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return 0
	}
	m := psqlVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	major, _ := strconv.Atoi(m[1])
	return major
}

// urlRoots is a connection URL whose roots this machine's clients can read,
// and a note for the terminal when it was changed. PostgreSQL's say
// sslrootcert=system, which only libpq 16+ reads; an older psql here gets
// the system's CA bundle instead (Ubuntu 22.04's is 14). MySQL's client
// will not VERIFY_IDENTITY without a CA file, so one is always named. `ca`
// is --ca-file: a path, or "system" to keep PostgreSQL's as the API gives it.
func urlRoots(engine, raw, ca string, psql int, bundle string, privateCA bool) (string, string) {
	switch engine {
	case "postgres":
		if ca == "system" {
			return raw, ""
		}
		if ca != "" {
			return sslRootCertRe.ReplaceAllString(raw, "${1}"+queryPath(ca)), ""
		}
		if strings.Contains(raw, "sslrootcert="+privateCAFile) {
			return raw, "This environment's certificates are its own: save the CA beside the client with `mw db ca > " + privateCAFile + "`."
		}
		if psql > 0 && psql < 16 && bundle != "" {
			return sslRootCertRe.ReplaceAllString(raw, "${1}"+queryPath(bundle)),
				fmt.Sprintf("psql %d here predates sslrootcert=system, so the URL names %s instead (--ca-file system keeps it).", psql, bundle)
		}
		return raw, ""
	case "mysql", "mariadb":
		note := ""
		if ca == "" {
			switch {
			case privateCA:
				ca = privateCAFile
				note = "This environment's certificates are its own: save the CA with `mw db ca > " + privateCAFile + "`."
			case bundle != "":
				ca = bundle
			default:
				return raw, "Name your system's CA bundle with --ca-file: MySQL's client verifies only against a CA file."
			}
		}
		if sslCARe.MatchString(raw) {
			return sslCARe.ReplaceAllString(raw, "${1}"+queryPath(ca)), note
		}
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + "ssl-ca=" + queryPath(ca), note
	}
	return raw, ""
}

// queryPath is a file path as a URL query value: percent-escaped (a space is
// %20: libpq reads "+" as itself), its slashes kept.
func queryPath(p string) string {
	return strings.ReplaceAll(url.PathEscape(p), "%2F", "/")
}
