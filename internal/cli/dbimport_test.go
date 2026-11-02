package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// uploadAPI answers a PostgreSQL database's file import: an upload of
// `parts` parts of `partBytes`, completed into job1, which succeeds with a
// report. Each part's handler is the test's.
func uploadAPI(t *testing.T, f *fakeAPI, parts int, partBytes int64, part func(n string, w http.ResponseWriter, c call)) {
	t.Helper()
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	numbers := []int{}
	for n := 1; n <= parts; n++ {
		numbers = append(numbers, n)
		n := strconv.Itoa(n)
		f.on("PUT /v1/databases/loja-prod/imports/uploads/job1/parts/"+n, func(w http.ResponseWriter, c call) { part(n, w, c) })
	}
	job := map[string]interface{}{"id": "job1", "kind": "import", "status": "uploading", "source": "loja.sql.gz",
		"size_bytes": "0", "database": "loja_prod", "replace": false, "created_at": "2026-11-02T18:00:00Z"}
	f.answer("POST /v1/databases/loja-prod/imports/uploads", 201, map[string]interface{}{"job": job,
		"upload": map[string]interface{}{"part_bytes": partBytes, "part_count": parts, "part_numbers": numbers, "uploaded_bytes": 0}})
	f.on("GET /v1/databases/loja-prod/jobs", func(w http.ResponseWriter, _ call) {
		if !f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
			reply(w, 200, []map[string]interface{}{})
			return
		}
		reply(w, 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "succeeded", "format": "sql.gz",
			"report": map[string]interface{}{"tables": 12, "left_out": 1, "notes": []string{"line 40: role \"app\" does not exist"},
				"definers_removed": 2, "server_statements_removed": 1, "other_database": "loja_antiga"}}})
	})
	f.answer("POST /v1/databases/loja-prod/imports/uploads/job1/complete", 202, map[string]interface{}{"id": "job1", "kind": "import", "status": "pending"})
}

func dumpFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A dump goes to the database's own API in parts, several at once; a part
// that did not arrive is sent again once the store says it lacks it; then
// the database imports it, and what it left out is said.
func TestADumpIsUploadedInPartsAndImportedByTheDatabase(t *testing.T) {
	f, srv := newFakeAPI(t)
	var mu sync.Mutex
	got := map[string]int{}
	inFlight, most := 0, 0
	failedOnce := false
	uploadAPI(t, f, 5, 4, func(n string, w http.ResponseWriter, c call) {
		mu.Lock()
		inFlight++
		if inFlight > most {
			most = inFlight
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		inFlight--
		if c.Header.Get("Authorization") != "Bearer mwk_test" || c.Header.Get("Content-Type") != "application/octet-stream" {
			w.WriteHeader(400)
			return
		}
		if n == "3" && !failedOnce {
			failedOnce = true
			w.WriteHeader(503)
			return
		}
		got[n] = len(c.Body)
		w.WriteHeader(200)
	})
	f.answer("POST /v1/databases/loja-prod/imports/uploads/job1/parts", 200, map[string]interface{}{"job": map[string]interface{}{"id": "job1"},
		"upload": map[string]interface{}{"part_bytes": 4, "part_count": 5, "part_numbers": []int{3}, "uploaded_bytes": 16}})

	out, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql.gz", 18))
	if err != nil {
		t.Fatal(err)
	}
	start := jsonOf(t, f.last("POST /v1/databases/loja-prod/imports/uploads").Body)
	if start["file_name"] != "loja.sql.gz" || start["size_bytes"] != float64(18) {
		t.Fatalf("started with %v", start)
	}
	if _, ok := start["database"]; ok {
		t.Fatalf("a dump named a database: %v", start)
	}
	if _, ok := start["replace"]; ok {
		t.Fatalf("replace without --replace: %v", start)
	}
	mu.Lock()
	if got["1"] != 4 || got["2"] != 4 || got["3"] != 4 || got["4"] != 4 || got["5"] != 2 || !failedOnce {
		t.Fatalf("the API got %v (part 3 failed once: %v)", got, failedOnce)
	}
	if most < 2 {
		t.Fatalf("one part at a time: at most %d in flight", most)
	}
	mu.Unlock()
	for _, want := range []string{"Imported loja.sql.gz into loja-prod: 12 table(s)", `role "app" does not exist`,
		"2 DEFINER clause(s)", "only the source server could run", "named its own database, loja_antiga"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

// What a database does not import is refused before a byte goes, saying
// what it does import.
func TestTheFileIsCheckedBeforeAByteGoes(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	mysql := database("running")
	mysql["name"], mysql["engine"] = "loja-wp", "mysql"
	f.answer("GET /v1/databases/loja-wp", 200, mysql)

	cases := []struct {
		db, file, want string
	}{
		{"loja-prod", dumpFile(t, "loja.zip", 10), ".sql or .sql.gz dump, or pg_dump's custom format"},
		{"loja-wp", dumpFile(t, "loja.dump", 10), ".sql or .sql.gz dump (mysqldump"},
		{"loja-prod", dumpFile(t, "loja.sql", 0), "is empty"},
		{"loja-prod", t.TempDir() + "/x.sql", "no such file"},
	}
	for _, c := range cases {
		if _, _, err := run(t, srv, t.TempDir(), "db", "import", c.db, c.file); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s into %s: %v, want %q", filepath.Base(c.file), c.db, err, c.want)
		}
	}
	dir := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dir); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("a directory: %v", err)
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads") || f.called("POST /v1/databases/loja-wp/imports/uploads") {
		t.Fatal("an upload began for a file the database does not import")
	}
}

// --replace (and --clean, the same for a dump) asks the database to drop
// what the dump creates first; the flags of other paths are refused.
func TestReplaceAndCleanAreTheSameForADump(t *testing.T) {
	for _, flag := range []string{"--replace", "--clean"} {
		f, srv := newFakeAPI(t)
		uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
		if _, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql.gz", 10), flag); err != nil {
			t.Fatal(err)
		}
		if body := jsonOf(t, f.last("POST /v1/databases/loja-prod/imports/uploads").Body); body["replace"] != true {
			t.Fatalf("%s sent %v", flag, body)
		}
	}
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("GET /v1/databases/erp", 200, sqlServerDB())
	refused := []struct {
		args []string
		want string
	}{
		{[]string{"loja-prod", dumpFile(t, "loja.sql", 5), "--database", "outra"}, "--database is for SQL Server"},
		{[]string{"erp", dumpFile(t, "erp.bak", 5), "--clean"}, "--clean is for dumps"},
		{[]string{"erp", dumpFile(t, "erp.bak", 5), "--local"}, "restored by the database itself"},
		{[]string{"loja-prod", dumpFile(t, "loja.sql", 5), "--local", "--replace"}, "--local runs your own client"},
		{[]string{"loja-prod", "--cancel", "--replace"}, "--cancel drops the upload left open"},
	}
	for _, r := range refused {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"db", "import"}, r.args...)...); err == nil || !strings.Contains(err.Error(), r.want) {
			t.Errorf("%v: %v, want %q", r.args, err, r.want)
		}
	}
}

// A refusal to start says what the person can do: backups off, or a server
// not yet updated to take files.
func TestARefusalToStartSaysWhatToDo(t *testing.T) {
	for reason, want := range map[string][]string{
		"backups_off":  {"backups off", "mw db update loja-prod --backups", "--local"},
		"agent_update": {"maintenance window", "--local", "--from-url"},
	} {
		f, srv := newFakeAPI(t)
		f.answer("GET /v1/databases/loja-prod", 200, database("running"))
		f.answer("GET /v1/databases/loja-prod/jobs", 200, []map[string]interface{}{})
		f.answer("POST /v1/databases/loja-prod/imports/uploads", 409, map[string]interface{}{"status": 409, "code": "conflict",
			"reason": "DATABASE_JOB_UNAVAILABLE", "detail": "A file is uploaded where the backups are.", "params": map[string]interface{}{"reason": reason}})
		_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 5))
		for _, w := range want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %v, want %q", reason, err, w)
			}
		}
	}
}

// A refusal sending again cannot change (the upload given up, or expired)
// stops at once, and says how to carry on.
func TestAPermanentRefusalStopsAtOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	var mu sync.Mutex
	puts := 0
	uploadAPI(t, f, 3, 4, func(_ string, w http.ResponseWriter, _ call) {
		mu.Lock()
		puts++
		mu.Unlock()
		reply(w, 409, map[string]interface{}{"status": 409, "code": "conflict", "reason": "DATABASE_IMPORT_UPLOAD", "detail": "This upload was given up."})
	})
	_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql.gz", 10))
	if err == nil || !strings.Contains(err.Error(), "given up") || !strings.Contains(err.Error(), "--cancel") {
		t.Fatalf("%v", err)
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads/job1/parts") || f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
		t.Fatal("asked again after a refusal that cannot change")
	}
	mu.Lock()
	defer mu.Unlock()
	if puts > uploadParallel {
		t.Fatalf("%d parts were sent after the first refusal", puts)
	}
}

// A part on which nothing moves is cut and sent again: a stalled connection
// does not hold the import forever.
func TestAStalledPartIsCutAndSentAgain(t *testing.T) {
	old := partStall
	partStall = 150 * time.Millisecond
	defer func() { partStall = old }()
	f, srv := newFakeAPI(t)
	release := make(chan struct{})
	defer close(release)
	var mu sync.Mutex
	tries := 0
	uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) {
		mu.Lock()
		tries++
		first := tries == 1
		mu.Unlock()
		if first {
			// Holds the answer: the client sees nothing move.
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
			return
		}
		w.WriteHeader(200)
	})
	f.answer("POST /v1/databases/loja-prod/imports/uploads/job1/parts", 200, map[string]interface{}{"job": map[string]interface{}{"id": "job1"},
		"upload": map[string]interface{}{"part_bytes": 16, "part_count": 1, "part_numbers": []int{1}, "uploaded_bytes": 0}})
	begun := time.Now()
	if _, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10)); err != nil {
		t.Fatal(err)
	}
	if time.Since(begun) > 5*time.Second {
		t.Fatalf("waited %s on a stalled part", time.Since(begun))
	}
	mu.Lock()
	defer mu.Unlock()
	if tries != 2 {
		t.Fatalf("the part was sent %d time(s)", tries)
	}
}

// --detach returns once the file is up; with -o json, standard output is the
// job alone (the progress goes to standard error).
func TestDetachAndJSON(t *testing.T) {
	f, srv := newFakeAPI(t)
	uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
	out, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10), "--detach")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`mw db jobs loja-prod` follows it") {
		t.Fatalf("%q", out)
	}

	f, srv = newFakeAPI(t)
	uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
	out, _, err = run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10), "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]interface{}
	if err := json.Unmarshal([]byte(out), &job); err != nil || job["id"] != "job1" || job["status"] != "succeeded" {
		t.Fatalf("standard output was not the job alone: %v %q", err, out)
	}
}

// An upload of the same dump left open carries on with what the store lacks;
// --cancel drops one, whatever the engine.
func TestADumpUploadCarriesOnAndCancels(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	var mu sync.Mutex
	sent := []string{}
	for _, n := range []string{"1", "2"} {
		n := n
		f.on("PUT /v1/databases/loja-prod/imports/uploads/job1/parts/"+n, func(w http.ResponseWriter, _ call) {
			mu.Lock()
			sent = append(sent, n)
			mu.Unlock()
			w.WriteHeader(200)
		})
	}
	open := map[string]interface{}{"id": "job1", "kind": "import", "status": "uploading", "source": "loja.sql",
		"size_bytes": "10", "database": "loja_prod", "replace": false, "created_at": "2026-11-02T18:00:00Z"}
	f.on("GET /v1/databases/loja-prod/jobs", func(w http.ResponseWriter, _ call) {
		if f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
			reply(w, 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "succeeded"}})
			return
		}
		reply(w, 200, []map[string]interface{}{open})
	})
	f.answer("POST /v1/databases/loja-prod/imports/uploads/job1/parts", 200, map[string]interface{}{"job": open,
		"upload": map[string]interface{}{"part_bytes": 6, "part_count": 2, "part_numbers": []int{2}, "uploaded_bytes": 6}})
	f.answer("POST /v1/databases/loja-prod/imports/uploads/job1/complete", 202, map[string]interface{}{"id": "job1", "kind": "import", "status": "pending"})
	f.answer("DELETE /v1/databases/loja-prod/imports/uploads/job1", 200, map[string]interface{}{"id": "job1", "status": "failed"})

	out, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10))
	if err != nil {
		t.Fatal(err)
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads") {
		t.Fatal("started a second upload")
	}
	mu.Lock()
	if strings.Join(sent, ",") != "2" {
		t.Fatalf("sent parts %v, not just the missing one", sent)
	}
	mu.Unlock()
	if !strings.Contains(out, "Carrying on with the upload of loja.sql") {
		t.Fatalf("%q", out)
	}

	f, srv = newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("GET /v1/databases/loja-prod/jobs", 200, []map[string]interface{}{open})
	f.answer("DELETE /v1/databases/loja-prod/imports/uploads/job1", 200, map[string]interface{}{"id": "job1", "status": "failed"})
	out, _, err = run(t, srv, t.TempDir(), "db", "import", "loja-prod", "--cancel")
	if err != nil || !f.called("DELETE /v1/databases/loja-prod/imports/uploads/job1") || !strings.Contains(out, "Dropped the upload of loja.sql") {
		t.Fatalf("did not drop it: %v %q", err, out)
	}
}

// A different file, or the same file with other options, does not take over
// the upload left open: the person is told which one is open and how to drop it.
func TestAnotherUploadOpenIsNamed(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("GET /v1/databases/loja-prod/jobs", 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "uploading",
		"source": "antigo.sql", "size_bytes": "99", "database": "loja_prod", "replace": false, "created_at": "2026-11-02T18:00:00Z"}})
	_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10))
	if err == nil || !strings.Contains(err.Error(), "antigo.sql") || !strings.Contains(err.Error(), "--cancel") {
		t.Fatalf("%v", err)
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads") || f.called("POST /v1/databases/loja-prod/imports/uploads/job1/parts") {
		t.Fatal("touched the upload left open")
	}
}

// --local is the person's own client: nothing goes through the database's
// upload, and a missing client says so.
func TestLocalUsesYourOwnClient(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	t.Setenv("PATH", t.TempDir())
	_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10), "--local")
	if err == nil || !strings.Contains(err.Error(), "psql") {
		t.Fatalf("%v", err)
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads") || f.called("GET /v1/databases/loja-prod/jobs") {
		t.Fatal("--local went through the database's upload")
	}
}

// Following the import rides out the API going away a moment (a deploy, a
// restart): a failed poll is tried again, and the import's end is reported.
func TestFollowingAnImportRidesOutABlip(t *testing.T) {
	f, srv := newFakeAPI(t)
	uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
	var mu sync.Mutex
	polls := 0
	f.on("GET /v1/databases/loja-prod/jobs", func(w http.ResponseWriter, _ call) {
		if !f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
			reply(w, 200, []map[string]interface{}{})
			return
		}
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		if n <= 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		reply(w, 200, []map[string]interface{}{{"id": "job1", "kind": "import", "status": "succeeded",
			"report": map[string]interface{}{"tables": 3}}})
	})
	out, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Imported loja.sql into loja-prod: 3 table(s)") {
		t.Fatalf("%q", out)
	}
}

// A refusal no retry can change ends the wait at once, saying the import
// carries on and how to follow it.
func TestFollowingAnImportStopsOnARefusal(t *testing.T) {
	f, srv := newFakeAPI(t)
	uploadAPI(t, f, 1, 16, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
	f.on("GET /v1/databases/loja-prod/jobs", func(w http.ResponseWriter, _ call) {
		if !f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
			reply(w, 200, []map[string]interface{}{})
			return
		}
		reply(w, 403, map[string]interface{}{"status": 403, "code": "forbidden", "detail": "This token cannot read databases."})
	})
	_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10))
	if err == nil || !strings.Contains(err.Error(), "the job carries on") || !strings.Contains(err.Error(), "mw db jobs loja-prod") {
		t.Fatalf("%v", err)
	}
}

func rawFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gzipOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// What the database's server would refuse by the file's bytes is refused
// here before a byte is uploaded, saying what to do: the server says so only
// once the whole file is up.
func TestAFileIsReadBeforeItIsSent(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	mysql := database("running")
	mysql["name"], mysql["engine"] = "loja-wp", "mysql"
	f.answer("GET /v1/databases/loja-wp", 200, mysql)
	utf16 := []byte{0xff, 0xfe}
	for _, c := range []byte("-- MySQL dump 10.13\nCREATE TABLE t (a int);\n") {
		utf16 = append(utf16, c, 0)
	}
	bare16 := utf16[2:]
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 9000)...)
	cases := []struct {
		db, name string
		body     []byte
		want     string
	}{
		{"loja-wp", "loja.sql", utf16, "is UTF-16 text (Windows PowerShell's >"},
		{"loja-wp", "loja.sql", bare16, "is UTF-16 text"},
		{"loja-prod", "loja.sql.gz", []byte("\xfd7zXZ\x00\x00\x04 xz"), "compressed with xz, and only gzip is read"},
		{"loja-prod", "loja.sql", png, "is not a dump"},
		{"loja-prod", "loja.sql.gz", gzipOf(t, png), "is not a dump"},
		{"loja-prod", "loja.sql.gz", gzipOf(t, gzipOf(t, []byte("SELECT 1;"))), "gzipped twice"},
		{"loja-prod", "loja.sql.gz", []byte("\x1f\x8bnot gzip at all"), "not readable as gzip"},
		{"loja-wp", "loja.sql", []byte("PGDMP\x01\x10\x00 an archive"), "imports into a PostgreSQL database"},
		{"loja-prod", "erp.dump", append([]byte("TAPE\x00\x00\x00\x00"), make([]byte, 600)...), "is a SQL Server backup"},
	}
	for _, c := range cases {
		_, _, err := run(t, srv, t.TempDir(), "db", "import", c.db, rawFile(t, c.name, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s into %s: %v, want %q", c.name, c.db, err, c.want)
		}
	}
	if f.called("POST /v1/databases/loja-prod/imports/uploads") || f.called("POST /v1/databases/loja-wp/imports/uploads") {
		t.Fatal("an upload began for a file the server would refuse")
	}
}

// What the server reads is sent, whatever the name says: a .dump holding
// plain SQL, pg_dump's archive under .sql, a gzipped archive, latin1 text.
func TestWhatTheServerReadsIsSent(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"loja.dump", []byte("-- PostgreSQL database dump\nCREATE TABLE t (a int);\n")},
		{"loja.sql", []byte("PGDMP\x01\x10\x00 an archive")},
		{"loja.sql.gz", gzipOf(t, []byte("PGDMP\x01\x10\x00 an archive"))},
		{"loja.sql", []byte("INSERT INTO t VALUES ('S\xe3o Paulo', '\x89PNG\\0\xff');\n")},
	} {
		f, srv := newFakeAPI(t)
		uploadAPI(t, f, 1, 16<<20, func(_ string, w http.ResponseWriter, _ call) { w.WriteHeader(200) })
		if _, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", rawFile(t, c.name, c.body)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !f.called("POST /v1/databases/loja-prod/imports/uploads/job1/complete") {
			t.Errorf("%s was not imported", c.name)
		}
	}
}

// A database the server last counted tables in is refused before the
// upload, and told how to go on.
func TestADatabaseThatHoldsTablesSaysReplace(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("GET /v1/databases/loja-prod/jobs", 200, []map[string]interface{}{})
	f.answer("POST /v1/databases/loja-prod/imports/uploads", 409, map[string]interface{}{"status": 409, "code": "conflict",
		"reason": "DATABASE_JOB_UNAVAILABLE", "detail": "The database already holds 4 table(s).", "params": map[string]interface{}{"reason": "not_empty", "tables": 4}})
	_, _, err := run(t, srv, t.TempDir(), "db", "import", "loja-prod", dumpFile(t, "loja.sql", 10))
	if err == nil || !strings.Contains(err.Error(), "loja-prod already holds 4 table(s): --replace") {
		t.Fatalf("%v", err)
	}
}

// The command's arguments say what is missing or too much.
func TestImportArgumentsSayWhatToDo(t *testing.T) {
	_, srv := newFakeAPI(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"loja-prod", "loja.sql", "--from-url", "postgres://x"}, "leave the file out"},
		{[]string{"loja-prod", "loja.sql", "--cancel"}, "name the database alone"},
		{[]string{"loja-prod"}, "which file?"},
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"db", "import"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}
