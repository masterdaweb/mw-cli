package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func instanceJSON(id, name, state string) map[string]interface{} {
	return map[string]interface{}{
		"object": "instance", "id": id, "name": name, "description": nil,
		"power_state_cached": state, "suspended": false,
		"region":       map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh"},
		"specs":        map[string]interface{}{"vcpus": 2, "ram_mb": 4096, "disk_gb": 80},
		"hostname":     nil,
		"ip_addresses": []interface{}{map[string]interface{}{"address": "203.0.113.10", "version": "ipv4", "primary": true}},
		"vpc_id":       nil, "offering_code": "c2m4", "created_at": "2026-10-20T12:00:00Z",
	}
}

func routeMissingReply(w http.ResponseWriter, path string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(404)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": 404, "code": "not_found", "title": "Not Found", "detail": "Cannot GET " + path})
}

func TestInstancesListAndGetByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances", 200, page(instanceJSON("24377", "web-1", "running"), instanceJSON("24378", "web-2", "stopped")))
	out, _, err := run(t, srv, t.TempDir(), "instances")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "web-1", "24378", "br-bh", "c2m4", "2 vCPU · 4 GB RAM · 80 GB disk", "203.0.113.10"} {
		if !strings.Contains(out, want) {
			t.Errorf("list misses %q:\n%s", want, out)
		}
	}
	// -q: the ids alone, for pipes.
	out, _, err = run(t, srv, t.TempDir(), "instances", "-q")
	if err != nil || out != "24377\n24378\n" {
		t.Fatalf("quiet list = %q, %v", out, err)
	}
	// By name: the list is read and the one with that name shown.
	out, _, err = run(t, srv, t.TempDir(), "instances", "get", "web-2")
	if err != nil || !strings.Contains(out, "24378") || !strings.Contains(out, "stopped") {
		t.Fatalf("get by name = %q, %v", out, err)
	}
	// By id: one read.
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	out, _, err = run(t, srv, t.TempDir(), "-o", "json", "instances", "get", "24377")
	if err != nil || jsonOf(t, []byte(out))["id"] != "24377" {
		t.Fatalf("get by id = %q, %v", out, err)
	}
}

func quoteJSON(purchasable bool) map[string]interface{} {
	return map[string]interface{}{
		"object": "quote", "offering_code": "c2m4", "term": "MONTHLY", "purchasable": purchasable,
		"violations": []interface{}{}, "hourly_rate_micro_cents": "6849", "term_total_cents": "4999", "currency": "BRL",
		"effective": map[string]interface{}{}, "warnings": []interface{}{},
		"line_items": []interface{}{map[string]interface{}{"label": "c2m4", "kind": "compute", "quantity": 1, "unit_rate_micro_cents_per_hour": "6849", "rate_micro_cents_per_hour": "6849", "cycle_cents": "4999"}},
	}
}

func TestInstanceCreateQuotesConfirmsAndOrders(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh", "available": true, "ipv4_available": true, "ipv6_available": true}))
	f.answer("POST /v1/orders/quote", 200, quoteJSON(true))
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": "0193f0e2-2222-7000-8000-000000000001", "name": "loja"}))
	f.answer("GET /v1/ssh-keys", 200, page(map[string]interface{}{"id": "k1", "name": "work", "type": "ssh-ed25519", "fingerprint": "SHA256:abc", "public_key": "ssh-ed25519 AAAA work", "created_at": "2026-10-01T00:00:00Z"}))
	f.answer("POST /v1/orders", 201, map[string]interface{}{"object": "order", "id": "ord1", "status": "provisioned", "subscription_id": "sub1", "instance_ids": []string{"24400"}})
	dir := t.TempDir()
	ud := filepath.Join(dir, "init.yaml")
	os.WriteFile(ud, []byte("#cloud-config\npackages: [nginx]\n"), 0o644)

	// Without --yes and nothing on standard input: priced, never ordered.
	_, _, err := run(t, srv, dir, "instances", "create", "--plan", "c2m4", "--image", "ubuntu-24-04")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unconfirmed create: %v", err)
	}
	for _, c := range f.calls {
		if c.Method == "POST" && c.Path == "/v1/orders" {
			t.Fatal("ordered without being confirmed")
		}
	}

	out, _, err := runWithInput(t, srv, dir, "y\n", "instances", "create", "--plan", "c2m4", "--image", "ubuntu-24-04",
		"--region", "br-bh", "--name", "web-3", "--project", "loja", "--ssh-key", "work", "--user-data", "@"+ud,
		"--addon", "backup-slot=2", "--extra-disk", "50", "--term", "annual")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Total:") || !strings.Contains(out, "instance 24400 is being built") {
		t.Fatalf("create output:\n%s", out)
	}
	b := jsonOf(t, f.last("POST /v1/orders").Body)
	if b["offering_code"] != "c2m4" || b["term"] != "ANNUAL" || b["region_id"] != float64(3) || b["name"] != "web-3" {
		t.Errorf("order body: %v", b)
	}
	if img := b["image"].(map[string]interface{}); img["type"] != "template" || img["code"] != "ubuntu-24-04" {
		t.Errorf("image: %v", img)
	}
	if b["project_id"] != "0193f0e2-2222-7000-8000-000000000001" {
		t.Errorf("project_id: %v", b["project_id"])
	}
	if keys := b["ssh_key_ids"].([]interface{}); len(keys) != 1 || keys[0] != "k1" {
		t.Errorf("ssh_key_ids: %v", b["ssh_key_ids"])
	}
	if !strings.Contains(b["user_data"].(string), "packages: [nginx]") {
		t.Errorf("user_data: %v", b["user_data"])
	}
	if ad := b["addons"].([]interface{})[0].(map[string]interface{}); ad["code"] != "backup-slot" || ad["quantity"] != float64(2) {
		t.Errorf("addons: %v", b["addons"])
	}
	if d := b["extra_disks"].([]interface{})[0].(map[string]interface{}); d["size_gb"] != float64(50) {
		t.Errorf("extra_disks: %v", b["extra_disks"])
	}
	// The quote saw the configuration only, never the payment or the secrets.
	q := jsonOf(t, f.last("POST /v1/orders/quote").Body)
	for _, k := range []string{"user_data", "ssh_key_ids", "project_id", "name"} {
		if _, ok := q[k]; ok {
			t.Errorf("the quote got %s", k)
		}
	}
	// Every write carries an Idempotency-Key.
	if f.last("POST /v1/orders").Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key on the order")
	}
}

func TestInstanceCreateRefusesUnpurchasable(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "BH", "available": true}))
	q := quoteJSON(false)
	q["violations"] = []interface{}{map[string]interface{}{"kind": "min_ram_mb", "required": 4096, "actual": 2048, "remedy": "choose_larger_plan"}}
	f.answer("POST /v1/orders/quote", 200, q)
	out, _, err := run(t, srv, t.TempDir(), "instances", "create", "--plan", "c1m2", "--image", "windows-2022", "--yes")
	if err == nil || !strings.Contains(out, "needs ram_mb 4096") {
		t.Fatalf("unpurchasable: %v\n%s", err, out)
	}
}

func TestInstanceCreateWaitsForPaymentAndBoot(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "BH", "available": true}))
	f.answer("POST /v1/orders/quote", 200, quoteJSON(true))
	f.answer("POST /v1/orders", 202, map[string]interface{}{"object": "order", "id": "ord2", "status": "pending_payment", "instance_ids": []string{},
		"payment": map[string]interface{}{"invoice_id": "INV-9", "amount_cents": "4999", "currency": "BRL", "pay_url": "https://pay.example/INV-9"}})
	var polls int32
	f.on("GET /v1/orders/ord2", func(w http.ResponseWriter, _ call) {
		if atomic.AddInt32(&polls, 1) < 3 {
			reply(w, 200, map[string]interface{}{"object": "order", "id": "ord2", "status": "pending_payment", "instance_ids": []string{}})
			return
		}
		reply(w, 200, map[string]interface{}{"object": "order", "id": "ord2", "status": "provisioned", "instance_ids": []string{"24401"}})
	})
	var reads int32
	f.on("GET /v1/instances/24401/status", func(w http.ResponseWriter, c call) {
		if url.Values(c.Query).Get("fresh") != "true" {
			t.Errorf("status read without fresh=true")
		}
		if atomic.AddInt32(&reads, 1) < 2 {
			reply(w, 200, map[string]interface{}{"instance_id": "24401", "power_state": "stopped", "busy": true, "deployment_progress": 40})
			return
		}
		reply(w, 200, map[string]interface{}{"instance_id": "24401", "power_state": "running", "busy": false, "deployment_progress": nil})
	})
	out, _, err := run(t, srv, t.TempDir(), "instances", "create", "--plan", "c2m4", "--image", "debian-12", "--yes", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pay at https://pay.example/INV-9") || !strings.Contains(out, "Instance 24401 is up") {
		t.Fatalf("wait output:\n%s", out)
	}
}

func TestInstanceDeleteNeedsTheIDTyped(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.on("GET /v1/instances/24377/removal-preview", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
	f.answer("DELETE /v1/instances/24377", 200, map[string]interface{}{"object": "instance_deletion", "instance_id": "24377", "deleted": true, "message": "ok"})

	_, _, err := runWithInput(t, srv, t.TempDir(), "web-1\n", "instances", "delete", "24377")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("wrong answer: %v", err)
	}
	out, _, err := runWithInput(t, srv, t.TempDir(), "24377\n", "instances", "delete", "24377")
	if err != nil || !strings.Contains(out, "destroyed") {
		t.Fatalf("delete = %q, %v", out, err)
	}
	c := f.last("DELETE /v1/instances/24377")
	if c.Query["confirm"][0] != "24377" || c.Header.Get("Idempotency-Key") == "" {
		t.Errorf("delete call: %v %v", c.Query, c.Header)
	}
}

func TestInstancePowerWaits(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "stopped"))
	f.answer("POST /v1/instances/24377/power", 202, map[string]interface{}{"object": "task", "instance_id": "24377", "action": "start", "task_id": "task_1"})
	var n int32
	f.on("GET /v1/instances/24377/status", func(w http.ResponseWriter, _ call) {
		state := "stopped"
		if atomic.AddInt32(&n, 1) > 1 {
			state = "running"
		}
		reply(w, 200, map[string]interface{}{"instance_id": "24377", "power_state": state, "busy": false})
	})
	out, _, err := run(t, srv, t.TempDir(), "instances", "start", "24377", "--wait")
	if err != nil || !strings.Contains(out, "started") {
		t.Fatalf("start --wait = %q, %v", out, err)
	}
	if jsonOf(t, f.last("POST /v1/instances/24377/power").Body)["action"] != "start" {
		t.Error("wrong action")
	}
}

func TestInstanceReinstallAndNotYetOffered(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/ssh-keys", 200, page(map[string]interface{}{"id": "k1", "name": "work", "fingerprint": "SHA256:abc"}))
	f.answer("POST /v1/instances/24377/reinstall", 202, map[string]interface{}{"object": "operation_accepted", "action": "reinstall", "resource_type": "instance", "resource_id": "24377", "operation_id": "op9"})
	f.answer("GET /v1/operations/op9", 200, map[string]interface{}{"id": "op9", "type": "vm_reinstall", "status": "completed", "created_at": "2026-10-26T10:00:00Z"})
	out, _, err := runWithInput(t, srv, t.TempDir(), "24377\n", "instances", "reinstall", "24377", "--image", "debian-12", "--ssh-key", "work", "--wait")
	if err != nil || !strings.Contains(out, "Reinstall of web-1 (24377)") {
		t.Fatalf("reinstall = %q, %v", out, err)
	}
	b := jsonOf(t, f.last("POST /v1/instances/24377/reinstall").Body)
	if b["image"] != "debian-12" || b["ssh_key_ids"].([]interface{})[0] != "k1" {
		t.Errorf("reinstall body: %v", b)
	}
	// An API without the endpoint says so, instead of a bare 404.
	f.on("POST /v1/instances/24377/reinstall", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
	_, _, err = run(t, srv, t.TempDir(), "instances", "reinstall", "24377", "--image", "debian-12", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not available on this API yet") {
		t.Fatalf("missing endpoint: %v", err)
	}
}

func TestInstanceMetricsSummary(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/metrics", 200, map[string]interface{}{"instance_id": "24377", "timeframe": "day", "points": []interface{}{
		map[string]interface{}{"time": "2026-10-26T10:00:00Z", "cpu_pct": 10.0, "mem_bytes": 2e9, "mem_max_bytes": 4e9, "net_in_bps": 1000.0},
		map[string]interface{}{"time": "2026-10-26T11:00:00Z", "cpu_pct": 30.0, "mem_bytes": 3e9, "mem_max_bytes": 4e9, "net_in_bps": 3000.0},
	}})
	out, _, err := run(t, srv, t.TempDir(), "instances", "metrics", "24377", "--timeframe", "day")
	if err != nil {
		t.Fatal(err)
	}
	if url.Values(f.last("GET /v1/instances/24377/metrics").Query).Get("timeframe") != "day" {
		t.Error("timeframe not sent")
	}
	for _, want := range []string{"2 readings", "CPU", "20.0%", "30.0%", "Memory", "62.5%", "75.0%", "Network in", "2.0 KB/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics miss %q:\n%s", want, out)
		}
	}
}

func TestSnapshotsLifecycle(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/snapshots", 200, map[string]interface{}{"object": "list", "max_snapshots": 3, "remaining": 2, "data": []interface{}{
		map[string]interface{}{"id": "s1", "instance_id": "24377", "name": "before-upgrade", "include_ram": false, "created_at": "2026-10-25T10:00:00Z", "expires_at": "2026-11-24T10:00:00Z"},
	}})
	out, _, err := run(t, srv, t.TempDir(), "snapshots", "24377")
	if err != nil || !strings.Contains(out, "before-upgrade") || !strings.Contains(out, "2 of 3 slots free") {
		t.Fatalf("list = %q, %v", out, err)
	}
	f.answer("POST /v1/instances/24377/snapshots", 201, map[string]interface{}{"object": "snapshot", "id": "s2", "instance_id": "24377", "name": "nightly", "include_ram": true, "created_at": "2026-10-26T10:00:00Z", "expires_at": "2026-11-25T10:00:00Z"})
	out, _, err = run(t, srv, t.TempDir(), "snapshots", "create", "24377", "--name", "nightly", "--ram")
	if err != nil || !strings.Contains(out, "Snapshot nightly of web-1 (24377) taken") {
		t.Fatalf("create = %q, %v", out, err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "snapshots", "create", "24377", "--name", "1bad name"); err == nil {
		t.Error("a name the API refuses was sent")
	}
	if b := jsonOf(t, f.last("POST /v1/instances/24377/snapshots").Body); b["name"] != "nightly" || b["include_ram"] != true {
		t.Errorf("create body: %v", b)
	}
	f.answer("POST /v1/instances/24377/snapshots/s1/restore", 202, map[string]interface{}{"object": "operation_accepted", "action": "restore"})
	if _, _, err := runWithInput(t, srv, t.TempDir(), "before-upgrade\n", "snapshots", "restore", "24377", "before-upgrade"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	f.answer("DELETE /v1/instances/24377/snapshots/s1", 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "snapshots", "delete", "24377", "s1", "--yes"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestBackupsRunRestoreAndSchedule(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/status", 200, map[string]interface{}{"object": "instance_status", "power_state": "running", "busy": false})
	b1 := map[string]interface{}{"object": "backup", "id": "b1", "instance_id": "24377", "created_at": "2026-10-20T03:30:00Z"}
	b2 := map[string]interface{}{"object": "backup", "id": "b2", "instance_id": "24377", "created_at": "2026-10-26T12:00:00Z"}
	list := func(running bool, data ...interface{}) map[string]interface{} {
		if data == nil {
			data = []interface{}{}
		}
		return map[string]interface{}{"object": "list", "data": data, "eligible": true, "reason": nil, "slots": 2, "available_slots": 2 - len(data), "running": running}
	}

	// Without the add-on: why, and where the add-on is.
	f.answer("GET /v1/instances/24377/backups", 200, map[string]interface{}{"object": "list", "data": []interface{}{}, "eligible": false,
		"reason": "the backup add-on is missing", "slots": 0, "available_slots": 0, "running": false})
	out, _, err := run(t, srv, t.TempDir(), "backups", "24377")
	if err != nil || !strings.Contains(out, "cannot be backed up: the backup add-on is missing") || !strings.Contains(out, "resize-options 24377") {
		t.Fatalf("list = %q, %v", out, err)
	}

	// A run is answered before it starts: --wait follows the list until a
	// new backup is there.
	var polls int
	f.on("GET /v1/instances/24377/backups", func(w http.ResponseWriter, c call) {
		f.mu.Lock()
		polls++
		n := polls
		f.mu.Unlock()
		switch {
		case n == 1:
			reply(w, 200, list(false, b1)) // before the run
		case n < 4:
			reply(w, 200, list(true, b1))
		default:
			reply(w, 200, list(false, b2, b1))
		}
	})
	f.answer("POST /v1/instances/24377/backups", 202, map[string]interface{}{"object": "operation_accepted", "action": "backup", "resource_type": "instance", "resource_id": "24377", "operation_id": nil})
	out, _, err = run(t, srv, t.TempDir(), "backups", "run", "24377", "--wait")
	if err != nil || !strings.Contains(out, "taken: b2") {
		t.Fatalf("run --wait = %q, %v", out, err)
	}
	if c := f.last("POST /v1/instances/24377/backups"); c.Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key on the run")
	}

	// One that stops running without a new backup failed.
	var polls2 int
	f.on("GET /v1/instances/24377/backups", func(w http.ResponseWriter, c call) {
		f.mu.Lock()
		polls2++
		n := polls2
		f.mu.Unlock()
		reply(w, 200, list(n == 2, b1))
	})
	if _, _, err = run(t, srv, t.TempDir(), "backups", "run", "24377", "--wait"); err == nil || !strings.Contains(err.Error(), "may have failed") {
		t.Fatalf("a failed backup must fail the command: %v", err)
	}

	// Restore: the instance and "latest", its id typed, then the instance followed.
	f.answer("GET /v1/instances/24377/backups", 200, list(false, b2, b1))
	f.answer("POST /v1/backups/b2/restore", 202, map[string]interface{}{"object": "operation_accepted", "action": "backup_restore", "resource_type": "backup", "resource_id": "b2", "operation_id": nil})
	if _, _, err := runWithInput(t, srv, t.TempDir(), "b2\n", "backups", "restore", "24377", "latest"); err == nil {
		t.Fatal("restored without the instance id typed")
	}
	out, _, err = runWithInput(t, srv, t.TempDir(), "24377\n", "backups", "restore", "24377", "latest", "--wait")
	if err != nil || !strings.Contains(out, "done") || !f.called("GET /v1/instances/24377/status") {
		t.Fatalf("restore = %q, %v", out, err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "backups", "restore", "24377", "b9", "--yes"); err == nil || !strings.Contains(err.Error(), "no backup b9") {
		t.Errorf("an unknown backup: %v", err)
	}

	f.answer("DELETE /v1/backups/b1", 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "backups", "delete", "24377", "b1", "--yes"); err != nil {
		t.Fatal(err)
	}

	// The schedule: days of the week and a time of day.
	f.answer("PUT /v1/instances/24377/backup-schedule", 200, map[string]interface{}{"object": "backup_schedule", "id": "s1", "instance_id": "24377",
		"days": []string{"mon", "thu"}, "time": "03:30", "timezone": "America/Sao_Paulo", "last_run_at": nil})
	out, _, err = run(t, srv, t.TempDir(), "backups", "schedule", "set", "24377", "--days", "monday,thu", "--time", "03:30", "--timezone", "America/Sao_Paulo")
	if err != nil || !strings.Contains(out, "on mon, thu at 03:30 America/Sao_Paulo") {
		t.Fatalf("schedule set = %q, %v", out, err)
	}
	b := jsonOf(t, f.last("PUT /v1/instances/24377/backup-schedule").Body)
	if days := b["days"].([]interface{}); len(b) != 3 || len(days) != 2 || days[0] != "mon" || days[1] != "thu" || b["time"] != "03:30" || b["timezone"] != "America/Sao_Paulo" {
		t.Errorf("schedule body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "backups", "schedule", "set", "24377", "--days", "weekdays", "--time", "23:00"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("PUT /v1/instances/24377/backup-schedule").Body); len(b["days"].([]interface{})) != 5 || b["timezone"] != nil {
		t.Errorf("weekdays body: %v", b)
	}
	for _, bad := range [][]string{
		{"--days", "funday", "--time", "03:30"},
		{"--days", "mon", "--time", "3:30"},
		{"--days", "mon", "--time", "03:30", "--timezone", "São Paulo"},
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"backups", "schedule", "set", "24377"}, bad...)...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// No schedule is an answer, not an error.
	f.answer("GET /v1/instances/24377/backup-schedule", 404, map[string]interface{}{"status": 404, "code": "not_found", "title": "Not Found", "detail": "This instance has no backup schedule."})
	if out, _, err := run(t, srv, t.TempDir(), "backups", "schedule", "get", "24377"); err != nil || !strings.Contains(out, "has no automatic backups") {
		t.Errorf("schedule get = %q, %v", out, err)
	}
}

func TestOperationsWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	var n int32
	f.on("GET /v1/operations/op5", func(w http.ResponseWriter, _ call) {
		status := "in_progress"
		if atomic.AddInt32(&n, 1) > 2 {
			status = "completed"
		}
		reply(w, 200, map[string]interface{}{"id": "op5", "type": "firewall_commit", "status": status, "progress": 50, "created_at": "2026-10-26T10:00:00Z"})
	})
	out, errOut, err := run(t, srv, t.TempDir(), "operations", "wait", "op5")
	if err != nil || !strings.Contains(out, "completed") || !strings.Contains(errOut, "in_progress 50%") {
		t.Fatalf("wait = %q / %q, %v", out, errOut, err)
	}
}

func TestInstanceBandwidthAndPasswordReset(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/bandwidth", 200, map[string]interface{}{
		"object": "instance_bandwidth", "instance_id": "24377", "period": "current_cycle", "bucket": "day",
		"starts_at": "2026-10-01T00:00:00Z", "ends_at": "2026-10-31T23:59:59Z", "series": []interface{}{},
		"total_inbound_bytes": 12e9, "total_outbound_bytes": 250e9,
		"cycle": map[string]interface{}{"starts_at": "2026-10-01T00:00:00Z", "ends_at": "2026-10-31T23:59:59Z", "included_gb": 1000, "public_outbound_bytes": 250e9},
	})
	out, _, err := run(t, srv, t.TempDir(), "instances", "bandwidth", "24377")
	if err != nil || !strings.Contains(out, "250.0 GB of 1000 GB included (25%)") || !strings.Contains(out, "12.0 GB") {
		t.Fatalf("bandwidth = %q, %v", out, err)
	}
	if url.Values(f.last("GET /v1/instances/24377/bandwidth").Query).Get("period") != "current_cycle" {
		t.Error("period not sent")
	}
	f.answer("POST /v1/instances/24377/reset-password", 200, map[string]interface{}{"object": "password_reset", "instance_id": "24377", "username": "deploy", "password": "Xy9-generated"})
	out, _, err = run(t, srv, t.TempDir(), "instances", "reset-password", "24377", "--username", "deploy")
	if err != nil || !strings.Contains(out, "New password for deploy on web-1 (24377) (shown this once): Xy9-generated") {
		t.Fatalf("reset = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/instances/24377/reset-password").Body); b["username"] != "deploy" || b["password"] != nil {
		t.Errorf("reset body: %v", b)
	}
}

func TestFieldErrorsAreNamed(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.on("POST /v1/instances/24377/reset-password", func(w http.ResponseWriter, _ call) {
		reply(w, 400, map[string]interface{}{"status": 400, "code": "validation_failed", "detail": "One or more fields are invalid.", "request_id": "req_1",
			"errors": []interface{}{map[string]interface{}{"field": "password", "issues": []string{"password must be 8 characters or more"}}}})
	})
	_, _, err := run(t, srv, t.TempDir(), "instances", "reset-password", "24377", "--password", "short")
	if err == nil {
		t.Fatal("no error")
	}
	msg := Describe(err)
	if !strings.Contains(msg, "password: password must be 8 characters or more") || !strings.Contains(msg, "(request req_1)") {
		t.Fatalf("described: %q", msg)
	}
}
