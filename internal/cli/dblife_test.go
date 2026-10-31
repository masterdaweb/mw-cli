package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRestoreMakesANewDatabaseToAnInstantAndWaitsForIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	made := database("provisioning")
	made["name"] = "loja-ontem"
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/restore", 200, map[string]interface{}{"database": made, "password": "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu"})
	var polls int32
	f.on("GET /v1/databases/loja-ontem", func(w http.ResponseWriter, _ call) {
		d := database("provisioning")
		d["name"] = "loja-ontem"
		if atomic.AddInt32(&polls, 1) > 1 {
			d["status"] = "running"
		}
		reply(w, 200, d)
	})
	out, _, err := run(t, srv, t.TempDir(), "db", "restore", "loja-prod", "--to", "loja-ontem", "--time", "2026-10-24T18:30:00-03:00")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases/loja-prod/restore").Body)
	if body["name"] != "loja-ontem" || body["target"] != "time" || body["time"] != "2026-10-24T21:30:00Z" || body["accept_terms"] != nil {
		t.Fatalf("body %v", body)
	}
	for _, want := range []string{"Restoring loja-prod into loja-ontem", "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu", "loja-ontem is restored"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q not in\n%s", want, out)
		}
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "restore", "loja-prod", "--to", "x", "--time", "yesterday"); err == nil || !strings.Contains(err.Error(), "RFC") && !strings.Contains(err.Error(), "instant") {
		t.Fatalf("a bad --time: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "restore", "loja-prod"); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("no --to: %v", err)
	}
}

func TestRestorePointsAreListedInWords(t *testing.T) {
	f, srv := newFakeAPI(t)
	n := 12
	f.answer("GET /v1/databases/loja-prod/restore-points", 200, []map[string]interface{}{
		{"name": "deploy-web-12", "reason": "release", "service_name": "web", "deployment_number": n, "created_at": "2026-10-24T18:00:00Z"},
		{"name": "before-import-abc", "reason": "import", "created_at": "2026-10-23T18:00:00Z"},
	})
	out, _, err := run(t, srv, t.TempDir(), "db", "restore-points", "loja-prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "before deployment #12 of web") || !strings.Contains(out, "before an import") {
		t.Fatalf("%s", out)
	}
}

func TestHibernateWaitsUntilAsleepAndSaysWhatItCosts(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/databases/loja-prod/hibernate", 200, database("hibernating"))
	var polls int32
	f.on("GET /v1/databases/loja-prod", func(w http.ResponseWriter, _ call) {
		d := database("hibernating")
		if atomic.AddInt32(&polls, 1) > 1 {
			since := "2026-10-24T18:00:00Z"
			monthly := "2430000"
			d["status"] = "hibernated"
			d["hibernation"] = map[string]interface{}{"since": since, "backup_bytes": "3000000000", "monthly_micro_cents": monthly, "waking": false}
		}
		reply(w, 200, d)
	})
	out, errOut, err := run(t, srv, t.TempDir(), "db", "hibernate", "loja-prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "loja-prod is asleep") || !strings.Contains(out, "(3.0 GB)") || !strings.Contains(out, "mw db wake loja-prod") {
		t.Fatalf("%s\n---\n%s", out, errOut)
	}
}

func TestDumpWaitsForTheDatabaseThenDownloadsIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/databases/loja-prod/exports", 200, map[string]interface{}{"id": "j1", "kind": "export", "status": "pending", "created_at": "2026-10-24T18:00:00Z"})
	var polls int32
	f.on("GET /v1/databases/loja-prod/jobs", func(w http.ResponseWriter, _ call) {
		status := "running"
		if atomic.AddInt32(&polls, 1) > 1 {
			status = "succeeded"
		}
		reply(w, 200, []map[string]interface{}{{"id": "j1", "kind": "export", "status": status, "size_bytes": "11", "created_at": "2026-10-24T18:00:00Z"}})
	})
	// The link leads to the API's own public route: no credential on it.
	dl := http.NewServeMux()
	dl.HandleFunc("/public/databases/exports/tok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the download carried a credential")
		}
		w.Header().Set("Content-Length", "11")
		_, _ = w.Write([]byte("PGDMP-dump!"))
	})
	dlSrv := httpServer(t, dl)
	f.answer("POST /v1/databases/loja-prod/exports/j1/link", 200, map[string]interface{}{
		"url": dlSrv + "/public/databases/exports/tok", "expires_at": "2026-10-24T18:15:00Z", "filename": "loja-prod-2026-10-24-1800.dump",
	})
	dir := t.TempDir()
	file := filepath.Join(dir, "backup.dump")
	out, _, err := run(t, srv, dir, "db", "dump", "loja-prod", file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "PGDMP-dump!" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := os.Stat(file + ".part"); !os.IsNotExist(err) {
		t.Fatal("the partial file was left behind")
	}
	if !strings.Contains(out, "Downloaded "+file+" (11 B)") {
		t.Fatalf("%s", out)
	}
}

func TestImportFromAURLFollowsTheJobAndReportsIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/import", 200, map[string]interface{}{"id": "j2", "kind": "import", "status": "pending", "source": "app@old.example.com:5432/loja", "created_at": "2026-10-24T18:00:00Z"})
	f.answer("GET /v1/databases/loja-prod/jobs", 200, []map[string]interface{}{{
		"id": "j2", "kind": "import", "status": "succeeded", "created_at": "2026-10-24T18:00:00Z",
		"report": map[string]interface{}{"tables": 14, "left_out": 1, "notes": []string{"function x() does not exist"}, "definers_removed": 0, "server_statements_removed": 0},
	}})
	out, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", "--from-url", "postgres://app:s3cr3t@old.example.com:5432/loja", "--replace")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/databases/loja-prod/import").Body)
	if body["source_url"] != "postgres://app:s3cr3t@old.example.com:5432/loja" || body["replace"] != true {
		t.Fatalf("body %v", body)
	}
	if strings.Contains(out, "s3cr3t") || !strings.Contains(out, "14 table(s)") || !strings.Contains(out, "function x() does not exist") {
		t.Fatalf("%s", out)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod"); err == nil {
		t.Fatal("an import with neither a file nor --from-url")
	}
}

func TestResizeInTheWindowAndUpdateTheWindow(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	scheduled := database("running")
	scheduled["maintenance"] = map[string]interface{}{"day": 0, "hour_utc": 6, "next_at": "2026-10-25T06:00:00Z", "scheduled_size": "pg-2-4"}
	f.answer("POST /v1/databases/loja-prod/resize", 200, scheduled)
	out, _, err := run(t, srv, t.TempDir(), "db", "resize", "loja-prod", "--size", "pg-2-4", "--in-window")
	if err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("POST /v1/databases/loja-prod/resize").Body); b["when"] != "maintenance" || b["size"] != "pg-2-4" {
		t.Fatalf("body %v", b)
	}
	if !strings.Contains(out, "scheduled for its maintenance window") {
		t.Fatalf("%s", out)
	}
	f.answer("PATCH /v1/databases/loja-prod", 200, database("running"))
	if _, _, err := run(t, srv, t.TempDir(), "db", "update", "loja-prod", "--maintenance-day", "sun", "--maintenance-hour", "3", "--disk-autogrow-max", "0"); err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("PATCH /v1/databases/loja-prod").Body)
	if b["maintenance_day"] != float64(0) || b["maintenance_hour_utc"] != float64(3) || b["disk_autogrow_max_gb"] != nil {
		t.Fatalf("body %v", b)
	}
	if _, ok := b["disk_autogrow_max_gb"]; !ok {
		t.Fatal("--disk-autogrow-max 0 must switch it off (null)")
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "update", "loja-prod", "--maintenance-day", "someday"); err == nil {
		t.Fatal("a day that is not one")
	}
}

// httpServer serves h on its own address (not the fake API's).
func httpServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
