package cli

import (
	"net/http"
	"strings"
	"testing"
)

// onThisMachine pins what `mw db url` finds here: psql's major version (0:
// none) and the system's CA bundle.
func onThisMachine(t *testing.T, psql int, bundle string) {
	t.Helper()
	prevPsql, prevBundle := localPsqlMajor, localCABundle
	localPsqlMajor = func() int { return psql }
	localCABundle = func() string { return bundle }
	t.Cleanup(func() { localPsqlMajor, localCABundle = prevPsql, prevBundle })
}

func instances() []map[string]interface{} {
	return []map[string]interface{}{
		{"id": "25380", "name": "web-1", "hostname": nil, "datacenter_id": 2, "addresses": []string{"148.224.62.22", "2804:6dec:10::7"}},
		{"id": "25381", "name": "worker", "hostname": "worker.example.com", "datacenter_id": 3, "addresses": []string{"148.224.62.23"}},
		{"id": "25382", "name": "worker", "hostname": nil, "datacenter_id": 2, "addresses": []string{}},
	}
}

func TestDbAllowPicksAnInstanceByNameOrId(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("GET /v1/databases/instances", 200, instances())
	f.on("PATCH /v1/databases/loja-prod", func(w http.ResponseWriter, c call) {
		d := database("running")
		d["datacenter_id"] = 2
		d["allowed_vms"] = []map[string]interface{}{instances()[0], instances()[1]}
		reply(w, 200, d)
	})
	out, _, err := run(t, srv, t.TempDir(), "db", "allow", "loja-prod", "--instance", "WEB-1", "--instance", "25381")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("PATCH /v1/databases/loja-prod").Body)
	if _, typed := body["allowed_cidrs"]; typed {
		t.Fatalf("touched the typed addresses: %v", body)
	}
	vms := body["allowed_vms"].([]interface{})
	if len(vms) != 2 || vms[0] != "25380" || vms[1] != "25381" {
		t.Fatalf("sent %v", body)
	}
	if !strings.Contains(out, "web-1 (148.224.62.22, 2804:6dec:10::7)") || !strings.Contains(out, "worker runs in another location") {
		t.Fatalf("said %q", out)
	}

	// Two instances are called worker: the id decides.
	if _, _, err := run(t, srv, t.TempDir(), "db", "allow", "loja-prod", "--instance", "worker"); err == nil ||
		!strings.Contains(err.Error(), "2 instances are called \"worker\"") || !strings.Contains(err.Error(), "25381, 25382") {
		t.Fatalf("an ambiguous name was taken: %v", err)
	}
	// One with nothing to allow.
	if _, _, err := run(t, srv, t.TempDir(), "db", "allow", "loja-prod", "--instance", "25382"); err == nil ||
		!strings.Contains(err.Error(), "no public address") {
		t.Fatalf("an instance without an address was taken: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "allow", "loja-prod", "--instance", "nope"); err == nil ||
		!strings.Contains(err.Error(), "mw instances") {
		t.Fatalf("an unknown instance was taken: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "allow", "loja-prod"); err == nil ||
		!strings.Contains(err.Error(), "--instance") {
		t.Fatalf("allowed nothing: %v", err)
	}
}

func TestDbDisallowTakesAnInstanceOff(t *testing.T) {
	f, srv := newFakeAPI(t)
	d := database("running")
	d["allowed_vms"] = []map[string]interface{}{instances()[0]}
	d["allowed_cidrs"] = []string{"203.0.113.7/32"}
	f.answer("GET /v1/databases/loja-prod", 200, d)
	f.answer("GET /v1/databases/instances", 200, instances())
	f.on("PATCH /v1/databases/loja-prod", func(w http.ResponseWriter, c call) {
		out := database("running")
		out["allowed_cidrs"] = []string{"203.0.113.7/32"}
		reply(w, 200, out)
	})
	if _, _, err := run(t, srv, t.TempDir(), "db", "disallow", "loja-prod", "--instance", "web-1"); err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("PATCH /v1/databases/loja-prod").Body)
	if vms, ok := body["allowed_vms"].([]interface{}); !ok || len(vms) != 0 {
		t.Fatalf("sent %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "disallow", "loja-prod", "--instance", "25381"); err == nil ||
		!strings.Contains(err.Error(), "does not allow worker") {
		t.Fatalf("took off an instance it did not allow: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "disallow", "loja-prod", "198.51.100.9"); err == nil ||
		!strings.Contains(err.Error(), "loja-prod does not allow 198.51.100.9") {
		t.Fatalf("took off an address it did not allow: %v", err)
	}
}

func TestDbURLNamesRootsThisMachineReads(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/reveal-password", 200, map[string]string{"password": "pw"})

	// Ubuntu 22.04's psql 14 reads `system` as a file: the bundle instead, said on stderr.
	onThisMachine(t, 14, "/etc/ssl/certs/ca-certificates.crt")
	out, errOut, err := run(t, srv, t.TempDir(), "db", "url", "loja-prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "postgresql://loja_prod:pw@db1.db.mwapp.test:6432/loja_prod?sslmode=verify-full&sslrootcert=/etc/ssl/certs/ca-certificates.crt" ||
		!strings.Contains(errOut, "psql 14 here predates sslrootcert=system") {
		t.Fatalf("url %q, said %q", out, errOut)
	}
	// psql 16+, or none at all: the API's URL as it is.
	for _, psql := range []int{16, 0} {
		onThisMachine(t, psql, "/etc/ssl/certs/ca-certificates.crt")
		out, errOut, _ = run(t, srv, t.TempDir(), "db", "url", "loja-prod")
		if !strings.Contains(out, "sslrootcert=system") || errOut != "" {
			t.Fatalf("psql %d: url %q, said %q", psql, out, errOut)
		}
	}
	// For another machine: its bundle named.
	out, _, _ = run(t, srv, t.TempDir(), "db", "url", "loja-prod", "--direct", "--ca-file", "/etc/pki/tls/certs/ca-bundle.crt")
	if !strings.Contains(out, ":5432/loja_prod?sslmode=verify-full&sslrootcert=/etc/pki/tls/certs/ca-bundle.crt") {
		t.Fatalf("url %q", out)
	}
	// "system" keeps the API's, whatever psql is here.
	onThisMachine(t, 14, "/etc/ssl/certs/ca-certificates.crt")
	out, _, _ = run(t, srv, t.TempDir(), "db", "url", "loja-prod", "--ca-file", "system")
	if !strings.Contains(out, "sslrootcert=system") {
		t.Fatalf("url %q", out)
	}
}

func TestDbURLForMySQLNamesACAFile(t *testing.T) {
	f, srv := newFakeAPI(t)
	my := database("running")
	my["engine"] = "mysql"
	my["connection"] = map[string]interface{}{"host": "db1.db.mwapp.test", "port": 3306, "database": "wp", "username": "wp",
		"uri": "mysql://wp@db1.db.mwapp.test:3306/wp?ssl-mode=VERIFY_IDENTITY"}
	f.answer("GET /v1/databases/loja-prod", 200, my)
	f.answer("POST /v1/databases/loja-prod/reveal-password", 200, map[string]string{"password": "pw"})
	f.answer("GET /v1/databases/ca-certificate", 200, map[string]interface{}{"ca_certificate": nil})
	onThisMachine(t, 0, "/etc/pki/tls/certs/ca-bundle.crt")
	out, _, err := run(t, srv, t.TempDir(), "db", "url", "loja-prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "mysql://wp:pw@db1.db.mwapp.test:3306/wp?ssl-mode=VERIFY_IDENTITY&ssl-ca=/etc/pki/tls/certs/ca-bundle.crt" {
		t.Fatalf("url %q", out)
	}
	// This environment's own CA: its file, and how to get it.
	f.answer("GET /v1/databases/ca-certificate", 200, map[string]interface{}{"ca_certificate": "-----BEGIN CERTIFICATE-----"})
	out, errOut, _ := run(t, srv, t.TempDir(), "db", "url", "loja-prod")
	if !strings.Contains(out, "&ssl-ca=database-ca.pem") || !strings.Contains(errOut, "mw db ca > database-ca.pem") {
		t.Fatalf("url %q, said %q", out, errOut)
	}
}

func TestUrlRootsLeavesTheRestOfTheURL(t *testing.T) {
	raw := "postgresql://a@h:6432/d?sslmode=verify-full&sslrootcert=system&application_name=x"
	got, _ := urlRoots("postgres", raw, "/my ca/bundle.pem", 0, "", false)
	if got != "postgresql://a@h:6432/d?sslmode=verify-full&sslrootcert=/my%20ca/bundle.pem&application_name=x" {
		t.Fatalf("got %q", got)
	}
	if got, _ := urlRoots("valkey", "rediss://a@h:6379", "", 0, "/b", false); got != "rediss://a@h:6379" {
		t.Fatalf("got %q", got)
	}
}
