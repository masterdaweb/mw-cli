package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/masterdaweb/mw-cli/internal/api"
)

const backupID = "0193f0e2-5555-7000-8000-000000000001"

// withVolume is service() with a 10 GB volume at /data, a fifth of it used.
func withVolume(kind string) map[string]interface{} {
	s := service(kind, "IMAGE")
	s["settings"] = map[string]interface{}{"vcpu_millis": 500, "memory_mib": 1024, "min_instances": 0, "max_instances": 1,
		"volume_mount_path": "/data", "volume_size_gb": 10}
	s["volume_usage"] = map[string]interface{}{"used_bytes": "2100000000", "capacity_bytes": "10000000000", "measured_at": "2026-10-28T15:30:00Z"}
	return s
}

func volumeBackup(id, kind, status string, size, expires interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "service_id": svcID, "service_name": "web", "kind": kind, "status": status, "mount_path": "/data",
		"volume_size_gb": 10, "size_bytes": size, "requested_by": "ci", "created_at": "2026-10-28T06:10:00Z",
		"completed_at": nil, "expires_at": expires, "restorable": status == "COMPLETED",
	}
}

// count is how many times a route was called.
func (f *fakeAPI) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method+" "+c.Path == route {
			n++
		}
	}
	return n
}

func TestVolumeAddGrowAndRemoveSendTheSettings(t *testing.T) {
	f, srv := newFakeAPI(t)
	const settings = "PATCH /v1/serverless/services/" + svcID + "/settings"
	web := service("WEB", "GIT")
	web["settings"] = map[string]interface{}{"min_instances": 2, "max_instances": 5, "rollout_percent": 10}
	f.answer("GET /v1/serverless/services", 200, page(web))
	f.answer(settings, 200, map[string]interface{}{"service": withVolume("WEB"), "deployment": deployment("d8", 8, "QUEUED")})
	out, _, err := run(t, srv, t.TempDir(), "volume", "add", "--service", "web", "--path", "/data", "--size", "5")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last(settings).Body)
	want := map[string]interface{}{"volume_mount_path": "/data", "volume_size_gb": float64(5),
		"max_instances": float64(1), "min_instances": float64(1), "rollout_percent": nil}
	if body["deploy"] != true || !reflect.DeepEqual(body["settings"], want) {
		t.Fatalf("add sent %v", body)
	}
	if !strings.Contains(out, "web gets a 5 GB volume at /data with deployment #8") || !strings.Contains(out, "runs one instance") {
		t.Fatalf("add said %q", out)
	}

	// A worker runs one instance; a cron job is refused before anything is sent.
	worker := service("WORKER", "GIT")
	worker["settings"] = map[string]interface{}{"instances": 3}
	f.answer("GET /v1/serverless/services", 200, page(worker))
	if _, _, err := run(t, srv, t.TempDir(), "volume", "add", "--service", "web", "--path", "/srv/data", "--size", "2"); err != nil {
		t.Fatal(err)
	}
	want = map[string]interface{}{"volume_mount_path": "/srv/data", "volume_size_gb": float64(2), "instances": float64(1)}
	if body := jsonOf(t, f.last(settings).Body); !reflect.DeepEqual(body["settings"], want) {
		t.Fatalf("worker sent %v", body)
	}
	f.answer("GET /v1/serverless/services", 200, page(service("CRON", "GIT")))
	sent := f.count(settings)
	if _, _, err := run(t, srv, t.TempDir(), "volume", "add", "--service", "web", "--path", "/data", "--size", "5"); err == nil || !strings.Contains(err.Error(), "cron job") {
		t.Fatalf("cron: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "volume", "add", "--service", "web", "--size", "5"); err == nil || !strings.Contains(err.Error(), "--path") {
		t.Fatalf("no path: %v", err)
	}
	if f.count(settings) != sent {
		t.Fatal("a refused add was sent")
	}

	// Grow: applied at once, nothing deployed; it never shrinks.
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	f.answer(settings, 200, map[string]interface{}{"service": withVolume("WEB"), "deployment": nil})
	out, _, err = run(t, srv, t.TempDir(), "volume", "grow", "--service", "web", "--size", "20")
	if err != nil || !strings.Contains(out, "web's volume has 20 GB now: applied at once, without a restart.") {
		t.Fatalf("grow: %v %q", err, out)
	}
	body = jsonOf(t, f.last(settings).Body)
	if body["deploy"] != false || !reflect.DeepEqual(body["settings"], map[string]interface{}{"volume_size_gb": float64(20)}) {
		t.Fatalf("grow sent %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "volume", "grow", "--service", "web", "--size", "10"); err == nil || !strings.Contains(err.Error(), "never shrinks") {
		t.Fatalf("shrink: %v", err)
	}

	// Remove: the name typed back, then the volume and its data go.
	sent = f.count(settings)
	for _, confirm := range []string{"", "Web"} {
		_, _, err := run(t, srv, t.TempDir(), "volume", "remove", "--service", "web", "--confirm", confirm)
		if err == nil || !strings.Contains(err.Error(), "--confirm web") || !strings.Contains(err.Error(), "7 days") {
			t.Fatalf("remove confirmed with %q: %v", confirm, err)
		}
	}
	if f.count(settings) != sent {
		t.Fatal("an unconfirmed remove was sent")
	}
	f.answer(settings, 200, map[string]interface{}{"service": service("WEB", "IMAGE"), "deployment": deployment("d9", 9, "QUEUED")})
	out, _, err = run(t, srv, t.TempDir(), "volume", "remove", "--service", "web", "--confirm", "web")
	if err != nil || !strings.Contains(out, "web is deployed without its volume (deployment #9)") || !strings.Contains(out, "kept 7 days") {
		t.Fatalf("remove: %v %q", err, out)
	}
	body = jsonOf(t, f.last(settings).Body)
	if body["deploy"] != true || body["delete_volume"] != true ||
		!reflect.DeepEqual(body["settings"], map[string]interface{}{"volume_mount_path": nil, "volume_size_gb": nil}) {
		t.Fatalf("remove sent %v", body)
	}
}

func TestVolumeShowSaysHowFullItIsAndWhatTheImageKeepsOutsideIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	s := withVolume("WEB")
	s["image_volumes"] = []string{"/data/uploads", "/var/lib/ghost/content/"}
	f.answer("GET /v1/serverless/services", 200, page(s))
	for _, args := range [][]string{{"volume"}, {"volume", "show"}} {
		out, _, err := run(t, srv, t.TempDir(), append(args, "--service", "web")...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"Mounted at  /data", "Size        10 GB",
			"2.1 GB used of 10 GB (21%), measured " + when("2026-10-28T15:30:00Z"),
			"The image keeps data in /var/lib/ghost/content, outside the volume at /data",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v: %q not in\n%s", args, want, out)
			}
		}
		if strings.Contains(out, "/data/uploads") {
			t.Fatalf("a path inside the volume was warned of:\n%s", out)
		}
	}

	// No volume: where the image keeps data, and the command that keeps it.
	bare := service("WEB", "IMAGE")
	bare["image_volumes"] = []string{"/var/lib/ghost/content"}
	f.answer("GET /v1/serverless/services", 200, page(bare))
	out, _, err := run(t, srv, t.TempDir(), "volume", "--service", "web")
	if err != nil || !strings.Contains(out, "without a volume there it is lost on every restart. Add one: `mw volume add --service web --path /var/lib/ghost/content --size 5`.") {
		t.Fatalf("no volume: %v\n%s", err, out)
	}
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	if out, _, err := run(t, srv, t.TempDir(), "volume", "--service", "web"); err != nil || !strings.Contains(out, "web has no volume") {
		t.Fatalf("none at all: %v %q", err, out)
	}
	// A cron job cannot have one: nothing proposes it.
	cron := service("CRON", "IMAGE")
	cron["image_volumes"] = []string{"/var/lib/ghost/content"}
	f.answer("GET /v1/serverless/services", 200, page(cron))
	if out, _, err := run(t, srv, t.TempDir(), "volume", "--service", "web"); err != nil || !strings.Contains(out, "cron job") || strings.Contains(out, "mw volume add") {
		t.Fatalf("cron: %v %q", err, out)
	}
}

func TestVolumeBackupsListThemAndThoseOfVolumesThatWent(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	const daily = "0193f0e2-5555-7000-8000-000000000002"
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	f.answer("GET /v1/serverless/services/"+svcID+"/volume/backups", 200, []interface{}{
		volumeBackup(backupID, "MANUAL", "COMPLETED", "1200000000", nil),
		volumeBackup(daily, "DAILY", "RUNNING", nil, "2026-11-04T06:10:00Z"),
	})
	out, _, err := run(t, srv, t.TempDir(), "volume", "backups", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !matches(`^WHEN +KIND +STATUS +DATA +KEPT UNTIL +ID$`, lines[0]) ||
		!matches(`manual +completed +1\.2 GB +you delete it +`+backupID+`$`, lines[1]) ||
		!matches(`daily +running +— +`+when("2026-11-04T06:10:00Z")+` +`+daily+`$`, lines[2]) {
		t.Fatalf("table:\n%s", out)
	}
	if out, _, err := run(t, srv, t.TempDir(), "volume", "backups", "--service", "web", "-q"); err != nil || out != backupID+"\n"+daily+"\n" {
		t.Fatalf("-q: %v %q", err, out)
	}

	// --deleted: the project's, named by --project alone.
	gone := volumeBackup(backupID, "FINAL", "COMPLETED", "300000000", "2026-11-04T10:00:00Z")
	gone["service_name"], gone["mount_path"] = "api", "/srv/storage"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("GET /v1/serverless/projects/"+pid+"/volume-backups", 200, []interface{}{gone})
	out, _, err = run(t, srv, t.TempDir(), "volume", "backups", "--deleted", "--project", "loja")
	if err != nil || !strings.Contains(out, "SERVICE") || !matches(`api \(/srv/storage\) +final +completed +300 MB`, out) {
		t.Fatalf("--deleted: %v\n%s", err, out)
	}
	f.answer("GET /v1/serverless/projects/"+pid+"/volume-backups", 200, []interface{}{})
	if out, _, err := run(t, srv, t.TempDir(), "volume", "backups", "--deleted", "--project", "loja"); err != nil || !strings.Contains(out, "No backups of volumes that went in loja.") {
		t.Fatalf("none gone: %v %q", err, out)
	}

	// Deleting one asks first.
	if _, _, err := run(t, srv, t.TempDir(), "volume", "backups", "delete", backupID); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete without asking: %v", err)
	}
	f.answer("DELETE /v1/serverless/volume-backups/"+backupID, 200, volumeBackup(backupID, "MANUAL", "COMPLETED", "1200000000", nil))
	if out, _, err := run(t, srv, t.TempDir(), "volume", "backups", "delete", backupID, "--yes"); err != nil || !strings.Contains(out, "of web's volume (manual,") {
		t.Fatalf("delete: %v %q", err, out)
	}
	msg := Describe(&api.Problem{Status: 409, Reason: "SERVERLESS_VOLUME_BACKUP_LIMIT", Detail: "A volume keeps 10 manual backups at most: delete one first."})
	if !strings.Contains(msg, "`mw volume backups delete <id>`") {
		t.Fatalf("limit: %s", msg)
	}
}

func TestVolumeBackupWaitsUntilItIsDone(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	f.answer("POST /v1/serverless/services/"+svcID+"/volume/backups", 202, volumeBackup(backupID, "MANUAL", "PENDING", nil, nil))
	var polls int32
	f.on("GET /v1/serverless/services/"+svcID+"/volume/backups", func(w http.ResponseWriter, _ call) {
		if atomic.AddInt32(&polls, 1) == 1 {
			reply(w, 200, []interface{}{volumeBackup(backupID, "MANUAL", "RUNNING", nil, nil)})
			return
		}
		reply(w, 200, []interface{}{volumeBackup(backupID, "MANUAL", "COMPLETED", "1200000000", nil)})
	})
	out, _, err := run(t, srv, t.TempDir(), "volume", "backup", "--service", "web", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  pending\n  running\n  completed\n") || !strings.Contains(out, "Backup "+backupID+" of web's volume done (1.2 GB), kept until you delete it.") {
		t.Fatalf("said %q", out)
	}
	if f.last("POST /v1/serverless/services/"+svcID+"/volume/backups").Header.Get("Idempotency-Key") == "" {
		t.Fatal("a backup without an Idempotency-Key")
	}
	f.answer("GET /v1/serverless/services/"+svcID+"/volume/backups", 200, []interface{}{volumeBackup(backupID, "MANUAL", "FAILED", nil, nil)})
	if _, _, err := run(t, srv, t.TempDir(), "volume", "backup", "--service", "web", "--wait"); err == nil || !strings.Contains(err.Error(), "the backup of web's volume failed") {
		t.Fatalf("failed: %v", err)
	}
	// Without --wait: asked, and said where it shows.
	if out, _, err := run(t, srv, t.TempDir(), "volume", "backup", "--service", "web"); err != nil || !strings.Contains(out, "`mw volume backups` shows it") {
		t.Fatalf("no wait: %v %q", err, out)
	}
}

func TestVolumeRestoreNeedsTheNameThenFollowsTheDeployment(t *testing.T) {
	f, srv := newFakeAPI(t)
	const restore = "POST /v1/serverless/services/" + svcID + "/volume/restore"
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	for _, confirm := range []string{"", "loja"} {
		_, _, err := run(t, srv, t.TempDir(), "volume", "restore", backupID, "--service", "web", "--confirm", confirm)
		if err == nil || !strings.Contains(err.Error(), "replaces the data on web's volume") || !strings.Contains(err.Error(), "--confirm web") {
			t.Fatalf("confirmed with %q: %v", confirm, err)
		}
	}
	if _, _, err := run(t, srv, t.TempDir(), "volume", "restore", "yesterday", "--service", "web", "--confirm", "web"); err == nil || !strings.Contains(err.Error(), "backup's id") {
		t.Fatalf("not an id: %v", err)
	}
	if f.called(restore) {
		t.Fatal("a restore was sent unconfirmed")
	}
	f.answer(restore, 202, deployment("d10", 10, "QUEUED"))
	live := deployment("d10", 10, "LIVE")
	live["trigger"], live["steps"] = "RESTORE", []interface{}{map[string]interface{}{"kind": "TRAFFIC", "status": "SUCCEEDED"}}
	f.answer("GET /v1/serverless/deployments/d10", 200, live)
	out, _, err := run(t, srv, t.TempDir(), "volume", "restore", backupID, "--service", "web", "--confirm", "web")
	if err != nil {
		t.Fatal(err)
	}
	c := f.last(restore)
	if body := jsonOf(t, c.Body); body["backup_id"] != backupID || body["confirm_name"] != "web" || c.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("sent %v %v", body, c.Header)
	}
	if !strings.Contains(out, "Restoring backup "+backupID+" into web's volume (deployment #10):") || !strings.Contains(out, "Deployment #10 is live.") {
		t.Fatalf("said %q", out)
	}
	polled := f.count("GET /v1/serverless/deployments/d10")
	out, _, err = run(t, srv, t.TempDir(), "volume", "restore", backupID, "--service", "web", "--confirm", "web", "--detach")
	if err != nil || !strings.Contains(out, "with deployment #10 (`mw deployments` follows it)") || f.count("GET /v1/serverless/deployments/d10") != polled {
		t.Fatalf("--detach: %v %q", err, out)
	}
}

func TestVolumeLsListsFoldersFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	at := "2026-10-27T12:00:00Z"
	f.answer("GET /v1/serverless/services/"+svcID+"/volume/files", 200, map[string]interface{}{
		"path": "uploads", "truncated": true,
		"entries": []interface{}{
			map[string]interface{}{"name": "b.jpg", "type": "file", "size_bytes": 2100000, "modified_at": at},
			map[string]interface{}{"name": "zz", "type": "dir", "size_bytes": 4096, "modified_at": at},
			map[string]interface{}{"name": "a.txt", "type": "file", "size_bytes": 12, "modified_at": at},
			map[string]interface{}{"name": "2026", "type": "dir", "size_bytes": 4096, "modified_at": at},
			map[string]interface{}{"name": "latest", "type": "link", "size_bytes": 5, "modified_at": at},
		},
	})
	out, _, err := run(t, srv, t.TempDir(), "volume", "ls", "uploads", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	if q := f.last("GET /v1/serverless/services/" + svcID + "/volume/files").Query["path"]; len(q) != 1 || q[0] != "uploads" {
		t.Fatalf("asked for %v", q)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:6] {
		names = append(names, strings.Fields(line)[0])
	}
	if strings.Join(names, " ") != "2026/ zz/ a.txt b.jpg latest@" {
		t.Fatalf("order %v in\n%s", names, out)
	}
	if !matches(`2026/ +— +`+when(at), out) || !matches(`b\.jpg +2\.1 MB`, out) || !matches(`a\.txt +12 B`, out) ||
		!strings.Contains(out, "The folder holds more than is listed") {
		t.Fatalf("table:\n%s", out)
	}
	out, _, err = run(t, srv, t.TempDir(), "volume", "ls", "uploads", "--service", "web", "-q")
	if err != nil || out != "uploads/2026\nuploads/zz\nuploads/a.txt\nuploads/b.jpg\nuploads/latest\n" {
		t.Fatalf("-q: %v %q", err, out)
	}
	f.answer("GET /v1/serverless/services/"+svcID+"/volume/files", 200, map[string]interface{}{"path": "", "entries": []interface{}{}, "truncated": false})
	if out, _, err := run(t, srv, t.TempDir(), "volume", "ls", "--service", "web"); err != nil || strings.TrimSpace(out) != "web's volume is empty." {
		t.Fatalf("empty: %v %q", err, out)
	}
}

func TestVolumeDownloadSavesTheArchiveUnderTheAPIsName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	archive := bytes.Repeat([]byte("tar.gz bytes "), 100000)
	f.on("GET /v1/serverless/services/"+svcID+"/volume/archive", func(w http.ResponseWriter, _ call) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="uploads.tar.gz"`)
		_, _ = w.Write(archive)
	})
	dir := t.TempDir()
	out, _, err := run(t, srv, dir, "volume", "download", "uploads", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	if q := f.last("GET /v1/serverless/services/" + svcID + "/volume/archive").Query["path"]; len(q) != 1 || q[0] != "uploads" {
		t.Fatalf("asked for %v", q)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "uploads.tar.gz")); err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("saved %d bytes, %v", len(got), err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
	if !strings.Contains(out, "Downloaded uploads.tar.gz (1.3 MB).") {
		t.Fatalf("said %q", out)
	}

	// -f: a file, a folder to save it in, or standard output.
	file := filepath.Join(t.TempDir(), "copy.tar.gz")
	if _, _, err := run(t, srv, dir, "volume", "download", "--service", "web", "-f", file); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, archive) {
		t.Fatalf("-f FILE saved %d bytes", len(got))
	}
	folder := t.TempDir()
	if _, _, err := run(t, srv, dir, "volume", "download", "--service", "web", "--file", folder); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(folder, "uploads.tar.gz")); !bytes.Equal(got, archive) {
		t.Fatalf("-f FOLDER saved %d bytes", len(got))
	}
	out, _, err = run(t, srv, dir, "volume", "download", "--service", "web", "-f", "-")
	if err != nil || out != string(archive) {
		t.Fatalf("-f -: %v, %d bytes", err, len(out))
	}

	// A refusal leaves nothing behind.
	f.answer("GET /v1/serverless/services/"+svcID+"/volume/archive", 404, map[string]interface{}{
		"status": 404, "code": "not_found", "reason": "SERVERLESS_VOLUME_PATH_NOT_FOUND", "detail": "No such file or folder in the volume.",
	})
	empty := t.TempDir()
	if _, _, err := run(t, srv, empty, "volume", "download", "nope", "--service", "web"); err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Fatalf("refused: %v", err)
	}
	if left, _ := os.ReadDir(empty); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// tarNames are an archive's entries, sorted, and its files' contents.
func tarNames(t *testing.T, raw []byte) ([]string, map[string]string) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			files[h.Name] = string(b)
		}
	}
	sort.Strings(names)
	return names, files
}

func TestVolumeUploadSendsAFolderAsATarGzAndAFileAsItIs(t *testing.T) {
	f, srv := newFakeAPI(t)
	const put = "PUT /v1/serverless/services/" + svcID + "/volume/files"
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	f.on(put, func(w http.ResponseWriter, _ call) { w.WriteHeader(204) })
	// The packed folder goes through the temporary directory, and leaves it.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	src := t.TempDir()
	for name, content := range map[string]string{"index.html": "<h1>loja</h1>", "img/logo.png": "png"} {
		p := filepath.Join(src, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	_ = os.Mkdir(filepath.Join(src, "cache"), 0o755)
	if runtime.GOOS != "windows" {
		_ = os.Symlink("index.html", filepath.Join(src, "home.html"))
	}
	out, _, err := run(t, srv, t.TempDir(), "volume", "upload", src, "--to", "public", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	c := f.last(put)
	if c.Query["as"][0] != "archive" || c.Query["path"][0] != "public" || c.Header.Get("Content-Type") != "application/gzip" ||
		c.Header.Get("Content-Length") != strconv.Itoa(len(c.Body)) || c.Header.Get("Idempotency-Key") == "" {
		t.Fatalf("sent ?%v %v", c.Query, c.Header)
	}
	names, files := tarNames(t, c.Body)
	if strings.Join(names, " ") != "cache/ img/ img/logo.png index.html" || files["index.html"] != "<h1>loja</h1>" {
		t.Fatalf("packed %v %v", names, files)
	}
	if !strings.Contains(out, "(2 files,") || !strings.Contains(out, "into public/.") {
		t.Fatalf("said %q", out)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("the packed folder was left in %s: %v", tmp, left)
	}

	// A file is written at its name, at the root or in the folder --to ends in
	// or names (one the volume has: imports); elsewhere --to is its path.
	f.on("GET /v1/serverless/services/"+svcID+"/volume/files", func(w http.ResponseWriter, c call) {
		if c.Query["path"][0] == "imports" {
			reply(w, 200, map[string]interface{}{"path": "imports", "entries": []interface{}{}, "truncated": false})
			return
		}
		reply(w, 404, map[string]interface{}{"status": 404, "code": "SERVERLESS_VOLUME_PATH_NOT_FOUND", "detail": "No such file or folder in the volume."})
	})
	file := filepath.Join(src, "index.html")
	for to, want := range map[string]string{"": "index.html", "site/": "site/index.html", "site/home.html": "site/home.html", "imports": "imports/index.html"} {
		args := []string{"volume", "upload", file, "--service", "web"}
		if to != "" {
			args = append(args, "--to", to)
		}
		if _, _, err := run(t, srv, t.TempDir(), args...); err != nil {
			t.Fatal(err)
		}
		c := f.last(put)
		if c.Query["as"][0] != "file" || c.Query["path"][0] != want || c.Header.Get("Content-Type") != "application/octet-stream" || string(c.Body) != "<h1>loja</h1>" {
			t.Fatalf("--to %q sent ?%v %q", to, c.Query, c.Body)
		}
	}

	// --extract sends a .tar.gz of yours to unpack, into the root by default.
	tgz := filepath.Join(t.TempDir(), "site.tar.gz")
	_ = os.WriteFile(tgz, []byte("gzip"), 0o644)
	if _, _, err := run(t, srv, t.TempDir(), "volume", "upload", tgz, "--extract", "--service", "web"); err != nil {
		t.Fatal(err)
	}
	if c := f.last(put); c.Query["as"][0] != "archive" || c.Query["path"][0] != "" || c.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("--extract sent ?%v %v", c.Query, c.Header)
	}
	if _, _, err := run(t, srv, t.TempDir(), "volume", "upload", src, "--extract", "--service", "web"); err == nil || !strings.Contains(err.Error(), "is a folder") {
		t.Fatalf("--extract a folder: %v", err)
	}
}

func TestVolumeRmNeedsTheName(t *testing.T) {
	f, srv := newFakeAPI(t)
	const del = "DELETE /v1/serverless/services/" + svcID + "/volume/files"
	f.answer("GET /v1/serverless/services", 200, page(withVolume("WEB")))
	if _, _, err := run(t, srv, t.TempDir(), "volume", "rm", "uploads/tmp", "--service", "web"); err == nil || !strings.Contains(err.Error(), "--confirm web") {
		t.Fatalf("unconfirmed: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "volume", "rm", "/", "--service", "web", "--confirm", "web"); err == nil || !strings.Contains(err.Error(), "mw volume remove") {
		t.Fatalf("the root: %v", err)
	}
	if f.called(del) {
		t.Fatal("deleted without the name")
	}
	f.answer(del, 204, nil)
	out, _, err := run(t, srv, t.TempDir(), "volume", "rm", "uploads/tmp", "--service", "web", "--confirm", "web")
	if err != nil || out != "uploads/tmp deleted from web's volume.\n" {
		t.Fatalf("rm: %v %q", err, out)
	}
	if q := f.last(del).Query["path"]; len(q) != 1 || q[0] != "uploads/tmp" {
		t.Fatalf("deleted %v", q)
	}
}

func TestServicesCreateWithAVolumeAndWhatTheImageKeeps(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000009"
	const create = "POST /v1/serverless/projects/" + pid + "/services"
	const inspect = "POST /v1/serverless/images/inspect"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer(create, 200, map[string]interface{}{"service": service("WEB", "IMAGE"), "deployment": deployment("d1", 1, "QUEUED")})
	f.answer(inspect, 200, map[string]interface{}{"ports": []int{2368}, "volumes": []string{"/var/lib/ghost/content", "/data/cache"}, "user": "node"})
	out, _, err := run(t, srv, t.TempDir(), "services", "create", "web", "--project", "loja", "--image", "ghost:6", "--port", "2368", "--volume", "/data:5")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"port": float64(2368), "volume_mount_path": "/data", "volume_size_gb": float64(5), "max_instances": float64(1)}
	if body := jsonOf(t, f.last(create).Body); !reflect.DeepEqual(body["settings"], want) {
		t.Fatalf("created with %v", body["settings"])
	}
	if body := jsonOf(t, f.last(inspect).Body); !reflect.DeepEqual(body, map[string]interface{}{"image": "ghost:6"}) {
		t.Fatalf("inspected %v", body)
	}
	if !strings.Contains(out, "The image keeps data in /var/lib/ghost/content, outside the volume at /data") || strings.Contains(out, "/data/cache") {
		t.Fatalf("said %q", out)
	}

	// Without a volume: each path, and the command that keeps its data.
	if out, _, err = run(t, srv, t.TempDir(), "services", "create", "web", "--project", "loja", "--image", "ghost:6"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "The image keeps data in /var/lib/ghost/content: without a volume there it is lost on every restart. "+
		"Add one: `mw volume add --service web --path /var/lib/ghost/content --size 5`.") {
		t.Fatalf("said %q", out)
	}
	// With -o json the answer stays alone on standard output.
	out, errOut, err := run(t, srv, t.TempDir(), "services", "create", "web", "--project", "loja", "--image", "ghost:6", "-o", "json")
	if err != nil || strings.Contains(out, "The image keeps") || !strings.Contains(errOut, "The image keeps data in /var/lib/ghost/content") {
		t.Fatalf("json: %v %q %q", err, out, errOut)
	}
	// An image that cannot be read is no failure: the service was made.
	f.answer(inspect, 502, map[string]interface{}{"status": 502, "detail": "The image's registry could not be reached."})
	if out, _, err := run(t, srv, t.TempDir(), "services", "create", "web", "--project", "loja", "--image", "ghost:6"); err != nil || strings.Contains(out, "keeps data") {
		t.Fatalf("unreadable image: %v %q", err, out)
	}

	// A worker runs one instance; a cron job and a bad --volume are refused first.
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "jobs", "--project", "loja", "--image", "ghost:6", "--kind", "worker", "--volume", "/data:2"); err != nil {
		t.Fatal(err)
	}
	want = map[string]interface{}{"volume_mount_path": "/data", "volume_size_gb": float64(2), "instances": float64(1)}
	if body := jsonOf(t, f.last(create).Body); !reflect.DeepEqual(body["settings"], want) {
		t.Fatalf("worker created with %v", body["settings"])
	}
	made := f.count(create)
	for _, args := range [][]string{{"--kind", "cron", "--volume", "/data:2"}, {"--volume", "/data"}, {"--volume", "/data:0"}} {
		_, _, err := run(t, srv, t.TempDir(), append([]string{"services", "create", "jobs", "--project", "loja", "--image", "ghost:6"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "volume") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if f.count(create) != made {
		t.Fatal("a refused --volume was sent")
	}
}

func TestParseVolumeReadsPathAndSize(t *testing.T) {
	if p, n, err := parseVolume("/var/lib/ghost/content:10"); err != nil || p != "/var/lib/ghost/content" || n != 10 {
		t.Fatalf("%q %d %v", p, n, err)
	}
	for _, bad := range []string{"", "/data", ":5", "/data:", "/data:five", "/data:-1"} {
		if _, _, err := parseVolume(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// matches says whether pattern matches somewhere in s.
func matches(pattern, s string) bool { return regexp.MustCompile(pattern).MatchString(s) }
