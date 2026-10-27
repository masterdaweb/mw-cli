package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func resizeOptionsJSON() map[string]interface{} {
	return map[string]interface{}{
		"object": "resize_options", "instance_id": "24377", "resizable": true, "reason": nil,
		"commitment_type": "ON_DEMAND", "term": nil, "cycle_ends_at": nil,
		"current": map[string]interface{}{"offering_code": "c2m4", "offering_on_sale": true, "vcpu": 2, "ram_mb": 4096, "main_disk_gb": 80,
			"extra_ipv4": 0, "extra_ipv6": 0, "hourly_rate_micro_cents": "20000", "rate_is_grandfathered": false},
		"capabilities": map[string]interface{}{"addons": true, "extra_ips": true, "storage": true},
		"offerings": []interface{}{
			map[string]interface{}{"code": "c2m4", "name": "Cloud M", "vcpu": 2, "ram_mb": 4096, "main_disk_base_gb": 80, "included_ipv4": 1, "included_ipv6": 1, "hourly_rate_micro_cents": "20000", "current": true},
			map[string]interface{}{"code": "c4m8", "name": "Cloud L", "vcpu": 4, "ram_mb": 8192, "main_disk_base_gb": 160, "included_ipv4": 1, "included_ipv6": 1, "hourly_rate_micro_cents": "40000", "current": false},
		},
		"addons": []interface{}{
			map[string]interface{}{"code": "backup-slot", "name": "Backup slots", "type": "backup", "current_quantity": 1, "min_quantity": 0, "max_quantity": 7, "quantity_step": 1, "quantity_follows_vcpu": false, "removable": true, "hourly_rate_micro_cents": "1000"},
			map[string]interface{}{"code": "windows", "name": "Windows Server", "type": "license", "current_quantity": 0, "min_quantity": 0, "max_quantity": nil, "quantity_step": 1, "quantity_follows_vcpu": true, "removable": true, "hourly_rate_micro_cents": "5000"},
		},
		"ip_assignments": []interface{}{map[string]interface{}{"assignment_id": "a1", "version": 4, "address": "203.0.113.10", "primary": true}},
		"extra_disks":    []interface{}{map[string]interface{}{"id": "d1", "label": "100 GB data"}},
		"limits":         map[string]interface{}{"max_ipv4": 4, "max_ipv6": 4, "can_add_ipv4": true, "can_add_ipv6": true},
		"currency":       "BRL",
	}
}

func resizeQuoteJSON(over map[string]interface{}) map[string]interface{} {
	q := map[string]interface{}{
		"object": "resize_quote", "instance_id": "24377", "has_changes": true, "can_apply": true,
		"current_hourly_rate_micro_cents": "20000", "new_hourly_rate_micro_cents": "40000", "new_cycle_cents": nil,
		"charge_now_cents": "0", "credit_cents": "0", "shortfall_cents": nil, "requires_restart": true,
		"removes": []interface{}{}, "violations": []interface{}{}, "warnings": []interface{}{}, "currency": "BRL",
	}
	for k, v := range over {
		q[k] = v
	}
	return q
}

func TestResizeOptionsTable(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/resize-options", 200, resizeOptionsJSON())
	out, _, err := run(t, srv, t.TempDir(), "instances", "resize-options", "24377")
	if err != nil {
		t.Fatal(err)
	}
	big := "40000"
	for _, want := range []string{
		"plan c2m4", "c4m8", "Cloud L", "8 GB", "160 GB", "← current",
		strings.TrimSuffix(perHour(&big), "/h"), strings.TrimSuffix(perMonth(&big), "/mo"), // the 672 h billing month
		"backup-slot", "0–7", "windows", "on or off", "per vCPU", "Extra disks: 100 GB data.",
		"mw instances resize 24377 --plan <code>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if out, _, err := run(t, srv, t.TempDir(), "instances", "resize-options", "24377", "-q"); err != nil || out != "c2m4\nc4m8\n" {
		t.Errorf("-q = %q, %v", out, err)
	}
	if out, _, err := run(t, srv, t.TempDir(), "instances", "resize-options", "24377", "-o", "json"); err != nil || !strings.Contains(out, `"offerings"`) {
		t.Errorf("-o json = %q, %v", out, err)
	}
}

func TestResizeQuotesConfirmsAndApplies(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/resize-options", 200, resizeOptionsJSON())
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"charge_now_cents": "450"}))
	f.answer("POST /v1/instances/24377/resize", 202, map[string]interface{}{
		"object": "operation_accepted", "action": "resize", "resource_type": "instance", "resource_id": "24377", "operation_id": "op-r",
		"new_hourly_rate_micro_cents": "40000", "credit_cents": "0",
		"planned": map[string]interface{}{"requires_restart": true, "main_disk_grow_gb": 80, "disks_added": 0, "disks_removed": 0,
			"ipv4_added": 0, "ipv4_removed": 0, "ipv6_added": 0, "ipv6_removed": 0, "reboot_required": false},
	})
	f.answer("GET /v1/operations/op-r", 200, map[string]interface{}{"object": "operation", "id": "op-r", "type": "resize", "status": "completed", "created_at": "2026-10-26T12:00:00Z"})

	// The quote first; saying no changes nothing.
	out, _, err := runWithInput(t, srv, t.TempDir(), "n\n", "instances", "resize", "24377", "--plan", "c4m8")
	if err != nil || !strings.Contains(out, "c2m4 → c4m8 (4 vCPU · 8 GB RAM · 160 GB disk)") || !strings.Contains(out, "Charged now") ||
		!strings.Contains(out, "it powers off and on again") {
		t.Fatalf("quote = %q, %v", out, err)
	}
	if f.called("POST /v1/instances/24377/resize") {
		t.Fatal("resized after a no")
	}

	out, _, err = run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8", "--yes", "--wait")
	if err != nil || !strings.Contains(out, "to c4m8: done.") || !strings.Contains(out, "grows by 80 GB") || !strings.Contains(out, "New price") {
		t.Fatalf("resize = %q, %v", out, err)
	}
	quote, apply := f.last("POST /v1/instances/24377/resize/quote"), f.last("POST /v1/instances/24377/resize")
	// Only the plan: the API refuses a field it does not declare.
	if b := jsonOf(t, quote.Body); len(b) != 1 || b["offering_code"] != "c4m8" {
		t.Errorf("quote body: %v", b)
	}
	if string(apply.Body) != string(quote.Body) || apply.Header.Get("Idempotency-Key") == "" {
		t.Errorf("resize call: %s %v", apply.Body, apply.Header)
	}
	if !f.called("GET /v1/operations/op-r") {
		t.Error("--wait did not follow the operation")
	}
}

func TestResizeExtrasKeepThePlan(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/resize-options", 200, resizeOptionsJSON())
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"new_hourly_rate_micro_cents": "26000"}))
	f.answer("POST /v1/instances/24377/resize", 202, map[string]interface{}{"object": "operation_accepted", "action": "resize", "resource_type": "instance", "resource_id": "24377", "operation_id": nil, "credit_cents": "0", "planned": nil})
	out, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--disk-extra-gb", "50", "--addon", "backup-slot=2", "--addon", "Windows=on", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"c2m4 (unchanged)", "main disk 80 → 130 GB", "backup-slot 1 → 2", "windows off → on"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	b := jsonOf(t, f.last("POST /v1/instances/24377/resize").Body)
	addons, _ := b["addons"].([]interface{})
	if len(b) != 3 || b["offering_code"] != "c2m4" || b["main_disk_extra_gb"] != float64(50) || len(addons) != 2 {
		t.Fatalf("body: %v", b)
	}
	// A per-vCPU add-on is switched on; the API sets its quantity from the plan.
	if a0, a1 := addons[0].(map[string]interface{}), addons[1].(map[string]interface{}); a0["code"] != "backup-slot" || a0["quantity"] != float64(2) ||
		a1["code"] != "windows" || a1["quantity"] != float64(1) {
		t.Errorf("addons: %v", addons)
	}
}

func TestResizeRefusals(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/resize-options", 200, resizeOptionsJSON())

	// What it cannot be is refused before anything is priced.
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "pass --plan"},
		{[]string{"--plan", "c64m256"}, "c2m4, c4m8"},
		{[]string{"--addon", "gpu=1"}, "backup-slot, windows"},
		{[]string{"--addon", "windows=2"}, "on or off"},
		{[]string{"--addon", "backup-slot"}, "code=quantity"},
		{[]string{"--disk-extra-gb", "-5"}, "negative"},
	} {
		_, _, err := run(t, srv, t.TempDir(), append([]string{"instances", "resize", "24377"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	if f.called("POST /v1/instances/24377/resize/quote") {
		t.Fatal("priced a resize that was refused here")
	}

	// Not allowed: why.
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"can_apply": false,
		"violations": []interface{}{map[string]interface{}{"code": "disk_shrink", "message": "the main disk cannot shrink"}}}))
	if _, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8", "--yes"); err == nil || !strings.Contains(err.Error(), "the main disk cannot shrink") {
		t.Errorf("violation: %v", err)
	}

	// Only the balance is short: the top-up that fixes it, on the subscription
	// the quote names — or, from an API whose quote does not, the instance's.
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"can_apply": false, "shortfall_cents": "1234", "subscription_id": "sub-9"}))
	if _, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "`mw subscriptions top-up sub-9 --amount 12,34`") {
		t.Errorf("shortfall: %v", err)
	}
	billed := instanceJSON("24377", "web-1", "running")
	billed["subscription_id"] = "sub-7"
	f.answer("GET /v1/instances/24377", 200, billed)
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"can_apply": false, "shortfall_cents": "1234"}))
	if _, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "`mw subscriptions top-up sub-7 --amount 12,34`") {
		t.Errorf("shortfall, the instance's subscription: %v", err)
	}
	if f.called("GET /v1/instances/24377/removal-preview") {
		t.Error("read the removal preview for the subscription")
	}

	// Nothing to change.
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{"has_changes": false}))
	if out, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c2m4", "--yes"); err != nil || !strings.Contains(out, "Nothing to change") {
		t.Errorf("no change = %q, %v", out, err)
	}
	if f.called("POST /v1/instances/24377/resize") {
		t.Fatal("applied a resize the quote refused")
	}

	// The balance ran out between the quote and the resize: 402.
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(nil))
	f.on("POST /v1/instances/24377/resize", func(w http.ResponseWriter, c call) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(402)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"type": "https://api.masterdaweb.com/v1/errors/insufficient_balance", "title": "Insufficient balance",
			"status": 402, "code": "insufficient_balance", "detail": "The subscription’s balance does not cover this resize.",
			"subscription_id": "sub-9", "shortfall_cents": "500", "currency": "BRL", "next": map[string]interface{}{"top_up": "POST /v1/subscriptions/sub-9/top-ups"}})
	})
	if _, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "short by") || !strings.Contains(err.Error(), "`mw subscriptions top-up sub-9 --amount 5,00`") {
		t.Errorf("402: %v", err)
	}

	// Not resizable now: the reason.
	opts := resizeOptionsJSON()
	opts["resizable"], opts["reason"] = false, "a backup is running"
	f.answer("GET /v1/instances/24377/resize-options", 200, opts)
	if _, _, err := run(t, srv, t.TempDir(), "instances", "resize", "24377", "--plan", "c4m8"); err == nil || !strings.Contains(err.Error(), "a backup is running") {
		t.Errorf("not resizable: %v", err)
	}
}

// Losing an address or a disk is not undone: the instance id is typed.
func TestResizeThatRemovesSomethingNeedsTheIDTyped(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/resize-options", 200, resizeOptionsJSON())
	f.answer("POST /v1/instances/24377/resize/quote", 200, resizeQuoteJSON(map[string]interface{}{
		"removes": []interface{}{map[string]interface{}{"type": "ipv4", "label": "203.0.113.11", "warning": "the address is released for good"}}}))
	f.answer("POST /v1/instances/24377/resize", 202, map[string]interface{}{"object": "operation_accepted", "action": "resize", "resource_type": "instance", "resource_id": "24377", "operation_id": "op-r", "credit_cents": "0"})
	if _, _, err := runWithInput(t, srv, t.TempDir(), "y\n", "instances", "resize", "24377", "--plan", "c4m8"); err == nil {
		t.Fatal("a y was enough to remove an address")
	}
	out, _, err := runWithInput(t, srv, t.TempDir(), "24377\n", "instances", "resize", "24377", "--plan", "c4m8")
	if err != nil || !strings.Contains(out, "Removes IPv4 address 203.0.113.11: the address is released for good") {
		t.Fatalf("resize = %q, %v", out, err)
	}
}

func TestInstanceDeleteSaysWhatWouldBlockIt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/removal-preview", 200, map[string]interface{}{"object": "removal_preview", "instance_id": "24377",
		"summary": "Its billing stops and R$ 3,10 comes back as credit.", "billing": "committed", "subscription_id": "sub-9",
		"hourly_rate_micro_cents": "20000", "credit": map[string]interface{}{"amount_cents": "310", "expires_at": nil, "none_reason": nil},
		"usable_after_cents": "310", "last_service": false, "blockers": []string{"pending_conversion"}, "currency": "BRL"})
	out, _, err := runWithInput(t, srv, t.TempDir(), "no\n", "instances", "delete", "24377")
	if err == nil {
		t.Fatal("deleted without the id typed")
	}
	if !strings.Contains(out, "Its billing stops and R$ 3,10 comes back as credit.") ||
		!strings.Contains(out, "The deletion will be refused now: a subscription conversion is pending.") {
		t.Errorf("before asking: %q", out)
	}
	if f.called("DELETE /v1/instances/24377") {
		t.Error("deleted")
	}
}

// A busy instance's 409 says what is running and how to wait; one in rescue
// says how to leave it.
func TestBusyOrRescuedInstanceSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	for reason, want := range map[string]string{
		"VM_OPERATION_IN_PROGRESS": "Another operation is running on this instance: `mw operations --resource-type instance --resource-id 24377 --status active` shows it",
		"INSTANCE_IN_RESCUE":       "`mw instances rescue exit 24377`",
	} {
		reason := reason
		f.on("POST /v1/instances/24377/power", func(w http.ResponseWriter, _ call) {
			reply(w, 409, map[string]interface{}{"status": 409, "code": "conflict", "reason": reason, "instance": "/v1/instances/24377/power",
				"detail": "The instance is busy.", "request_id": "req_9"})
		})
		_, _, err := run(t, srv, t.TempDir(), "instances", "reboot", "24377")
		if err == nil {
			t.Fatalf("%s: no error", reason)
		}
		if msg := Describe(err); !strings.Contains(msg, want) || !strings.Contains(msg, "(request req_9)") {
			t.Errorf("%s described: %q", reason, msg)
		}
	}
}

// A backup restored by its id alone: it names its instance and date before
// asking (the instance's id is typed), and the 202's instance is followed.
func TestBackupRestoreByItsIDAlone(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/backups/b2", 200, map[string]interface{}{"object": "backup", "id": "b2", "instance_id": "24377", "created_at": "2026-10-26T12:00:00Z"})
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("POST /v1/backups/b2/restore", 202, map[string]interface{}{"object": "operation_accepted", "action": "backup_restore",
		"resource_type": "backup", "resource_id": "b2", "instance_id": "24377", "operation_id": nil})
	f.answer("GET /v1/instances/24377/status", 200, map[string]interface{}{"object": "instance_status", "power_state": "running", "busy": false})
	out, _, err := runWithInput(t, srv, t.TempDir(), "b2\n", "backups", "restore", "b2")
	if err == nil || !strings.Contains(out, "Overwrite web-1 (24377) with its backup of") || f.called("POST /v1/backups/b2/restore") {
		t.Fatalf("restored without the instance id typed: %q, %v", out, err)
	}
	out, _, err = runWithInput(t, srv, t.TempDir(), "24377\n", "backups", "restore", "b2", "--wait")
	if err != nil || !strings.Contains(out, "Restore of web-1 (24377) to its backup of") || !strings.Contains(out, "done.") || !f.called("GET /v1/instances/24377/status") {
		t.Fatalf("restore = %q, %v", out, err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "backups", "restore", "latest", "--yes"); err == nil || !strings.Contains(err.Error(), "needs the instance") {
		t.Errorf("latest alone: %v", err)
	}
	f.answer("DELETE /v1/backups/b2", 204, nil)
	if out, _, err := runWithInput(t, srv, t.TempDir(), "y\n", "backups", "delete", "b2"); err != nil || !strings.Contains(out, "the backup of web-1 (24377) taken") || !strings.Contains(out, "Backup b2 deleted.") {
		t.Errorf("delete = %q, %v", out, err)
	}

	// An API that cannot read one backup: the backup's id is typed instead.
	f.on("GET /v1/backups/b2", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
	if out, _, err := runWithInput(t, srv, t.TempDir(), "b2\n", "backups", "restore", "b2"); err != nil || !strings.Contains(out, "Type the backup id (b2)") || !strings.Contains(out, "Restore of instance 24377 to backup b2") {
		t.Errorf("older API: %q, %v", out, err)
	}
}

func TestInstanceGetShowsImageProjectAndSubscription(t *testing.T) {
	f, srv := newFakeAPI(t)
	const pid = "0193f0e2-2222-7000-8000-000000000001"
	in := instanceJSON("24377", "web-1", "running")
	in["image"], in["project_id"], in["subscription_id"] = "ubuntu-24-04", pid, "sub-9"
	f.answer("GET /v1/instances/24377", 200, in)
	f.answer("GET /v1/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	out, _, err := run(t, srv, t.TempDir(), "instances", "get", "24377")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ubuntu-24-04", "loja (" + pid + ")", "sub-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Built from a custom image: that, by id.
	in["image"], in["custom_image_id"], in["project_id"] = nil, "ci-4", nil
	f.answer("GET /v1/instances/24377", 200, in)
	if out, _, err := run(t, srv, t.TempDir(), "instances", "get", "24377"); err != nil || !strings.Contains(out, "custom image ci-4") {
		t.Errorf("custom image = %q, %v", out, err)
	}
}

func TestWebhookGet(t *testing.T) {
	f, srv := newFakeAPI(t)
	const id = "0193f0e2-5555-7000-8000-000000000001"
	hook := map[string]interface{}{"id": id, "kind": "SLACK", "url": "https://hooks.slack.com/…XXXX", "description": "deploys", "events": []string{"deployment.failed"},
		"enabled": false, "disabled_reason": "failing", "consecutive_failures": 12, "last_success_at": nil, "last_failure_at": "2026-10-26T10:00:00Z", "created_at": "2026-10-01T00:00:00Z"}
	f.answer("GET /v1/webhooks/"+id, 200, hook)
	out, _, err := run(t, srv, t.TempDir(), "webhooks", "get", id)
	if err != nil || !strings.Contains(out, "off: it kept failing") || !strings.Contains(out, "deployment.failed") || !strings.Contains(out, "Last failure") {
		t.Fatalf("get = %q, %v", out, err)
	}
	if f.called("GET /v1/webhooks") {
		t.Error("listed every webhook to read one by its id")
	}
	// By the start of its id: from the list.
	f.answer("GET /v1/webhooks", 200, []interface{}{hook})
	if out, _, err := run(t, srv, t.TempDir(), "webhooks", "get", "0193f0e2-5555"); err != nil || !strings.Contains(out, id) {
		t.Errorf("by prefix = %q, %v", out, err)
	}
}
