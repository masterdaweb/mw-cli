package cli

import (
	"net/http"
	"strings"
	"testing"
)

func cloneCheckJSON(over map[string]interface{}) map[string]interface{} {
	c := map[string]interface{}{
		"object": "clone_check", "origin_instance_id": "24377", "destination_instance_id": "24378", "can_clone": true, "reason": nil,
		"origin_disk_gb": 160, "destination_disk_gb": 80,
		"disk_changes": map[string]interface{}{"main_disk_before_gb": 80, "main_disk_after_gb": 160, "extra_disks_before_gb": []float64{50}, "extra_disks_after_gb": []float64{}},
		"billing": map[string]interface{}{"commitment_type": "ON_DEMAND", "subscription_id": "sub-9", "current_hourly_rate_micro_cents": "20000",
			"new_hourly_rate_micro_cents": "26000", "charge_now_cents": "0", "credit_cents": "0", "shortfall_cents": nil},
		"warnings": []string{"The destination restarts."}, "violations": []interface{}{},
	}
	for k, v := range over {
		c[k] = v
	}
	return c
}

func cloneAPI(t *testing.T) (*fakeAPI, func(input string, args ...string) (string, error)) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24378", 200, instanceJSON("24378", "web-2", "running"))
	f.answer("POST /v1/instances/24377/clone/quote", 200, cloneCheckJSON(nil))
	f.answer("POST /v1/instances/24377/clone", 202, map[string]interface{}{"object": "operation_accepted", "action": "clone", "resource_type": "instance",
		"resource_id": "24378", "instance_id": "24378", "operation_id": "op-c"})
	f.answer("GET /v1/operations/op-c", 200, map[string]interface{}{"object": "operation", "id": "op-c", "type": "clone", "status": "completed", "created_at": "2026-10-26T12:00:00Z"})
	return f, func(input string, args ...string) (string, error) {
		out, _, err := runWithInput(t, srv, t.TempDir(), input, append([]string{"instances", "clone"}, args...)...)
		return out, err
	}
}

func TestInstanceCloneChecksConfirmsAndClones(t *testing.T) {
	f, clone := cloneAPI(t)
	// The quote first; typing the origin's id is not enough.
	out, err := clone("24377\n", "24377", "24378")
	if err == nil || f.called("POST /v1/instances/24377/clone") {
		t.Fatal("cloned without the destination's id typed")
	}
	for _, want := range []string{"web-1 (24377) → web-2 (24378), as it is now", "80 → 160 GB", "Data disks lost", "50 GB", "Destination price", "Note: The destination restarts."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if b := jsonOf(t, f.last("POST /v1/instances/24377/clone/quote").Body); len(b) != 2 || b["destination_instance_id"] != "24378" || b["source"] != "current" {
		t.Errorf("quote body: %v", b)
	}

	out, err = clone("24378\n", "24377", "24378", "--wait")
	if err != nil || !strings.Contains(out, "Clone of web-1 (24377) onto web-2 (24378): done.") || !f.called("GET /v1/operations/op-c") {
		t.Fatalf("clone = %q, %v", out, err)
	}
	c := f.last("POST /v1/instances/24377/clone")
	if b := jsonOf(t, c.Body); len(b) != 3 || b["confirm"] != "24378" || b["source"] != "current" {
		t.Errorf("clone body: %v", b)
	}
	if c.Header.Get("Idempotency-Key") == "" {
		t.Error("clone without an Idempotency-Key")
	}
	if _, err := clone("", "24377", "24377", "--yes"); err == nil || !strings.Contains(err.Error(), "same instance") {
		t.Errorf("onto itself: %v", err)
	}
}

func TestInstanceCloneFromABackupAndRefusals(t *testing.T) {
	f, clone := cloneAPI(t)
	f.answer("GET /v1/instances/24377/backups", 200, map[string]interface{}{"object": "list", "eligible": true, "slots": 2, "available_slots": 0, "running": false,
		"data": []interface{}{map[string]interface{}{"object": "backup", "id": "b2", "instance_id": "24377", "created_at": "2026-10-26T03:30:00Z"}}})
	if _, err := clone("", "24377", "24378", "--from-backup", "latest", "--yes"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("POST /v1/instances/24377/clone").Body); b["source"] != "backup" || b["backup_id"] != "b2" || b["confirm"] != "24378" {
		t.Errorf("from a backup: %v", b)
	}

	// Not allowed: why, and nothing cloned.
	f.on("POST /v1/instances/24377/clone", func(w http.ResponseWriter, c call) { t.Error("cloned what the quote refused"); reply(w, 500, nil) })
	f.answer("POST /v1/instances/24377/clone/quote", 200, cloneCheckJSON(map[string]interface{}{"can_clone": false, "reason": "the origin does not fit",
		"violations": []interface{}{map[string]interface{}{"code": "os_min_ram_mb", "message": "the destination has less memory than the system needs"}}}))
	if _, err := clone("", "24377", "24378", "--yes"); err == nil || !strings.Contains(err.Error(), "less memory than the system needs") {
		t.Errorf("violation: %v", err)
	}
	// Only the balance is short: the top-up.
	billing := cloneCheckJSON(nil)["billing"].(map[string]interface{})
	billing["shortfall_cents"] = "990"
	f.answer("POST /v1/instances/24377/clone/quote", 200, cloneCheckJSON(map[string]interface{}{"can_clone": false, "billing": billing}))
	if _, err := clone("", "24377", "24378", "--yes"); err == nil || !strings.Contains(err.Error(), "`mw subscriptions top-up sub-9 --amount 9,90`") {
		t.Errorf("shortfall: %v", err)
	}
}
