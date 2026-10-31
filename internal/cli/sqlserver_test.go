package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func sqlServerOffer(f *fakeAPI) {
	f.answer("GET /v1/databases/info", 200, map[string]interface{}{
		"available": true,
		"engines": []map[string]interface{}{{"engine": "sqlserver", "versions": []string{"2025"},
			"sizes": []map[string]interface{}{
				{"code": "ms-2-4", "cpus": 2, "memory_mb": 4096, "disk_gb": 40, "hourly_micro_cents": "3200000", "monthly_micro_cents": "2336000000"},
				{"code": "ms-4-8", "cpus": 4, "memory_mb": 8192, "disk_gb": 80, "hourly_micro_cents": "6400000", "monthly_micro_cents": "4672000000"},
			}}},
	})
	f.answer("POST /v1/databases", 200, map[string]interface{}{"database": database("provisioning"), "password": "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu"})
}

// Microsoft's terms are the person's to accept: never by default, and a
// script must say so with a flag.
func TestSQLServerNeedsMicrosoftsTermsAccepted(t *testing.T) {
	f, srv := newFakeAPI(t)
	sqlServerOffer(f)
	_, _, err := run(t, srv, t.TempDir(), "db", "create", "erp", "--engine", "sqlserver", "--detach")
	if err == nil || !strings.Contains(err.Error(), "--accept-terms") {
		t.Fatalf("created without the terms accepted: %v", err)
	}
	f.mu.Lock()
	for _, c := range f.calls {
		if c.Method == "POST" && c.Path == "/v1/databases" {
			t.Error("the request was sent")
		}
	}
	f.mu.Unlock()
	_, _, err = runWithInput(t, srv, t.TempDir(), "n\n", "db", "create", "erp", "--engine", "sqlserver", "--detach")
	if err == nil || !strings.Contains(err.Error(), "licence terms") {
		t.Fatalf("a no was not a no: %v", err)
	}

	if _, _, err := run(t, srv, t.TempDir(), "db", "create", "erp", "--engine", "sqlserver", "--accept-terms",
		"--collation", "Latin1_General_CI_AS", "--detach"); err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases").Body)
	if body["engine"] != "sqlserver" || body["edition"] != "express" || body["accept_terms"] != true ||
		body["collation"] != "Latin1_General_CI_AS" || body["size"] != "ms-2-4" {
		t.Fatalf("sent %v", body)
	}
	if _, ok := body["product_key"]; ok {
		t.Fatalf("Express sent a key: %v", body)
	}
}

func TestSQLServerOwnLicenceNeedsAKeyAndTheAttestation(t *testing.T) {
	f, srv := newFakeAPI(t)
	sqlServerOffer(f)
	base := []string{"db", "create", "erp", "--engine", "sqlserver", "--size", "ms-4-8", "--accept-terms", "--detach"}

	if _, _, err := run(t, srv, t.TempDir(), append(base, "--edition", "standard", "--licence-attested")...); err == nil ||
		!strings.Contains(err.Error(), "--product-key") {
		t.Fatalf("a paid edition went without its key: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), append(base, "--edition", "express", "--product-key", "ABCDE-12345-FGHIJ-67890-KLMNO")...); err == nil ||
		!strings.Contains(err.Error(), "Express takes none") {
		t.Fatalf("Express took a key: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), append(base, "--edition", "standard", "--product-key", "ABCDE-12345-FGHIJ-67890-KLMNO")...); err == nil ||
		!strings.Contains(err.Error(), "--licence-attested") {
		t.Fatalf("an unattested licence was taken: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), append(base, "--edition", "developer")...); err == nil ||
		!strings.Contains(err.Error(), "express, standard, enterprise") {
		t.Fatalf("an edition not offered was taken: %v", err)
	}

	// The key read from standard input, never from the command line.
	if _, _, err := runWithInput(t, srv, t.TempDir(), "abcde-12345-fghij-67890-klmno\n",
		append(base, "--edition", "standard", "--product-key", "-", "--licence-attested")...); err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases").Body)
	if body["edition"] != "standard" || body["product_key"] != "ABCDE-12345-FGHIJ-67890-KLMNO" || body["licence_attestation"] != true {
		t.Fatalf("sent %v", body)
	}
}

func TestSQLServerFlagsAreForSQLServer(t *testing.T) {
	f, srv := newFakeAPI(t)
	sqlServerOffer(f)
	if _, _, err := run(t, srv, t.TempDir(), "db", "create", "loja", "--engine", "postgres", "--edition", "express"); err == nil ||
		!strings.Contains(err.Error(), "for SQL Server") {
		t.Fatalf("a PostgreSQL database took SQL Server's flags: %v", err)
	}
}

func sqlServerDB() map[string]interface{} {
	d := database("running")
	d["name"], d["engine"], d["version"] = "erp", "sqlserver", "2025"
	return d
}

// A .bak goes to the API part by part; a part that did not arrive is sent
// again, and only then is the import asked for. The parts go to the API the
// CLI signed in to, with its token, whatever address the ticket's part_urls
// name (the API's own idea of its name, which need not be the CLI's).
func TestSQLServerImportSendsTheBakPartByPart(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/erp", 200, sqlServerDB())
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
		w.WriteHeader(200)
	}))
	defer other.Close()
	var mu sync.Mutex
	received := map[string]int{}
	failedOnce := false
	partPath := "/v1/databases/erp/imports/uploads/job1/parts/"
	for _, n := range []string{"1", "2"} {
		n := n
		f.on("PUT "+partPath+n, func(w http.ResponseWriter, c call) {
			mu.Lock()
			defer mu.Unlock()
			if c.Header.Get("Content-Type") != "application/octet-stream" {
				w.WriteHeader(415)
				return
			}
			if n == "2" && !failedOnce {
				failedOnce = true
				w.WriteHeader(500)
				return
			}
			received[n] = len(c.Body)
			w.WriteHeader(204)
		})
	}
	ticket := func(parts []int) map[string]interface{} {
		urls := []string{}
		for _, n := range parts {
			urls = append(urls, other.URL+partPath+strconv.Itoa(n))
		}
		return map[string]interface{}{"part_bytes": 6, "part_count": 2, "part_numbers": parts, "part_urls": urls,
			"expires_at": "2026-10-31T06:00:00Z", "uploaded_bytes": 0}
	}
	job := map[string]interface{}{"id": "job1", "kind": "import", "status": "uploading"}
	f.answer("POST /v1/databases/erp/imports/uploads", 201, map[string]interface{}{"job": job, "upload": ticket([]int{1, 2})})
	f.answer("POST /v1/databases/erp/imports/uploads/job1/parts", 200, map[string]interface{}{"job": job, "upload": ticket([]int{2})})
	f.answer("POST /v1/databases/erp/imports/uploads/job1/complete", 202, map[string]interface{}{"id": "job1", "kind": "import", "status": "queued"})
	f.answer("GET /v1/databases/erp/jobs", 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "succeeded"}})

	dir := t.TempDir()
	file := filepath.Join(dir, "erp-2026.bak")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, srv, dir, "db", "import", "erp", file, "--database", "empresa2")
	if err != nil {
		t.Fatal(err)
	}
	start := jsonOf(t, f.last("POST /v1/databases/erp/imports/uploads").Body)
	if start["file_name"] != "erp-2026.bak" || start["size_bytes"] != float64(10) || start["database"] != "empresa2" {
		t.Fatalf("started with %v", start)
	}
	mu.Lock()
	if received["1"] != 6 || received["2"] != 4 || !failedOnce || elsewhere != 0 {
		t.Fatalf("the API got %v; the part_urls' host got %d", received, elsewhere)
	}
	mu.Unlock()
	if !strings.Contains(out, "Imported erp-2026.bak into erp, as its database empresa2") {
		t.Fatalf("said %q", out)
	}

	if _, _, err := run(t, srv, dir, "db", "import", "erp", filepath.Join(dir, "dump.sql")); err == nil || !strings.Contains(err.Error(), ".bak") {
		t.Fatalf("a SQL dump was taken for SQL Server: %v", err)
	}
}

// An upload left open by an earlier run carries on when the same file is
// sent again: only the parts the store lacks, and no second upload.
func TestSQLServerImportCarriesOnWithAnOpenUpload(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/erp", 200, sqlServerDB())
	var mu sync.Mutex
	sent := []string{}
	for _, n := range []string{"1", "2"} {
		n := n
		f.on("PUT /v1/databases/erp/imports/uploads/job1/parts/"+n, func(w http.ResponseWriter, c call) {
			mu.Lock()
			sent = append(sent, n)
			mu.Unlock()
			w.WriteHeader(200)
		})
	}
	open := map[string]interface{}{"id": "job1", "kind": "import", "status": "uploading", "source": "erp-2026.bak",
		"size_bytes": "10", "database": "empresa2", "replace": false, "created_at": "2026-10-31T03:11:04Z"}
	f.on("GET /v1/databases/erp/jobs", func(w http.ResponseWriter, _ call) {
		if f.called("POST /v1/databases/erp/imports/uploads/job1/complete") {
			reply(w, 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "succeeded"}})
			return
		}
		reply(w, 200, []map[string]interface{}{open})
	})
	f.answer("POST /v1/databases/erp/imports/uploads/job1/parts", 200, map[string]interface{}{"job": open,
		"upload": map[string]interface{}{"part_bytes": 6, "part_count": 2, "part_numbers": []int{2}, "uploaded_bytes": 6}})
	f.answer("POST /v1/databases/erp/imports/uploads/job1/complete", 202, map[string]interface{}{"id": "job1", "kind": "import", "status": "queued"})

	dir := t.TempDir()
	file := filepath.Join(dir, "erp-2026.bak")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, srv, dir, "db", "import", "erp", file, "--database", "empresa2")
	if err != nil {
		t.Fatal(err)
	}
	if f.called("POST /v1/databases/erp/imports/uploads") {
		t.Fatal("started a second upload")
	}
	mu.Lock()
	if strings.Join(sent, ",") != "2" {
		t.Fatalf("sent parts %v, not just the missing one", sent)
	}
	mu.Unlock()
	if !strings.Contains(out, "Carrying on with the upload of erp-2026.bak") || !strings.Contains(out, "Imported erp-2026.bak into erp") {
		t.Fatalf("said %q", out)
	}
}

// Another file's upload is in the way: the command says which, and --cancel
// drops it.
func TestSQLServerImportNamesAnotherOpenUploadAndCancelsIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/erp", 200, sqlServerDB())
	f.answer("GET /v1/databases/erp/jobs", 200, []map[string]interface{}{{"id": "job9", "kind": "import", "status": "uploading",
		"source": "old.bak", "size_bytes": "99", "created_at": "2026-10-31T03:11:04Z"}})
	f.answer("DELETE /v1/databases/erp/imports/uploads/job9", 200, map[string]interface{}{"id": "job9", "status": "failed"})
	dir := t.TempDir()
	file := filepath.Join(dir, "new.bak")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, srv, dir, "db", "import", "erp", file)
	if err == nil || !strings.Contains(err.Error(), "old.bak") || !strings.Contains(err.Error(), "--cancel") {
		t.Fatalf("went ahead past another upload: %v", err)
	}
	if f.called("POST /v1/databases/erp/imports/uploads") {
		t.Fatal("started an upload while another was open")
	}
	out, _, err := run(t, srv, dir, "db", "import", "erp", "--cancel")
	if err != nil {
		t.Fatal(err)
	}
	if !f.called("DELETE /v1/databases/erp/imports/uploads/job9") || !strings.Contains(out, "Dropped the upload of old.bak") {
		t.Fatalf("did not drop it: %q", out)
	}
}

// A restore installs SQL Server afresh: the terms are accepted again, the
// original's edition (and key) carry over unless another is asked for, and
// an own licence is stated to cover the new machine too.
func TestSQLServerRestoreAsksForTheTermsAndTheLicence(t *testing.T) {
	f, srv := newFakeAPI(t)
	src := sqlServerDB()
	src["licence"] = map[string]interface{}{"edition": "standard", "has_product_key": true, "running_edition": "standard", "key_applied": true}
	f.answer("GET /v1/databases/erp", 200, src)
	made := sqlServerDB()
	made["name"] = "erp-ontem"
	f.answer("POST /v1/databases/erp/restore", 200, map[string]interface{}{"database": made, "password": "x"})
	restore := func(extra ...string) error {
		_, _, err := run(t, srv, t.TempDir(), append([]string{"db", "restore", "erp", "--to", "erp-ontem", "--detach"}, extra...)...)
		return err
	}
	if err := restore(); err == nil || !strings.Contains(err.Error(), "--accept-terms") || f.called("POST /v1/databases/erp/restore") {
		t.Fatalf("restored without the terms: %v", err)
	}
	if err := restore("--accept-terms"); err == nil || !strings.Contains(err.Error(), "--licence-attested") || f.called("POST /v1/databases/erp/restore") {
		t.Fatalf("restored an own licence without saying it covers the machine: %v", err)
	}
	if err := restore("--accept-terms", "--licence-attested"); err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases/erp/restore").Body)
	if body["accept_terms"] != true || body["licence_attestation"] != true || body["edition"] != nil || body["product_key"] != nil {
		t.Fatalf("sent %v", body)
	}
	// Down to Express: no key, nothing to state.
	if err := restore("--accept-terms", "--edition", "express"); err != nil {
		t.Fatal(err)
	}
	body = jsonOf(t, f.last("POST /v1/databases/erp/restore").Body)
	if body["edition"] != "express" || body["licence_attestation"] != nil || body["product_key"] != nil {
		t.Fatalf("sent %v", body)
	}
	if err := restore("--accept-terms", "--edition", "express", "--product-key", "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE"); err == nil {
		t.Fatal("a key went with Express")
	}
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	if _, _, err := run(t, srv, t.TempDir(), "db", "restore", "loja-prod", "--to", "x", "--accept-terms"); err == nil || !strings.Contains(err.Error(), "for SQL Server") {
		t.Fatalf("a PostgreSQL restore took SQL Server's flags: %v", err)
	}
}

// What a SQL Server database runs is on its page: the edition (and a key
// SQL Server refused), the collation, its other databases, the warnings.
func TestSQLServerGetShowsTheEditionAndWarnings(t *testing.T) {
	f, srv := newFakeAPI(t)
	d := sqlServerDB()
	d["collation"] = "Latin1_General_CI_AS"
	d["extra_databases"] = []string{"copia"}
	d["licence"] = map[string]interface{}{"edition": "standard", "has_product_key": true, "running_edition": "express",
		"key_applied": false, "problem": "the product key was refused"}
	d["warnings"] = []map[string]interface{}{{"code": "licence_key_refused", "severity": "critical", "message": "SQL Server refused the product key: it runs as Express."}}
	f.answer("GET /v1/databases/erp", 200, d)
	out, _, err := run(t, srv, t.TempDir(), "db", "get", "erp")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standard (your own licence) · running Express: the product key was refused", "Latin1_General_CI_AS",
		"Also databases", "copia", "SQL Server refused the product key: it runs as Express."} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not in\n%s", want, out)
		}
	}
}

// The sqlcmd taken is one that can check the database's certificate through
// the tunnel: Microsoft's ODBC one or the Go one, by what its help offers. An
// older one is named as too old, never reported as missing.
func TestSqlcmdIsOneThatChecksTheCertificateName(t *testing.T) {
	for name, c := range map[string]struct{ help, want string }{
		"odbc 18":          {"usage: sqlcmd [-U login id] [-F Hostname in certificate] [-J Server certificate]", ""},
		"go 1.10":          {"-F,--host-name-in-certificate   Specifies the host name in the server certificate.   --vertical", ""},
		"go, -F as format": {"-F,--format   Specifies the screen formatting for results.   --vertical", "too old"},
		"odbc 17":          {"usage: sqlcmd [-U login id] [-N Encrypt Connection] [-C Trust Server Certificate]", "too old"},
	} {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '" + c.help + "'\n"
			if err := os.WriteFile(filepath.Join(bin, "sqlcmd"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			got, err := sqlcmdTool()
			if c.want == "" {
				if err != nil || got != filepath.Join(bin, "sqlcmd") {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "brew upgrade sqlcmd") {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	if _, err := os.Stat("/opt/mssql-tools18/bin/sqlcmd"); err == nil {
		return // this machine has Microsoft's tools where the CLI also looks
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := sqlcmdTool(); err == nil || !strings.Contains(err.Error(), "not installed") || !strings.Contains(err.Error(), "brew install sqlcmd") {
		t.Fatalf("no sqlcmd: %v", err)
	}
}

// sqlcmd gets the password in its environment, never on its command line,
// and an encrypted connection; the arguments after -- are passed on.
func TestSQLServerConnectRunsSqlcmd(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/erp", 200, sqlServerDB())
	f.answer("POST /v1/databases/erp/reveal-password", 200, map[string]string{"password": "s3cr3t-pw"})
	f.answer("GET /v1/databases/ca-certificate", 200, map[string]interface{}{"ca_certificate": nil})
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nif [ \"$1\" = '-?' ]; then printf '%s\\n' '-F,--host-name-in-certificate'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + record + "\nprintf 'SQLCMDPASSWORD=%s\\n' \"$SQLCMDPASSWORD\" >> " + record + "\n"
	if err := os.WriteFile(filepath.Join(bin, "sqlcmd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, err := run(t, srv, t.TempDir(), "db", "connect", "erp", "--no-tunnel", "--", "-Q", "select 1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	want := "-S\ntcp:db1.db.mwapp.test,5432\n-U\nloja_prod\n-d\nloja_prod\n-N\n-Q\nselect 1\nSQLCMDPASSWORD=s3cr3t-pw\n"
	if got != want {
		t.Fatalf("sqlcmd got\n%s\nwant\n%s", got, want)
	}
}
