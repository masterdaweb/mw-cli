package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputFormats(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(
		map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh", "available": true, "ipv4_available": true, "ipv6_available": false},
	))
	f.answer("GET /v1/serverless/locations", 200, []interface{}{})
	f.answer("GET /v1/databases/locations", 200, []interface{}{})
	// YAML keeps the API's field order.
	out, _, err := run(t, srv, t.TempDir(), "regions", "-o", "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "- id: \"3\"\n  name: Belo Horizonte\n  region_name: br-bh\n") {
		t.Fatalf("yaml:\n%s", out)
	}
	// --json is -o json.
	out, _, err = run(t, srv, t.TempDir(), "regions", "--json")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "[") || !strings.Contains(out, `"region_name": "br-bh"`) {
		t.Fatalf("json: %q %v", out, err)
	}
	// --no-headers.
	out, _, err = run(t, srv, t.TempDir(), "regions", "--no-headers")
	if err != nil || strings.Contains(out, "AVAILABLE") || !strings.Contains(out, "br-bh") {
		t.Fatalf("no headers: %q %v", out, err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "regions", "-o", "xml"); err == nil {
		t.Error("-o xml accepted")
	}
}

func TestFingerprintIsOpenSSHs(t *testing.T) {
	blob := []byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20" + strings.Repeat("k", 32))
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " ana@laptop"
	sum := sha256.Sum256(blob)
	want := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	got, err := fingerprint(line)
	if err != nil || got != want {
		t.Fatalf("fingerprint = %q, %v; want %q", got, err, want)
	}
	if keyName(line, "/x/id_ed25519.pub") != "ana@laptop" || keyName("ssh-ed25519 AAAA", "/x/work.pub") != "work" {
		t.Error("key names")
	}
	if _, err := fingerprint("not a key"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestSSHKeyFileIsAddedOnceAndUsed(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := t.TempDir()
	blob := []byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20" + strings.Repeat("z", 32))
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " ana@laptop"
	pub := filepath.Join(dir, "id_ed25519.pub")
	os.WriteFile(pub, []byte(line+"\n"), 0o644)
	priv := filepath.Join(dir, "id_ed25519")
	os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nxxx\n"), 0o600)

	f.answer("GET /v1/ssh-keys", 200, page())
	f.answer("POST /v1/ssh-keys", 200, map[string]interface{}{"id": "k9", "name": "ana@laptop", "type": "ssh-ed25519", "fingerprint": "SHA256:x", "public_key": line, "created_at": "2026-10-26T10:00:00Z"})
	out, _, err := run(t, srv, dir, "ssh-keys", "add", pub)
	if err != nil || !strings.Contains(out, "SSH key added") {
		t.Fatalf("add = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/ssh-keys").Body); b["name"] != "ana@laptop" || b["public_key"] != line {
		t.Errorf("add body: %v", b)
	}
	// A private key is refused, never sent.
	_, _, err = run(t, srv, dir, "ssh-keys", "add", priv)
	if err == nil || !strings.Contains(err.Error(), "PRIVATE key") {
		t.Fatalf("private key: %v", err)
	}

	// --ssh-key with a .pub path: added when new, by id when known.
	ids, err := func() ([]string, error) {
		t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "c.json"))
		t.Setenv("MW_TOKEN", "mwk_test")
		t.Setenv("MW_API_URL", srv.URL)
		a := &App{Version: "test", Out: &strings.Builder{}, Err: &strings.Builder{}}
		return a.resolveSSHKeys(context.Background(), []string{pub})
	}()
	if err != nil || len(ids) != 1 || ids[0] != "k9" {
		t.Fatalf("resolve = %v, %v", ids, err)
	}
}

func TestProjectResourcesNewPathAndFallback(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	f.answer("GET /v1/serverless/projects/"+pid, 200, map[string]interface{}{"id": pid, "name": "loja"})
	f.answer("GET /v1/projects/"+pid+"/resources", 200, []interface{}{
		map[string]interface{}{"object": "project_resource", "kind": "INSTANCE", "id": "24377", "name": "web-1", "status": "RUNNING", "detail": "203.0.113.10", "image": "Ubuntu 24.04"},
		map[string]interface{}{"object": "project_resource", "kind": "BUCKET", "id": "b1", "name": "loja-fotos", "status": "ACTIVE", "detail": "br-bh"},
	})
	out, _, err := run(t, srv, t.TempDir(), "projects", "resources", pid)
	if err != nil || !strings.Contains(out, "web-1") || !strings.Contains(out, "203.0.113.10 · Ubuntu 24.04") || !strings.Contains(out, "loja-fotos") {
		t.Fatalf("list = %q, %v", out, err)
	}

	// An API with only /v1/serverless/projects: the CLI falls back to it.
	f.on("POST /v1/projects/"+pid+"/resources", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
	f.answer("GET /v1/instances", 200, page(instanceJSON("24377", "web-1", "running")))
	f.answer("POST /v1/serverless/projects/"+pid+"/resources", 200, []interface{}{})
	if _, _, err := run(t, srv, t.TempDir(), "projects", "resources", "add", pid, "instance", "web-1"); err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/resources").Body)
	r := b["resources"].([]interface{})[0].(map[string]interface{})
	if r["kind"] != "INSTANCE" || r["id"] != "24377" {
		t.Errorf("add body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "projects", "resources", "add", pid, "printer", "x"); err == nil {
		t.Error("an unknown kind accepted")
	}

	f.answer("DELETE /v1/projects/"+pid+"/resources/bucket/loja-fotos", 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "projects", "resources", "remove", pid, "bucket", "loja-fotos"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDeleteNewPathAndFallback(t *testing.T) {
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	for _, newAPI := range []bool{true, false} {
		f, srv := newFakeAPI(t)
		f.answer("GET /v1/serverless/projects/"+pid, 200, map[string]interface{}{"id": pid, "name": "loja"})
		want := "DELETE /v1/serverless/projects/" + pid
		if newAPI {
			f.answer("GET /v1/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
			want = "DELETE /v1/projects/" + pid
		} else {
			f.on("GET /v1/projects", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
		}
		f.answer(want, 200, map[string]interface{}{"id": pid, "name": "loja", "deleting": true})
		if _, _, err := run(t, srv, t.TempDir(), "projects", "delete", pid); err == nil || !strings.Contains(err.Error(), "--confirm loja") {
			t.Fatalf("delete without --confirm = %v", err)
		}
		if _, _, err := run(t, srv, t.TempDir(), "projects", "delete", pid, "--confirm", "loja"); err != nil {
			t.Fatal(err)
		}
		if q := url.Values(f.last(want).Query); q.Get("confirm") != "loja" {
			t.Errorf("%s query: %v", want, q)
		}
	}
}

func TestBucketCreateGoesInTheProject(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	f.answer("POST /v1/object-storage/buckets", 200, map[string]interface{}{"name": "loja-fotos", "location": "br-bh"})
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("POST /v1/projects/"+pid+"/resources", 200, []interface{}{})
	out, _, err := run(t, srv, t.TempDir(), "storage", "create", "loja-fotos", "--project", "loja")
	if err != nil || !strings.Contains(out, "Put in project loja") {
		t.Fatalf("create = %q, %v", out, err)
	}
	r := jsonOf(t, f.last("POST /v1/projects/"+pid+"/resources").Body)["resources"].([]interface{})[0].(map[string]interface{})
	if r["kind"] != "BUCKET" || r["id"] != "loja-fotos" {
		t.Errorf("resource: %v", r)
	}
}

func TestStoragePresignAndRm(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/object-storage/buckets/loja/objects/download-url", 200, map[string]interface{}{"url": "https://s3.example/loja/a.pdf?sig=1", "expires_at": "2026-10-26T12:00:00Z"})
	out, _, err := run(t, srv, t.TempDir(), "storage", "presign", "loja", "a.pdf", "--expires", "30m")
	if err != nil || strings.TrimSpace(out) != "https://s3.example/loja/a.pdf?sig=1" {
		t.Fatalf("presign = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/object-storage/buckets/loja/objects/download-url").Body); len(b) != 2 || b["key"] != "a.pdf" || b["expires_seconds"] != float64(1800) {
		t.Errorf("presign body: %v", b)
	}
	// The API takes 60 seconds to 7 days.
	for _, bad := range []string{"30s", "200h"} {
		if _, _, err := run(t, srv, t.TempDir(), "storage", "presign", "loja", "a.pdf", "--expires", bad); err == nil {
			t.Errorf("--expires %s accepted", bad)
		}
	}
	// An upload link: the key and the type, nothing else, and the curl its
	// signature takes.
	f.answer("POST /v1/object-storage/buckets/loja/objects/upload-url", 200, map[string]interface{}{"url": "https://s3.example/up", "expires_at": "2026-10-26T12:00:00Z"})
	out, _, err = run(t, srv, t.TempDir(), "storage", "presign", "loja", "n.csv", "--upload", "--content-type", "text/csv")
	if err != nil || !strings.Contains(out, "curl -X PUT -H 'Content-Type: text/csv' --upload-file n.csv https://s3.example/up") {
		t.Fatalf("upload presign = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/object-storage/buckets/loja/objects/upload-url").Body); len(b) != 2 || b["key"] != "n.csv" || b["content_type"] != "text/csv" {
		t.Errorf("upload body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "storage", "presign", "loja", "n.csv", "--upload", "--expires", "1h"); err == nil {
		t.Error("an upload link took --expires")
	}
	f.answer("POST /v1/object-storage/buckets/loja/objects/delete", 200, map[string]interface{}{"deleted": 2})
	out, _, err = run(t, srv, t.TempDir(), "storage", "rm", "loja", "a.pdf", "b.pdf", "--yes")
	if err != nil || !strings.Contains(out, "2 file(s) deleted") {
		t.Fatalf("rm = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/object-storage/buckets/loja/objects/delete").Body); len(b) != 1 || len(b["keys"].([]interface{})) != 2 {
		t.Errorf("rm body: %v", b)
	}
}

func TestVersionAndCompletion(t *testing.T) {
	_, srv := newFakeAPI(t)
	out, _, err := run(t, srv, t.TempDir(), "version")
	if err != nil || !strings.HasPrefix(out, "mw test (") {
		t.Fatalf("version = %q, %v", out, err)
	}
	out, _, err = run(t, srv, t.TempDir(), "completion", "bash")
	if err != nil || !strings.Contains(out, "bash completion") {
		t.Fatalf("completion: %v", err)
	}
}
