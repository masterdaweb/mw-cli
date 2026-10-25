package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
)

const svcID = "0193f0e2-1111-7000-8000-000000000001"

type call struct {
	Method, Path string
	Query        map[string][]string
	Header       http.Header
	Body         []byte
}

// fakeAPI answers v1 routes ("GET /v1/…") and records every call.
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []call
	routes map[string]func(w http.ResponseWriter, c call)
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, routes: map[string]func(http.ResponseWriter, call){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer mwk_test" {
			w.WriteHeader(401)
			return
		}
		if h == nil {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		h(w, c)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) on(route string, h func(w http.ResponseWriter, c call)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fakeAPI) answer(route string, status int, v interface{}) {
	f.on(route, func(w http.ResponseWriter, _ call) { reply(w, status, v) })
}

func reply(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) last(route string) *call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method+" "+f.calls[i].Path == route {
			c := f.calls[i]
			return &c
		}
	}
	f.t.Fatalf("no %s", route)
	return nil
}

func jsonOf(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body %q: %v", raw, err)
	}
	return m
}

// run executes `mw args…` against the fake API, in dir.
func run(t *testing.T, srv *httptest.Server, dir string, args ...string) (string, string, error) {
	t.Helper()
	return runWithInput(t, srv, dir, "", args...)
}

// runWithInput is run with something on the command's standard input.
func runWithInput(t *testing.T, srv *httptest.Server, dir, input string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MW_TOKEN", "mwk_test")
	t.Setenv("MW_API_URL", srv.URL)
	pollEvery = 10 * time.Millisecond
	var out, errOut bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(input), Out: &out, Err: &errOut, Dir: dir}
	root := NewRoot(a)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func service(kind, source string) map[string]interface{} {
	return map[string]interface{}{
		"id": svcID, "project_id": "0193f0e2-2222-7000-8000-000000000001", "project_name": "loja",
		"name": "web", "kind": kind, "status": "LIVE",
		"source":   map[string]interface{}{"kind": source},
		"settings": map[string]interface{}{"vcpu_millis": 500, "memory_mib": 1024, "min_instances": 0, "max_instances": 3},
	}
}

func page(items ...interface{}) map[string]interface{} {
	return map[string]interface{}{"object": "list", "data": items, "has_more": false, "next_cursor": nil}
}

func TestEnvSetSendsTheVariablesAndSaysWhatChanged(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("PUT /v1/serverless/services/"+svcID+"/variables", 200, map[string]interface{}{
		"created": []string{"STRIPE_KEY"}, "updated": []string{"APP_ENV"}, "removed": []string{}, "unchanged": []string{},
		"deployment": map[string]interface{}{"id": "d1", "number": 7, "status": "QUEUED"},
	})
	out, _, err := run(t, srv, t.TempDir(), "env", "set", "--service", "web", "--secret", "STRIPE_KEY=sk_live_1=2", "APP_ENV=production")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("PUT /v1/serverless/services/"+svcID+"/variables").Body)
	vars := body["variables"].([]interface{})
	first := vars[0].(map[string]interface{})
	if first["key"] != "STRIPE_KEY" || first["value"] != "sk_live_1=2" || first["is_secret"] != true || body["deploy"] != true {
		t.Fatalf("sent %v", body)
	}
	if _, ok := first["available_at_build"]; ok {
		t.Fatal("--build was not passed, yet sent")
	}
	if !strings.Contains(out, "web: 1 added, 1 changed.") || !strings.Contains(out, "deployment #7") {
		t.Fatalf("said %q", out)
	}
	if f.last("PUT /v1/serverless/services/"+svcID+"/variables").Header.Get("Idempotency-Key") == "" {
		t.Fatal("a change without an Idempotency-Key")
	}
}

func TestEnvPullWritesWhatImportReads(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/variables", 200, []map[string]interface{}{
		{"key": "APP_NAME", "value": "Loja do Zé", "is_secret": false},
		{"key": "DB_PASSWORD", "value": nil, "is_secret": true},
		{"key": "PROMPT", "value": "say \"hi\"\n$HOME", "is_secret": false},
		{"key": "PLAIN", "value": "abc-123", "is_secret": false},
		{"key": "EMPTY", "value": "", "is_secret": false},
	})
	dir := t.TempDir()
	out, _, err := run(t, srv, dir, "env", "pull", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	want := "APP_NAME=\"Loja do Zé\"\nPROMPT=\"say \\\"hi\\\"\\n\\$HOME\"\nPLAIN=abc-123\nEMPTY=\n# Secrets are never shown again once saved: DB_PASSWORD\n"
	if out != want {
		t.Fatalf("pulled\n%s\nwant\n%s", out, want)
	}
	file := filepath.Join(dir, ".env")
	if _, _, err := run(t, srv, dir, "env", "pull", "--service", "web", file); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env is %v: others could read it", st.Mode().Perm())
	}
	if _, _, err := run(t, srv, dir, "env", "pull", "--service", "web", file); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrote a file: %v", err)
	}
}

func TestDeployingAFolderSendsItsCodeAndFollowsTheBuild(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"name":"site"}`, "index.js": "console.log(1)",
		".gitignore": "node_modules/\n", "node_modules/x/i.js": "dep",
	} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	if err := config.SaveLink(dir, config.Link{ServiceID: svcID, ServiceName: "web"}); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "node_modules")
	f.answer("GET /v1/serverless/services/"+svcID, 200, service("WEB", "UPLOAD"))
	f.answer("GET /v1/serverless/access", 200, map[string]interface{}{"enabled": true, "upload_max_bytes": 1 << 20})
	f.answer("POST /v1/serverless/services/"+svcID+"/uploads", 200, map[string]interface{}{
		"id": "dep-1", "number": 3, "status": "QUEUED",
		"steps": []map[string]interface{}{{"kind": "SOURCE", "status": "SUCCEEDED"}},
	})
	f.answer("GET /v1/serverless/deployments/dep-1", 200, map[string]interface{}{
		"id": "dep-1", "number": 3, "status": "LIVE",
		"steps": []map[string]interface{}{{"kind": "SOURCE", "status": "SUCCEEDED"}, {"kind": "BUILD", "status": "SUCCEEDED"}, {"kind": "TRAFFIC", "status": "SUCCEEDED"}},
	})
	// From a subfolder: the linked folder is what is sent.
	out, _, err := run(t, srv, sub, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	up := f.last("POST /v1/serverless/services/" + svcID + "/uploads")
	if up.Header.Get("Content-Type") != "application/gzip" || up.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("headers %v", up.Header)
	}
	if !strings.HasPrefix(up.Query["message"][0], "mw deploy from "+filepath.Base(dir)) {
		t.Fatalf("message %v", up.Query["message"])
	}
	gz, err := gzip.NewReader(bytes.NewReader(up.Body))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if got := strings.Join(names, " "); strings.Contains(got, "node_modules") || !strings.Contains(got, "index.js") || strings.Contains(got, ".mw") {
		t.Fatalf("sent %s", got)
	}
	if !strings.Contains(out, "Deployment #3 of web:") || !regexp.MustCompile(`\n  build +succeeded\n`).MatchString(out) ||
		strings.Contains(out, "pending") || !strings.Contains(out, "Deployment #3 is live.") {
		t.Fatalf("said %q", out)
	}

	// A Git service is not deployed from a folder.
	f.answer("GET /v1/serverless/services/"+svcID, 200, service("WEB", "GIT"))
	if _, _, err := run(t, srv, dir, "deploy", "."); err == nil || !strings.Contains(err.Error(), "deploys from its repository") {
		t.Fatalf("a Git service took a folder: %v", err)
	}
}

func TestAFailedDeploymentSaysWhyAndExitsNonZero(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/deployments", 200, map[string]interface{}{"id": "d9", "number": 9, "status": "BUILDING"})
	f.answer("GET /v1/serverless/deployments/d9", 200, map[string]interface{}{
		"id": "d9", "number": 9, "status": "FAILED",
		"failure": map[string]interface{}{"code": "BUILD_FAILED", "message": "The build failed.", "hint": "Fix the error and push again.", "log_tail": "npm ERR! missing script: build"},
	})
	_, _, err := run(t, srv, t.TempDir(), "deploy", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "deployment #9 failed: The build failed.") || !strings.Contains(err.Error(), "npm ERR! missing script") {
		t.Fatalf("got %v", err)
	}
}

func TestANameInTwoProjectsAsksWhichProject(t *testing.T) {
	f, srv := newFakeAPI(t)
	other := service("WEB", "GIT")
	other["id"], other["project_name"] = "0193f0e2-1111-7000-8000-000000000002", "blog"
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT"), other))
	_, _, err := run(t, srv, t.TempDir(), "status", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "more than one project (blog, loja): pass --project") {
		t.Fatalf("got %v", err)
	}
}

func database(status string) map[string]interface{} {
	return map[string]interface{}{
		"id": "db1", "name": "loja-prod", "engine": "postgres", "version": 18, "status": status,
		"size": map[string]interface{}{"code": "pg-1-1", "cpus": 1, "memory_mb": 1024, "disk_gb": 10},
		"connection": map[string]interface{}{
			"host": "db1.db.mwapp.test", "port": 5432, "pooler_port": 6432, "database": "loja_prod", "username": "loja_prod",
			"uri":        "postgresql://loja_prod@db1.db.mwapp.test:6432/loja_prod?sslmode=require",
			"direct_uri": "postgresql://loja_prod@db1.db.mwapp.test:5432/loja_prod?sslmode=require",
		},
		"allowed_cidrs": []string{}, "extensions": []string{"vector"}, "health": map[string]interface{}{"state": "ok"},
		"cost": map[string]interface{}{"month_to_date_micro_cents": "1234560000"}, "deletion_protection": true,
	}
}

func TestDbCreateShowsThePasswordOnceAndWaitsUntilReady(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/info", 200, map[string]interface{}{
		"available": true,
		"engines": []map[string]interface{}{{"engine": "postgres", "versions": []int{18, 17},
			"sizes": []map[string]interface{}{{"code": "pg-1-1", "cpus": 1, "memory_mb": 1024, "disk_gb": 10, "hourly_micro_cents": "62500", "monthly_micro_cents": "45625000"}}}},
	})
	f.answer("POST /v1/databases", 200, map[string]interface{}{"database": database("provisioning"), "password": "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu"})
	polls := 0
	f.on("GET /v1/databases/loja-prod", func(w http.ResponseWriter, _ call) {
		polls++
		if polls < 3 {
			reply(w, 200, database("provisioning"))
			return
		}
		reply(w, 200, database("running"))
	})
	out, _, err := run(t, srv, t.TempDir(), "db", "create", "loja-prod", "--extension", "vector", "--allow", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases").Body)
	if body["engine"] != "postgres" || body["size"] != "pg-1-1" || body["extensions"].([]interface{})[0] != "vector" || body["allowed_cidrs"].([]interface{})[0] != "203.0.113.7" {
		t.Fatalf("sent %v", body)
	}
	if strings.Count(out, "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu") != 1 || !strings.Contains(out, "loja-prod is ready") {
		t.Fatalf("said %q", out)
	}

	// `mw redis` is the same commands on the other engine only.
	if _, _, err := run(t, srv, t.TempDir(), "redis", "get", "loja-prod"); err == nil || !strings.Contains(err.Error(), "use `mw db`") {
		t.Fatalf("redis took a PostgreSQL database: %v", err)
	}
}

func TestDbCreateTellsADeveloperWhoHasThePassword(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/info", 200, map[string]interface{}{
		"available": true,
		"engines": []map[string]interface{}{{"engine": "postgres", "versions": []int{18},
			"sizes": []map[string]interface{}{{"code": "pg-1-1", "cpus": 1, "memory_mb": 1024, "disk_gb": 10, "hourly_micro_cents": "62500", "monthly_micro_cents": "45625000"}}}},
	})
	// A developer's key: the API keeps the secret with the administrators.
	f.answer("POST /v1/databases", 200, map[string]interface{}{"database": database("provisioning"), "password": nil})
	out, _, err := run(t, srv, t.TempDir(), "db", "create", "loja-prod", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stays with the account's administrators") || !strings.Contains(out, "mw db password loja-prod") || strings.Contains(out, "shown here once") {
		t.Fatalf("said %q", out)
	}
}

func TestDbURLCarriesThePasswordAndListMoney(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/reveal-password", 200, map[string]string{"password": "p@ss/w:rd"})
	out, _, err := run(t, srv, t.TempDir(), "db", "url", "loja-prod", "--direct")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "postgresql://loja_prod:p%40ss%2Fw%3Ard@db1.db.mwapp.test:5432/loja_prod?sslmode=require" {
		t.Fatalf("url %q", out)
	}
	f.answer("GET /v1/databases", 200, []interface{}{database("running")})
	out, _, err = run(t, srv, t.TempDir(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "R$ 1.234,56") || !strings.Contains(out, "PostgreSQL 18") {
		t.Fatalf("list %q", out)
	}
}

func TestDbConnectRunsPsqlWithThePasswordInTheEnvironmentOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for psql")
	}
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/reveal-password", 200, map[string]string{"password": "s3cr3t-pw"})
	f.answer("GET /v1/databases/ca-certificate", 200, map[string]interface{}{"ca_certificate": "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"})
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'psql (PostgreSQL) 17.2'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + record + "\necho \"PGPASSWORD=$PGPASSWORD\" >> " + record + "\ncat \"$(echo \"$1\" | sed 's/.*sslrootcert=//')\" >> " + record + "\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, err := run(t, srv, t.TempDir(), "db", "connect", "loja-prod", "--no-tunnel", "--", "-c", "select 1")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 2 || !strings.Contains(exit.msg, "mw db allow loja-prod") {
		t.Fatalf("got %v", err)
	}
	got, _ := os.ReadFile(record)
	lines := strings.Split(string(got), "\n")
	if !strings.HasPrefix(lines[0], "host=db1.db.mwapp.test port=5432 dbname=loja_prod user=loja_prod sslmode=verify-full sslrootcert=") {
		t.Fatalf("dsn %q", lines[0])
	}
	if lines[1] != "-c" || lines[2] != "select 1" || lines[3] != "PGPASSWORD=s3cr3t-pw" || !strings.Contains(string(got), "BEGIN CERTIFICATE") {
		t.Fatalf("psql got %q", got)
	}
	if strings.Contains(lines[0]+lines[1]+lines[2], "s3cr3t-pw") {
		t.Fatal("the password is on the command line")
	}
}

func TestStorageKeysShowTheSecretOnceAndRevokeByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/object-storage/keys", 200, map[string]interface{}{
		"key":               map[string]interface{}{"id": "0193f0e2-3333-7000-8000-000000000001", "name": "backup", "access_key_id": "MWAKEXAMPLE", "access": "read_only", "buckets": []string{"loja-backups"}},
		"secret_access_key": "secret-shown-once",
	})
	out, _, err := run(t, srv, t.TempDir(), "storage", "keys", "create", "backup", "--read-only", "--bucket", "loja-backups", "--expires", "2027-01-31")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/object-storage/keys").Body)
	exp, _ := time.Parse(time.RFC3339, body["expires_at"].(string))
	if body["access"] != "read_only" || body["buckets"].([]interface{})[0] != "loja-backups" || exp.Local().Format("2006-01-02 15:04") != "2027-01-31 23:59" {
		t.Fatalf("sent %v", body)
	}
	if strings.Count(out, "secret-shown-once") != 1 || !strings.Contains(out, "MWAKEXAMPLE") {
		t.Fatalf("said %q", out)
	}
	f.answer("GET /v1/object-storage/keys", 200, []map[string]interface{}{{"id": "0193f0e2-3333-7000-8000-000000000001", "name": "backup", "access_key_id": "MWAKEXAMPLE", "access": "read_only"}})
	f.answer("DELETE /v1/object-storage/keys/0193f0e2-3333-7000-8000-000000000001", 200, map[string]bool{"revoked": true})
	if out, _, err = run(t, srv, t.TempDir(), "storage", "keys", "revoke", "backup"); err != nil || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke: %v %q", err, out)
	}
}

func TestWalletTopupRaisesAnInvoiceInCents(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/wallet", 200, map[string]interface{}{"subscription_id": "sub-1", "usable_micro_cents": "12500000", "runway_hours": 30})
	f.answer("POST /v1/subscriptions/sub-1/top-ups", 200, map[string]interface{}{"invoice_id": "INV-77", "amount_cents": "4990", "pay_url": "https://pay.example/inv-77", "due_at": "2026-10-27T03:00:00.000Z"})
	out, _, err := run(t, srv, t.TempDir(), "wallet", "topup", "49,90")
	if err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/subscriptions/sub-1/top-ups").Body); body["amount_cents"] != float64(4990) {
		t.Fatalf("sent %v", body)
	}
	if !strings.Contains(out, "R$ 49,90") || !strings.Contains(out, "https://pay.example/inv-77") {
		t.Fatalf("said %q", out)
	}
	f.answer("GET /v1/serverless/spend-cap", 200, map[string]interface{}{
		"cap_micro_cents": "200000000", "action": "WARN", "spent_micro_cents": "50000000",
		"period_from": "2026-10-01T00:00:00.000Z", "period_to": "2026-11-01T00:00:00.000Z", "stopped": false,
	})
	out, _, err = run(t, srv, t.TempDir(), "wallet")
	if err != nil || !strings.Contains(out, "R$ 12,50") || !strings.Contains(out, "about 30 hours") || !strings.Contains(out, "R$ 50,00 of R$ 200,00 this period (tells you when reached)") {
		t.Fatalf("wallet: %v %q", err, out)
	}
}

func TestWalletRechargeTurnsOnWithAnAmountAndOff(t *testing.T) {
	f, srv := newFakeAPI(t)
	on := map[string]interface{}{"subscription_id": "sub-1", "usable_micro_cents": "12500000", "auto_recharge_enabled": true, "auto_recharge_amount_micro_cents": "100000000"}
	f.answer("PUT /v1/serverless/wallet/auto-recharge", 200, on)
	out, _, err := run(t, srv, t.TempDir(), "wallet", "recharge", "on", "100")
	if err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("PUT /v1/serverless/wallet/auto-recharge").Body); body["enabled"] != true || body["amount_micro_cents"] != "100000000" {
		t.Fatalf("sent %v", body)
	}
	if !strings.Contains(out, "on: R$ 100,00 when the credit has 3 days left") {
		t.Fatalf("said %q", out)
	}
	if _, _, err := run(t, srv, t.TempDir(), "wallet", "recharge", "on", "5"); err == nil || !strings.Contains(err.Error(), "R$ 20") {
		t.Fatalf("too little: %v", err)
	}
	f.answer("PUT /v1/serverless/wallet/auto-recharge", 200, map[string]interface{}{"subscription_id": "sub-1", "usable_micro_cents": "12500000", "auto_recharge_enabled": false, "auto_recharge_amount_micro_cents": "100000000"})
	out, _, err = run(t, srv, t.TempDir(), "wallet", "recharge", "off")
	if err != nil || !strings.Contains(out, "off — when the credit runs out, the services stop") {
		t.Fatalf("off: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PUT /v1/serverless/wallet/auto-recharge").Body); body["enabled"] != false || body["amount_micro_cents"] != nil {
		t.Fatalf("off sent %v", body)
	}
}

func TestWalletCapSetsAStopAndSaysWhatItDoes(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("PUT /v1/serverless/spend-cap", 200, map[string]interface{}{
		"cap_micro_cents": "500000000", "action": "STOP", "spent_micro_cents": "123450000",
		"period_from": "2026-10-01T00:00:00.000Z", "period_to": "2026-11-01T00:00:00.000Z", "stopped": false,
	})
	out, _, err := run(t, srv, t.TempDir(), "wallet", "cap", "set", "500", "--stop")
	if err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("PUT /v1/serverless/spend-cap").Body); body["cap_micro_cents"] != "500000000" || body["action"] != "STOP" {
		t.Fatalf("sent %v", body)
	}
	if !strings.Contains(out, "R$ 500,00 a period") || !strings.Contains(out, "R$ 123,45") || !strings.Contains(out, "the services stop") {
		t.Fatalf("said %q", out)
	}
	if _, _, err := run(t, srv, t.TempDir(), "wallet", "cap", "set", "0,50"); err == nil || !strings.Contains(err.Error(), "at least R$ 1") {
		t.Fatalf("a cap under R$ 1: %v", err)
	}
	// Removed: a null cap, whatever it did.
	f.answer("PUT /v1/serverless/spend-cap", 200, map[string]interface{}{
		"cap_micro_cents": nil, "action": "WARN", "spent_micro_cents": "0",
		"period_from": "2026-10-01T00:00:00.000Z", "period_to": "2026-11-01T00:00:00.000Z", "stopped": false,
	})
	out, _, err = run(t, srv, t.TempDir(), "wallet", "cap", "remove")
	if err != nil || !strings.Contains(out, "none") {
		t.Fatalf("remove: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PUT /v1/serverless/spend-cap").Body); body["cap_micro_cents"] != nil {
		t.Fatalf("sent %v", body)
	}
}

func TestInitMakesTheProjectTheServiceAndTheLink(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := filepath.Join(t.TempDir(), "Minha Loja")
	_ = os.Mkdir(dir, 0o755)
	f.answer("GET /v1/serverless/projects", 200, page())
	f.answer("POST /v1/serverless/projects", 200, map[string]interface{}{"id": "0193f0e2-2222-7000-8000-000000000009", "name": "minha-loja"})
	f.answer("POST /v1/serverless/projects/0193f0e2-2222-7000-8000-000000000009/services", 200, map[string]interface{}{"service": service("WEB", "UPLOAD")})
	if _, _, err := run(t, srv, dir, "init", "--port", "3000"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/projects").Body); body["name"] != "minha-loja" {
		t.Fatalf("project %v", body)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/projects/0193f0e2-2222-7000-8000-000000000009/services").Body)
	if body["name"] != "minha-loja" || body["kind"] != "WEB" || body["source"].(map[string]interface{})["kind"] != "UPLOAD" || body["settings"].(map[string]interface{})["port"] != float64(3000) {
		t.Fatalf("service %v", body)
	}
	l, at, _ := config.FindLink(dir)
	if l == nil || l.ServiceID != svcID || at != dir {
		t.Fatalf("link %+v at %s", l, at)
	}
	if _, _, err := run(t, srv, dir, "init"); err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Fatalf("init twice: %v", err)
	}
}

func TestDomainsAddPrintsTheRecordsToCreate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/domains", 200, map[string]interface{}{
		"id": "dom1", "hostname": "app.loja.com.br", "status": "PENDING_DNS",
		"instructions": []map[string]interface{}{
			{"type": "CNAME", "name": "app.loja.com.br", "value": "cname.mwapp.com.br"},
			{"type": "TXT", "name": "_mw-verify.app.loja.com.br", "value": "mw-verify=abc", "found": false},
		},
	})
	out, _, err := run(t, srv, t.TempDir(), "domains", "add", "App.Loja.com.br", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/services/"+svcID+"/domains").Body); body["hostname"] != "app.loja.com.br" {
		t.Fatalf("sent %v", body)
	}
	for _, want := range []string{"CNAME", "cname.mwapp.com.br", "_mw-verify.app.loja.com.br", "not found yet", "mw domains check app.loja.com.br"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestRefusalsSayWhatToDoNext(t *testing.T) {
	scope := Describe(&api.Problem{Status: 403, Code: "insufficient_scope", Detail: "This token cannot do that.", RequestID: "req-1"})
	if !strings.Contains(scope, "Create one with that scope") || !strings.Contains(scope, "(request req-1)") {
		t.Fatalf("scope: %s", scope)
	}
	credit := Describe(&api.Problem{Status: 402, Code: "insufficient_balance", Reason: "DATABASE_CREDIT_REQUIRED", Detail: "Add credit."})
	if !strings.Contains(credit, "mw wallet") {
		t.Fatalf("credit: %s", credit)
	}
	t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("MW_TOKEN", "")
	a := &App{Version: "test", In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, Dir: t.TempDir()}
	if _, err := a.Client(); err == nil || !strings.Contains(err.Error(), "mw login") {
		t.Fatalf("unsigned: %v", err)
	}
}

func TestProjectsUpdateTurnsProtectionOffAndFreezesDeploys(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja", "deletion_protection": true}))
	f.on("PATCH /v1/serverless/projects/"+pid, func(w http.ResponseWriter, c call) {
		body := jsonOf(t, c.Body)
		reply(w, 200, map[string]interface{}{"id": pid, "name": "loja", "deletion_protection": body["deletion_protection"] != false, "deploys_frozen": body["deploys_frozen"] == true})
	})
	out, _, err := run(t, srv, t.TempDir(), "projects", "update", "loja", "--deletion-protection=false")
	if err != nil || !strings.Contains(out, "loja saved: deletion protection off, deploys allowed.") {
		t.Fatalf("protection off: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/projects/"+pid).Body); len(body) != 1 || body["deletion_protection"] != false {
		t.Fatalf("protection body %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "projects", "update", "loja", "--freeze", "Black Friday"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/projects/"+pid).Body); body["deploys_frozen"] != true || body["frozen_reason"] != "Black Friday" {
		t.Fatalf("freeze body %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "projects", "update", "loja", "--unfreeze"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/projects/"+pid).Body); body["deploys_frozen"] != false || body["frozen_reason"] != nil {
		t.Fatalf("unfreeze body %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "projects", "update", "loja"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("no flags: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "projects", "update", "loja", "--freeze", "x", "--unfreeze"); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("freeze and unfreeze: %v", err)
	}
}

func TestServicesUpdateSetsDeletionProtection(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.on("PUT /v1/serverless/services/"+svcID+"/deletion-protection", func(w http.ResponseWriter, c call) {
		s := service("WEB", "GIT")
		s["deletion_protection"] = jsonOf(t, c.Body)["enabled"]
		reply(w, 200, s)
	})
	out, _, err := run(t, srv, t.TempDir(), "services", "update", "web", "--deletion-protection=true")
	if err != nil || !strings.Contains(out, "web saved: deletion protection on.") {
		t.Fatalf("on: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PUT /v1/serverless/services/"+svcID+"/deletion-protection").Body); body["enabled"] != true {
		t.Fatalf("body %v", body)
	}
	out, _, err = run(t, srv, t.TempDir(), "services", "update", "web", "--deletion-protection=false")
	if err != nil || !strings.Contains(out, "deletion protection off") {
		t.Fatalf("off: %v %q", err, out)
	}
	if _, _, err := run(t, srv, t.TempDir(), "services", "update", "web"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("no flags: %v", err)
	}
}

func TestTemplatesDeployMakesTheProjectAndFollowsItLive(t *testing.T) {
	f, srv := newFakeAPI(t)
	laravel := map[string]interface{}{
		"id": "laravel", "name": "Laravel", "summary": map[string]string{"pt": "…", "en": "A Laravel site."}, "category": "framework",
		"services":  []map[string]interface{}{{"name": "web", "kind": "WEB"}, {"name": "worker", "kind": "WORKER"}},
		"databases": []map[string]interface{}{{"key": "db", "engine": "postgres", "size": "pg-1-1"}, {"key": "cache", "engine": "valkey", "size": "kv-1-1"}},
		"buckets":   []map[string]interface{}{{"key": "files", "public": true}},
		"questions": []map[string]interface{}{},
	}
	f.answer("GET /v1/serverless/templates", 200, []interface{}{laravel})
	out, _, err := run(t, srv, t.TempDir(), "templates")
	if err != nil || !strings.Contains(out, "2 services, PostgreSQL, Redis, Object Storage") {
		t.Fatalf("list: %v %q", err, out)
	}

	f.answer("POST /v1/serverless/templates/laravel/deploy", 200, map[string]interface{}{
		"project_id": "p1",
		"services":   []map[string]interface{}{{"id": "s1", "name": "web", "deployment_id": "d1"}, {"id": "s2", "name": "worker", "deployment_id": "d2"}},
		"databases":  []string{"loja-db", "loja-cache"}, "buckets": []string{"loja-files-ab12cd"},
	})
	for _, d := range []string{"d1", "d2"} {
		f.answer("GET /v1/serverless/deployments/"+d, 200, map[string]interface{}{
			"id": d, "number": 1, "status": "LIVE",
			"steps": []map[string]interface{}{{"kind": "BUILD", "status": "SUCCEEDED"}, {"kind": "TRAFFIC", "status": "SUCCEEDED"}},
		})
	}
	if _, _, err := run(t, srv, t.TempDir(), "templates", "deploy", "laravel"); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("no name: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "templates", "deploy", "laravel", "--name", "loja", "--answer", "oops"); err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("bad answer: %v", err)
	}
	out, _, err = run(t, srv, t.TempDir(), "templates", "deploy", "laravel", "--name", "loja", "--answer", "ADMIN_EMAIL=ana@loja.com.br")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/templates/laravel/deploy").Body)
	if body["project_name"] != "loja" || body["answers"].(map[string]interface{})["ADMIN_EMAIL"] != "ana@loja.com.br" {
		t.Fatalf("sent %v", body)
	}
	for _, want := range []string{"database loja-db: being created", "bucket loja-files-ab12cd: ready", "Deployment #1 of web", "Deployment #1 of worker", "All live"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestTemplateEstimateSaysWhatSleeps(t *testing.T) {
	dbs, awake, none := "13940000000", "2555000000", "0"
	for _, c := range []struct {
		e            api.TemplateEstimate
		hasDatabases bool
		want         string
	}{
		{api.TemplateEstimate{DatabasesMonthlyMicroCents: &dbs, AlwaysOnMonthlyMicroCents: &awake, SomeSleep: true}, true, "(databases); the site sleeps without traffic and is billed by use"},
		{api.TemplateEstimate{DatabasesMonthlyMicroCents: &dbs, AlwaysOnMonthlyMicroCents: &none, Sleeps: true, SomeSleep: true}, true, "(databases); services sleep without traffic and are billed by use"},
		{api.TemplateEstimate{DatabasesMonthlyMicroCents: &none, AlwaysOnMonthlyMicroCents: &awake}, false, "(always-on); services billed by use"},
	} {
		if got := estimateText(c.e, c.hasDatabases); !strings.HasSuffix(got, c.want) {
			t.Fatalf("%q does not end in %q", got, c.want)
		}
	}
}

func TestProjectsDuplicateMakesAStagingWithItsOwnData(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": "0193f0e2-2222-7000-8000-000000000009", "name": "loja"}))
	f.answer("POST /v1/serverless/projects/0193f0e2-2222-7000-8000-000000000009/duplicate", 200, map[string]interface{}{
		"project_id": "p2", "services": []map[string]interface{}{{"id": "s9", "name": "web", "deployment_id": "d9"}},
		"databases": []string{"loja-staging-db"}, "buckets": []string{"loja-staging-files-abc123"},
	})
	if _, _, err := run(t, srv, t.TempDir(), "projects", "duplicate", "loja"); err == nil || !strings.Contains(err.Error(), "--name loja-staging") {
		t.Fatalf("no name: %v", err)
	}
	out, _, err := run(t, srv, t.TempDir(), "projects", "duplicate", "loja", "--name", "loja-staging")
	if err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/projects/0193f0e2-2222-7000-8000-000000000009/duplicate").Body); body["name"] != "loja-staging" {
		t.Fatalf("sent %v", body)
	}
	for _, want := range []string{"Project loja-staging made from loja", "database loja-staging-db: new and empty", "bucket loja-staging-files-abc123"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

// laravelProposal is what the API proposes for a Laravel repository.
func laravelProposal() map[string]interface{} {
	return map[string]interface{}{
		"readable": true, "framework": "laravel", "template_id": "laravel", "branch": nil,
		"services": []map[string]interface{}{
			{"name": "web", "kind": "WEB", "start_command": nil, "release_command": "php artisan migrate --force"},
			{"name": "worker", "kind": "WORKER", "start_command": "php artisan queue:work"},
			{"name": "scheduler", "kind": "CRON", "start_command": "php artisan schedule:run", "cron_schedule": "* * * * *"},
		},
		"databases": []map[string]interface{}{{"key": "db", "engine": "postgres", "size": "pg-1-1"}, {"key": "cache", "engine": "valkey", "size": "kv-1-1"}},
		"buckets":   []map[string]interface{}{{"key": "files", "public": true}},
		"variables": []map[string]interface{}{{"key": "APP_KEY", "value": nil, "generated": true}, {"key": "APP_ENV", "value": "production", "generated": false}},
		"to_fill":   []string{"MAIL_PASSWORD"},
		"estimate":  map[string]interface{}{"databases_monthly_micro_cents": "140000000", "always_on_monthly_micro_cents": "0", "sleeps": false, "some_sleep": true},
	}
}

func TestLaunchProposesTheWholeProjectAndPublishesWhatIsKept(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/git/connections", 200, []map[string]interface{}{
		{"id": "c1", "provider": "GITHUB", "account_login": "ana", "status": "ACTIVE"},
		{"id": "c2", "provider": "GITLAB", "account_login": "ana", "status": "NEEDS_REAUTH"},
	})
	f.answer("POST /v1/serverless/source-inspections", 200, laravelProposal())
	f.answer("POST /v1/serverless/projects/from-source", 200, map[string]interface{}{
		"project_id": "p1",
		"services":   []map[string]interface{}{{"id": "s1", "name": "web", "deployment_id": "d1"}, {"id": "s2", "name": "worker", "deployment_id": "d2"}},
		"databases":  []string{"loja-db", "loja-cache"}, "buckets": []string{"loja-files-ab12cd"},
	})
	for _, d := range []string{"d1", "d2"} {
		f.answer("GET /v1/serverless/deployments/"+d, 200, map[string]interface{}{
			"id": d, "number": 1, "status": "LIVE",
			"steps": []map[string]interface{}{{"kind": "BUILD", "status": "SUCCEEDED"}, {"kind": "TRAFFIC", "status": "SUCCEEDED"}},
		})
	}
	if _, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja", "--without", "web", "--yes"); err == nil || !strings.Contains(err.Error(), "always stays") {
		t.Fatalf("without web: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja", "--without", "redis", "--yes"); err == nil || !strings.Contains(err.Error(), `nothing called "redis"`) || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("unknown: %v", err)
	}
	out, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja", "--without", "scheduler", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	inspected := jsonOf(t, f.last("POST /v1/serverless/source-inspections").Body)["git"].(map[string]interface{})
	if inspected["provider"] != "GITHUB" || inspected["connection_id"] != "c1" || inspected["repo_full_name"] != "ana/loja" {
		t.Fatalf("inspected %v", inspected)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/projects/from-source").Body)
	keep, _ := json.Marshal(body["keep"])
	if body["project_name"] != "loja" || string(keep) != `{"buckets":["files"],"databases":["db","cache"],"services":["web","worker"]}` {
		t.Fatalf("sent %v %s", body, keep)
	}
	for _, want := range []string{
		"Laravel app found in ana/loja", "before each release: php artisan migrate --force",
		"PostgreSQL", "1 vCPU, 1 GB RAM", "Object Storage, public files", "Secrets generated:  APP_KEY",
		"Left out:", "scheduler", "from R$ 140,00 a month (databases)", "Deployment #1 of worker", "All live",
		"Still to fill in: MAIL_PASSWORD",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLaunchAsksFirstAndReadsAPublicAddressWhenNoAccountSeesIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/git/connections", 200, []map[string]interface{}{
		{"id": "c1", "provider": "GITHUB", "account_login": "ana", "status": "ACTIVE"},
	})
	f.on("POST /v1/serverless/source-inspections", func(w http.ResponseWriter, c call) {
		var b struct {
			Git map[string]interface{} `json:"git"`
		}
		_ = json.Unmarshal(c.Body, &b)
		if b.Git["provider"] == "GITHUB" {
			reply(w, 400, map[string]interface{}{"status": 400, "code": "invalid_request", "reason": "INVALID_SOURCE", "detail": "The repository was not found, or the connection cannot see it."})
			return
		}
		p := laravelProposal()
		p["branch"] = "master"
		reply(w, 200, p)
	})
	out, _, err := runWithInput(t, srv, t.TempDir(), "n\n", "launch", "https://github.com/laravel/laravel.git")
	if err != nil {
		t.Fatal(err)
	}
	git := jsonOf(t, f.last("POST /v1/serverless/source-inspections").Body)["git"].(map[string]interface{})
	if git["provider"] != "PUBLIC_URL" || git["repo_url"] != "https://github.com/laravel/laravel.git" {
		t.Fatalf("fell back to %v", git)
	}
	if !strings.Contains(out, "github.com/laravel/laravel (branch master)") || !strings.Contains(out, "every minute") ||
		!strings.Contains(out, `Publish it as the project "laravel"? [Y/n]`) || !strings.Contains(out, "Nothing was made.") {
		t.Fatalf("out:\n%s", out)
	}
	for _, c := range f.calls {
		if c.Path == "/v1/serverless/projects/from-source" {
			t.Fatal("published without a yes")
		}
	}
	if _, _, err := run(t, srv, t.TempDir(), "launch", "https://github.com/laravel/laravel"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("no answer: %v", err)
	}
	out, _, err = run(t, srv, t.TempDir(), "launch", "https://github.com/laravel/laravel", "--dry-run", "--json")
	if err != nil || !strings.Contains(out, `"framework": "laravel"`) {
		t.Fatalf("dry run: %v %s", err, out)
	}
}

func TestLaunchSaysWhichAccountToPickAndWhatCannotBeRead(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/git/connections", 200, []map[string]interface{}{})
	if _, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja"); err == nil || !strings.Contains(err.Error(), "Settings › Integrations") {
		t.Fatalf("none connected: %v", err)
	}
	f.answer("GET /v1/serverless/git/connections", 200, []map[string]interface{}{
		{"id": "c1", "provider": "GITHUB", "account_login": "ana", "status": "ACTIVE"},
		{"id": "c2", "provider": "BITBUCKET", "account_login": "loja", "status": "ACTIVE"},
	})
	if _, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja"); err == nil || !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "bitbucket loja") {
		t.Fatalf("two connected: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "launch", "loja"); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("bad name: %v", err)
	}
	f.answer("POST /v1/serverless/source-inspections", 200, map[string]interface{}{"readable": false, "services": []interface{}{}, "databases": []interface{}{}, "buckets": []interface{}{}, "variables": []interface{}{}, "to_fill": []interface{}{}})
	if _, _, err := run(t, srv, t.TempDir(), "launch", "https://git.example.com/ana/loja.git"); err == nil || !strings.Contains(err.Error(), "cannot be read from here") {
		t.Fatalf("unreadable: %v", err)
	}
	git := jsonOf(t, f.last("POST /v1/serverless/source-inspections").Body)["git"].(map[string]interface{})
	if git["provider"] != "PUBLIC_URL" {
		t.Fatalf("another host is public: %v", git)
	}
	f.answer("POST /v1/serverless/source-inspections", 200, laravelProposal())
	if _, _, err := run(t, srv, t.TempDir(), "launch", "ana/loja", "--provider", "bitbucket", "--dry-run"); err != nil {
		t.Fatalf("picked: %v", err)
	}
	if git := jsonOf(t, f.last("POST /v1/serverless/source-inspections").Body)["git"].(map[string]interface{}); git["connection_id"] != "c2" {
		t.Fatalf("picked %v", git)
	}
}

func TestServicesCreateBuildsFromARepository(t *testing.T) {
	f, srv := newFakeAPI(t)
	pid := "0193f0e2-2222-7000-8000-000000000009"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("GET /v1/serverless/git/connections", 200, []map[string]interface{}{
		{"id": "c1", "provider": "GITHUB", "account_login": "ana", "status": "ACTIVE"},
	})
	created := map[string]interface{}{"service": service("WEB", "GIT"), "deployment": map[string]interface{}{"id": "d1", "number": 1, "status": "BUILDING"}}
	f.on("POST /v1/serverless/projects/"+pid+"/services", func(w http.ResponseWriter, c call) {
		var b struct {
			Source struct {
				Git map[string]interface{} `json:"git"`
			} `json:"source"`
		}
		_ = json.Unmarshal(c.Body, &b)
		// The connected account cannot see someone else's repository.
		if b.Source.Git["repo_full_name"] == "laravel/laravel" {
			reply(w, 400, map[string]interface{}{"status": 400, "code": "invalid_request", "reason": "INVALID_SOURCE", "detail": "The repository was not found, or the connection cannot see it."})
			return
		}
		reply(w, 200, created)
	})
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "api", "--project", "loja"); err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("no source: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "api", "--project", "loja", "--repo", "ana/loja", "--image", "nginx:1"); err == nil || !strings.Contains(err.Error(), "one of them") {
		t.Fatalf("both: %v", err)
	}
	out, _, err := run(t, srv, t.TempDir(), "services", "create", "api", "--project", "loja", "--repo", "ana/loja", "--branch", "main")
	if err != nil {
		t.Fatal(err)
	}
	src := jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/services").Body)["source"].(map[string]interface{})
	git := src["git"].(map[string]interface{})
	if src["kind"] != "GIT" || git["provider"] != "GITHUB" || git["connection_id"] != "c1" || git["repo_full_name"] != "ana/loja" || git["branch"] != "main" {
		t.Fatalf("sent %v", src)
	}
	if !strings.Contains(out, "Deployment #1 started") {
		t.Fatalf("out: %s", out)
	}
	// Someone else's public repository: read as public when the account cannot see it.
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "site", "--project", "loja", "--repo", "https://github.com/laravel/laravel"); err != nil {
		t.Fatal(err)
	}
	src = jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/services").Body)["source"].(map[string]interface{})
	if git := src["git"].(map[string]interface{}); git["provider"] != "PUBLIC_URL" || git["repo_url"] != "https://github.com/laravel/laravel" {
		t.Fatalf("fell back to %v", git)
	}
}

func TestProjectsPauseAndResume(t *testing.T) {
	f, srv := newFakeAPI(t)
	pid := "0193f0e2-2222-7000-8000-000000000009"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("POST /v1/serverless/projects/"+pid+"/pause", 200, map[string]interface{}{"id": pid, "name": "loja", "paused": true})
	f.answer("POST /v1/serverless/projects/"+pid+"/resume", 200, map[string]interface{}{"id": pid, "name": "loja", "paused": false})
	out, _, err := run(t, srv, t.TempDir(), "projects", "pause", "loja")
	if err != nil || !strings.Contains(out, "loja paused") || !strings.Contains(out, "mw projects resume loja") {
		t.Fatalf("pause: %v %q", err, out)
	}
	out, _, err = run(t, srv, t.TempDir(), "projects", "resume", "loja")
	if err != nil || !strings.Contains(out, "loja resumed") {
		t.Fatalf("resume: %v %q", err, out)
	}
}

func TestGroupsShareVariablesAcrossServices(t *testing.T) {
	f, srv := newFakeAPI(t)
	pid := "0193f0e2-2222-7000-8000-000000000001"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	groups := []map[string]interface{}{{"id": "g1", "project_id": pid, "name": "shared", "service_ids": []string{svcID}, "variable_count": 3}}
	f.answer("GET /v1/serverless/projects/"+pid+"/env-groups", 200, groups)
	out, _, err := run(t, srv, t.TempDir(), "groups", "--project", "loja")
	if err != nil || !strings.Contains(out, "shared") {
		t.Fatalf("list: %v %q", err, out)
	}
	f.answer("PUT /v1/serverless/env-groups/g1/variables", 200, map[string]interface{}{"created": []string{"MAIL_HOST"}, "updated": []string{}, "removed": []string{}, "deployments": []map[string]interface{}{{"id": "d1", "number": 2}}})
	out, _, err = run(t, srv, t.TempDir(), "groups", "set", "shared", "MAIL_HOST=smtp.loja.com.br", "--project", "loja")
	if err != nil || !strings.Contains(out, "shared: 1 added") || !strings.Contains(out, "1 service reading it") {
		t.Fatalf("set: %v %q", err, out)
	}
	body := jsonOf(t, f.last("PUT /v1/serverless/env-groups/g1/variables").Body)
	if body["deploy"] != true || body["variables"].([]interface{})[0].(map[string]interface{})["key"] != "MAIL_HOST" {
		t.Fatalf("sent %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "groups", "set", "outro", "A=1", "--project", "loja"); err == nil || !strings.Contains(err.Error(), "there is shared") {
		t.Fatalf("unknown group: %v", err)
	}
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/projects/"+pid, 200, map[string]interface{}{"id": pid, "name": "loja"})
	f.answer("PUT /v1/serverless/services/"+svcID+"/env-groups", 200, map[string]interface{}{"deployment": map[string]interface{}{"id": "d2", "number": 3}})
	out, _, err = run(t, srv, t.TempDir(), "groups", "use", "shared", "--service", "web")
	if err != nil || !strings.Contains(out, "web reads 1 group") {
		t.Fatalf("use: %v %q", err, out)
	}
	if ids := jsonOf(t, f.last("PUT /v1/serverless/services/"+svcID+"/env-groups").Body)["env_group_ids"].([]interface{}); len(ids) != 1 || ids[0] != "g1" {
		t.Fatalf("sent ids %v", ids)
	}
}

func TestRegistryCredentialsAreWrittenFromStdinAndUsedByImages(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/serverless/registry-credentials", 200, map[string]interface{}{"id": "r1", "name": "ghcr.io", "registry_host": "ghcr.io", "username": "ana", "created_at": "2026-10-25T10:00:00Z"})
	if _, _, err := run(t, srv, t.TempDir(), "registry", "add", "ghcr.io", "--username", "ana"); err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("no stdin: %v", err)
	}
	out, _, err := runWithInput(t, srv, t.TempDir(), "ghp_secret\n", "registry", "add", "ghcr.io", "--username", "ana", "--password-stdin")
	if err != nil || !strings.Contains(out, "Credential ghcr.io saved") {
		t.Fatalf("add: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/registry-credentials").Body); body["password"] != "ghp_secret" || body["registry_host"] != "ghcr.io" {
		t.Fatalf("sent %v", body)
	}
	if strings.Contains(out, "ghp_secret") {
		t.Fatal("the secret was printed")
	}
	f.answer("GET /v1/serverless/registry-credentials", 200, []map[string]interface{}{{"id": "r1", "name": "ghcr.io", "registry_host": "ghcr.io", "username": "ana", "created_at": "2026-10-25T10:00:00Z"}})
	pid := "0193f0e2-2222-7000-8000-000000000009"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("POST /v1/serverless/projects/"+pid+"/services", 200, map[string]interface{}{"service": service("WEB", "IMAGE")})
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "api", "--project", "loja", "--image", "ghcr.io/ana/api:1.4", "--registry", "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	img := jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/services").Body)["source"].(map[string]interface{})["image"].(map[string]interface{})
	if img["registry_credential_id"] != "r1" || img["ref"] != "ghcr.io/ana/api:1.4" {
		t.Fatalf("sent image %v", img)
	}
	f.answer("DELETE /v1/serverless/registry-credentials/r1", 204, nil)
	if out, _, err := run(t, srv, t.TempDir(), "registry", "remove", "ghcr.io"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("remove: %v %q", err, out)
	}
}

func TestServicesHookShowsTheURLOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/deploy-hook", 200, map[string]interface{}{"url": "https://api.example.com/hooks/deploy/abc", "rotated_at": "2026-10-25T10:00:00Z"})
	out, _, err := run(t, srv, t.TempDir(), "services", "hook", "web")
	if err != nil || !strings.Contains(out, "https://api.example.com/hooks/deploy/abc") || !strings.Contains(out, "shown this once") {
		t.Fatalf("hook: %v %q", err, out)
	}
	f.answer("DELETE /v1/serverless/services/"+svcID+"/deploy-hook", 204, nil)
	if out, _, err := run(t, srv, t.TempDir(), "services", "hook", "web", "--disable"); err != nil || !strings.Contains(out, "disabled") {
		t.Fatalf("disable: %v %q", err, out)
	}
}

func TestAQuotaRefusalSaysWhatIsFreeAndWhereToAsk(t *testing.T) {
	msg := Describe(&api.Problem{Status: 400, Code: "invalid_request", Reason: "SERVERLESS_QUOTA_EXCEEDED", Detail: "This would reserve more vCPU than the account's quota allows.",
		Params: map[string]interface{}{"over": []interface{}{
			map[string]interface{}{"resource": "vcpu_millis", "limit": 4000.0, "used": 3500.0, "requested": 1500.0},
			map[string]interface{}{"resource": "memory_mib", "limit": 8192.0, "used": 7168.0, "requested": 3072.0},
		}}})
	for _, want := range []string{"vCPU: 0.5 free of 4; this needs 1.5", "memory: 1 GiB free of 8; this needs 3", "Account limits › Ask for more"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %s", want, msg)
		}
	}
}
