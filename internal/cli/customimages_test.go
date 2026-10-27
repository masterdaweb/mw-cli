package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const imgID = "0193f0e2-6666-7000-8000-000000000001"

func customImageJSON(name, kind, status string) map[string]interface{} {
	return map[string]interface{}{
		"object": "custom_image", "id": imgID, "name": name, "filename": name + ".qcow2", "kind": kind, "status": status,
		"size_bytes": "2147483648", "virtual_size_bytes": "10737418240", "min_disk_gb": 10, "format": "qcow2", "sha256": "abc",
		"requires_efi": true, "requires_tpm": false, "has_cloud_init": true, "default_username": "debian",
		"expires_at": "2026-11-25T00:00:00Z", "failure_reason": nil, "progress": nil, "instance_count": 2,
		"regions": []interface{}{map[string]interface{}{"region_id": "3", "state": "ready"}}, "created_at": "2026-10-26T12:00:00Z",
	}
}

func quotaJSON() map[string]interface{} {
	return map[string]interface{}{"object": "custom_image_quota", "images": 1, "max_images": 10, "total_bytes": "1000",
		"max_total_bytes": "100000000", "max_file_bytes": "50000000", "retention_days": 30, "uploads_enabled": true,
		"accepted_extensions": []string{".qcow2", ".img", ".raw", ".vmdk", ".iso"}}
}

// storage is the presigned side of an upload: it keeps each part's bytes,
// fails the parts it is told to (once each, or always), and counts how many
// PUTs are in flight at once.
type storage struct {
	mu       sync.Mutex
	parts    map[int][]byte
	failOnce map[int]bool
	failAll  map[int]bool
	inFlight int
	maxLive  int
	sawAuth  bool
	*httptest.Server
}

func newStorage(t *testing.T) *storage {
	s := &storage{parts: map[int][]byte{}, failOnce: map[int]bool{}, failAll: map[int]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/part/"))
		s.mu.Lock()
		s.inFlight++
		if s.inFlight > s.maxLive {
			s.maxLive = s.inFlight
		}
		s.sawAuth = s.sawAuth || r.Header.Get("Authorization") != ""
		fail := s.failAll[n] || s.failOnce[n]
		delete(s.failOnce, n)
		s.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		time.Sleep(15 * time.Millisecond) // long enough for parts to overlap
		s.mu.Lock()
		s.inFlight--
		if !fail && r.Method == http.MethodPut && int64(len(body)) == r.ContentLength {
			s.parts[n] = body
		}
		s.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"etag"`)
	}))
	t.Cleanup(s.Close)
	return s
}

// missing is what the upload still lacks, the way GET …/upload says it.
func (s *storage) missing(count int, partSize int64) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parts []interface{}
	var have int64
	for n := 1; n <= count; n++ {
		if b, ok := s.parts[n]; ok {
			have += int64(len(b))
			continue
		}
		parts = append(parts, map[string]interface{}{"part_number": n, "url": s.URL + "/part/" + strconv.Itoa(n) + "?sig=fresh"})
	}
	if parts == nil {
		parts = []interface{}{}
	}
	return map[string]interface{}{"object": "custom_image_upload", "image_id": imgID, "part_size_bytes": partSize, "part_count": count, "uploaded_bytes": have, "parts": parts}
}

func (s *storage) joined(count int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	for n := 1; n <= count; n++ {
		b.Write(s.parts[n])
	}
	return b.Bytes()
}

// imageFile is a file of 10 parts of 1 KiB and a short last one.
func imageFile(t *testing.T, name string) (string, []byte) {
	t.Helper()
	data := make([]byte, 10*1024+123)
	for i := range data {
		data[i] = byte(i * 7)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestCustomImageUploadInPartsResumesAndWaits(t *testing.T) {
	f, srv := newFakeAPI(t)
	st := newStorage(t)
	st.failOnce[3] = true // a part fails the first time: asked for again
	path, data := imageFile(t, "debian-12.qcow2")
	const parts, size = 11, int64(1024)

	f.answer("GET /v1/images/quota", 200, quotaJSON())
	f.on("POST /v1/images", func(w http.ResponseWriter, c call) {
		up := st.missing(parts, size)
		reply(w, 201, up)
	})
	f.on("GET /v1/images/"+imgID+"/upload", func(w http.ResponseWriter, c call) { reply(w, 200, st.missing(parts, size)) })
	f.answer("POST /v1/images/"+imgID+"/upload/complete", 202, map[string]interface{}{"object": "operation_accepted", "action": "image_import",
		"resource_type": "custom_image", "resource_id": imgID, "operation_id": "op-i"})
	polls := 0
	f.on("GET /v1/images/"+imgID, func(w http.ResponseWriter, c call) {
		f.mu.Lock()
		polls++
		n := polls
		f.mu.Unlock()
		img := customImageJSON("debian-12", "disk_image", "ready")
		if n < 3 {
			img["status"], img["progress"] = "converting", 40
		}
		reply(w, 200, img)
	})

	out, errOut, err := run(t, srv, t.TempDir(), "custom-images", "upload", path, "--name", "debian-12", "--wait")
	if err != nil {
		t.Fatalf("upload: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "Image debian-12 ("+imgID+") is ready") {
		t.Errorf("output: %q", out)
	}
	if got := st.joined(parts); !bytes.Equal(got, data) {
		t.Fatalf("storage holds %d bytes, not the file's %d", len(got), len(data))
	}
	b := jsonOf(t, f.last("POST /v1/images").Body)
	if b["filename"] != "debian-12.qcow2" || b["size_bytes"] != float64(len(data)) || b["name"] != "debian-12" || len(b) != 3 {
		t.Errorf("start body: %v", b)
	}
	if !f.called("GET /v1/images/" + imgID + "/upload") {
		t.Error("a failed part was not asked for again")
	}
	if c := f.last("POST /v1/images/" + imgID + "/upload/complete"); c.Header.Get("Idempotency-Key") == "" {
		t.Error("complete without an Idempotency-Key")
	}
	st.mu.Lock()
	maxLive, sawAuth := st.maxLive, st.sawAuth
	st.mu.Unlock()
	if maxLive < 2 || maxLive > uploadParallel {
		t.Errorf("%d parts in flight at once, want 2 to %d", maxLive, uploadParallel)
	}
	if sawAuth {
		t.Error("the API token went to storage")
	}
	if !strings.Contains(errOut, "Uploading debian-12.qcow2: 100%") || !strings.Contains(errOut, "converting 40%") {
		t.Errorf("progress: %q", errOut)
	}
}

func TestCustomImageUploadRefusedBeforeSending(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/images/quota", 200, quotaJSON())
	exe, _ := imageFile(t, "setup.exe")
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", exe); err == nil || !strings.Contains(err.Error(), ".qcow2") {
		t.Errorf("an .exe: %v", err)
	}
	q := quotaJSON()
	q["max_file_bytes"] = "1000"
	f.answer("GET /v1/images/quota", 200, q)
	big, _ := imageFile(t, "big.qcow2")
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", big); err == nil || !strings.Contains(err.Error(), "the largest file") {
		t.Errorf("too large: %v", err)
	}
	q = quotaJSON()
	q["images"] = 10
	f.answer("GET /v1/images/quota", 200, q)
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", big); err == nil || !strings.Contains(err.Error(), "delete one first") {
		t.Errorf("too many: %v", err)
	}
	if f.called("POST /v1/images") {
		t.Error("started an upload the quota refuses")
	}
}

// A part that keeps failing: after the retries, how to resume.
func TestCustomImageUploadSaysHowToResume(t *testing.T) {
	f, srv := newFakeAPI(t)
	st := newStorage(t)
	st.failAll[2] = true
	path, _ := imageFile(t, "debian-12.qcow2")
	f.answer("GET /v1/images/quota", 200, quotaJSON())
	f.on("POST /v1/images", func(w http.ResponseWriter, c call) { reply(w, 201, st.missing(11, 1024)) })
	f.on("GET /v1/images/"+imgID+"/upload", func(w http.ResponseWriter, c call) { reply(w, 200, st.missing(11, 1024)) })
	_, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", path)
	if err == nil || !strings.Contains(err.Error(), "part 2") || !strings.Contains(err.Error(), "--resume "+imgID) {
		t.Fatalf("a part that never arrives: %v", err)
	}
	if f.called("POST /v1/images/" + imgID + "/upload/complete") {
		t.Error("completed an upload with a part missing")
	}

	// --resume sends only what is missing, then completes.
	delete(st.failAll, 2)
	f.answer("POST /v1/images/"+imgID+"/upload/complete", 202, map[string]interface{}{"object": "operation_accepted", "action": "image_import", "resource_type": "custom_image", "resource_id": imgID, "operation_id": nil})
	out, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", path, "--resume", imgID)
	if err != nil || !strings.Contains(out, "Uploaded debian-12.qcow2") {
		t.Fatalf("resume = %q, %v", out, err)
	}
	// Another file than the one the upload started with is refused.
	other := filepath.Join(t.TempDir(), "other.qcow2")
	_ = os.WriteFile(other, make([]byte, 100), 0o644)
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "upload", other, "--resume", imgID); err == nil || !strings.Contains(err.Error(), "the file the upload started with") {
		t.Errorf("another file: %v", err)
	}
}

func TestCustomImagesListGetChangeDelete(t *testing.T) {
	f, srv := newFakeAPI(t)
	iso := customImageJSON("win2022", "iso", "uploading")
	iso["id"], iso["progress"], iso["size_bytes"] = "0193f0e2-6666-7000-8000-000000000002", 40, nil
	f.answer("GET /v1/images", 200, map[string]interface{}{"object": "list", "data": []interface{}{customImageJSON("debian-12", "disk_image", "ready"), iso}})
	out, _, err := run(t, srv, t.TempDir(), "custom-images")
	if err != nil || !strings.Contains(out, "debian-12") || !strings.Contains(out, "disk image") || !strings.Contains(out, "ISO") || !strings.Contains(out, "uploading 40%") {
		t.Fatalf("list = %q, %v", out, err)
	}
	if out, _, err := run(t, srv, t.TempDir(), "my-images", "list", "--kind", "iso", "-q"); err != nil || url.Values(f.last("GET /v1/images").Query).Get("kind") != "iso" || !strings.Contains(out, imgID) {
		t.Errorf("--kind iso -q = %q, %v", out, err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "--kind", "exe"); err == nil {
		t.Error("--kind exe accepted")
	}

	f.answer("GET /v1/images/"+imgID, 200, customImageJSON("debian-12", "disk_image", "ready"))
	out, _, err = run(t, srv, t.TempDir(), "custom-images", "get", "debian-12")
	if err != nil || !strings.Contains(out, "UEFI") || !strings.Contains(out, "region 3: ready") || !strings.Contains(out, "debian") {
		t.Fatalf("get = %q, %v", out, err)
	}

	f.answer("GET /v1/images/quota", 200, quotaJSON())
	if out, _, err := run(t, srv, t.TempDir(), "custom-images", "quota"); err != nil || !strings.Contains(out, "1 of 10") || !strings.Contains(out, "30 days unless renewed") {
		t.Errorf("quota = %q, %v", out, err)
	}

	f.answer("PATCH /v1/images/"+imgID, 200, customImageJSON("debian-12", "disk_image", "ready"))
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "update", "debian-12", "--cloud-init", "--default-username", "admin", "--efi=false"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("PATCH /v1/images/"+imgID).Body); len(b) != 3 || b["has_cloud_init"] != true || b["default_username"] != "admin" || b["requires_efi"] != false {
		t.Errorf("update body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "update", "debian-12", "--default-username", ""); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("PATCH /v1/images/"+imgID).Body); len(b) != 1 || b["default_username"] != nil {
		t.Errorf("clearing the user: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "rename", "debian-12", "debian-12-base"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("PATCH /v1/images/"+imgID).Body); len(b) != 1 || b["name"] != "debian-12-base" {
		t.Errorf("rename body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "update", "debian-12"); err == nil {
		t.Error("an update with nothing to change")
	}

	f.answer("DELETE /v1/images/"+imgID, 204, nil)
	if _, _, err := runWithInput(t, srv, t.TempDir(), "y\n", "custom-images", "delete", "debian-12"); err == nil || f.called("DELETE /v1/images/"+imgID) {
		t.Fatal("deleted without the name typed")
	}
	if out, _, err := runWithInput(t, srv, t.TempDir(), "debian-12\n", "custom-images", "delete", "debian-12"); err != nil || !strings.Contains(out, "Instances built from it: 2") || !strings.Contains(out, "deleted") {
		t.Errorf("delete = %q, %v", out, err)
	}
	f.answer("POST /v1/images/"+imgID+"/upload/abort", 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "custom-images", "abort", imgID, "--yes"); err != nil {
		t.Error(err)
	}
}

// --custom-image takes a name as well as an id; an ISO or an image that is
// not ready is refused before anything is priced.
func TestCustomImageByNameInCreateAndReinstall(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh", "available": true}))
	f.answer("POST /v1/orders/quote", 200, quoteJSON(true))
	f.answer("POST /v1/orders", 201, map[string]interface{}{"object": "order", "id": "ord1", "status": "provisioned", "subscription_id": "sub1", "instance_ids": []string{"24400"}})
	iso := customImageJSON("win2022", "iso", "ready")
	iso["id"] = "0193f0e2-6666-7000-8000-000000000002"
	busy := customImageJSON("arch", "disk_image", "converting")
	busy["id"] = "0193f0e2-6666-7000-8000-000000000003"
	f.answer("GET /v1/images", 200, map[string]interface{}{"object": "list", "data": []interface{}{customImageJSON("debian-12", "disk_image", "ready"), iso, busy}})

	if _, _, err := run(t, srv, t.TempDir(), "instances", "create", "--plan", "c2m4", "--custom-image", "debian-12", "--yes"); err != nil {
		t.Fatal(err)
	}
	if img := jsonOf(t, f.last("POST /v1/orders").Body)["image"].(map[string]interface{}); img["type"] != "client_image" || img["image_id"] != imgID {
		t.Errorf("order image: %v", img)
	}
	for ref, want := range map[string]string{"win2022": "is an ISO", "arch": "not ready yet", "nope": "no custom image nope"} {
		if _, _, err := run(t, srv, t.TempDir(), "instances", "create", "--plan", "c2m4", "--custom-image", ref, "--yes"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", ref, err, want)
		}
	}

	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("POST /v1/instances/24377/reinstall", 202, map[string]interface{}{"object": "operation_accepted", "action": "reinstall", "resource_type": "instance", "resource_id": "24377", "instance_id": "24377", "operation_id": "op-1"})
	if _, _, err := runWithInput(t, srv, t.TempDir(), "24377\n", "instances", "reinstall", "24377", "--custom-image", "Debian-12"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("POST /v1/instances/24377/reinstall").Body); b["custom_image_id"] != imgID {
		t.Errorf("reinstall body: %v", b)
	}
}
