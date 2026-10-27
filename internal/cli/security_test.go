package cli

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
)

const findingID = "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e10"
const exceptionID = "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e20"

func counts(c, h, m, l, i int) map[string]interface{} {
	return map[string]interface{}{"critical": c, "high": h, "medium": m, "low": l, "info": i}
}

func findingJSON(id, severity string, exploited bool) map[string]interface{} {
	return map[string]interface{}{
		"object": "security_finding", "id": id,
		"resource": map[string]interface{}{"type": "instance", "id": "24377", "name": "web-1"},
		"rule_id":  "exposed-mysql", "category": "EXPOSED_SERVICE", "severity": severity, "status": "open",
		"risk_score": 81, "known_exploited": exploited, "cve_ids": []string{}, "port": 3306, "protocol": "tcp",
		"title": "MySQL is open to the internet", "description": "Port 3306 answers from anywhere.",
		"impact": "Anyone can try passwords.", "recommendation": "Allow only your addresses.",
		"first_seen_at": "2026-10-20T12:00:00Z", "last_seen_at": "2026-10-26T12:00:00Z",
		"last_verified_at": "2026-10-26T12:00:00Z", "resolved_at": nil, "occurrence_count": 3,
	}
}

func TestSecurityOverviewShowsTheScoreWhatLowersItAndTheWorst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/security/overview", 200, map[string]interface{}{
		"object": "security_overview", "score": 72, "grade": "C", "last_scan_at": "2026-10-26T12:00:00Z",
		"scanning_available": false, "open_findings": counts(0, 2, 1, 0, 4),
		"engines": []interface{}{
			map[string]interface{}{"engine": "posture", "score": 80, "grade": "B", "last_scan_at": "2026-10-26T12:00:00Z", "open_findings": counts(0, 2, 0, 0, 0)},
			map[string]interface{}{"engine": "image", "score": nil, "grade": nil, "last_scan_at": nil, "open_findings": counts(0, 0, 0, 0, 0)},
		},
		"instances":       map[string]interface{}{"total": 3, "assessed": 1, "awaiting_first_scan": 1, "excluded": 1, "opted_out": 0, "excluded_reasons": map[string]int{"no_public_ip": 1}},
		"resources":       []interface{}{map[string]interface{}{"type": "bucket", "total": 2, "checked": 2, "image_scans": nil}},
		"worst_instances": []interface{}{map[string]interface{}{"type": "instance", "id": "24377", "name": "web-1", "score": 60, "grade": "C", "open_critical": 0, "open_high": 2}},
		"worst_resources": []interface{}{map[string]interface{}{"type": "bucket", "id": "b-1", "name": "assets", "score": 50, "grade": "D", "open_critical": 1, "open_high": 0}},
	})
	out, _, err := run(t, srv, t.TempDir(), "security")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"72/100 (C)", "0 critical · 2 high · 1 medium · 0 low · 4 info", "3: 1 assessed, 1 awaiting their first scan, 1 not scanned (no public ip: 1)",
		"Buckets", "configuration", "80/100 (B)", "container images", "not run yet", "instance web-1 (24377)", "bucket assets", "Scanning is paused", "`mw security findings`"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview misses %q:\n%s", want, out)
		}
	}
	out, _, err = run(t, srv, t.TempDir(), "security", "overview", "-o", "json")
	if err != nil || jsonOf(t, []byte(out))["score"] != float64(72) {
		t.Fatalf("json overview: %q, %v", out, err)
	}
}

func TestSecurityFindingsFilterAndComeRiskiestFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances", 200, page(instanceJSON("24377", "web-1", "running")))
	f.on("GET /v1/security/findings", func(w http.ResponseWriter, c call) {
		if c.Query["cursor"] == nil {
			reply(w, 200, map[string]interface{}{"object": "list", "data": []interface{}{findingJSON(findingID, "high", true), findingJSON("0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e11", "high", false)}, "has_more": true, "next_cursor": "Mg"})
			return
		}
		reply(w, 200, map[string]interface{}{"object": "list", "data": []interface{}{findingJSON("0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e12", "medium", false)}, "has_more": false, "next_cursor": nil})
	})
	out, _, err := run(t, srv, t.TempDir(), "security", "findings", "--severity", "HIGH", "--instance", "web-1", "--status", "accepted", "--category", "exposed-service")
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values(f.last("GET /v1/security/findings").Query)
	if q.Get("severity") != "high" || q.Get("instance_id") != "24377" || q.Get("status") != "excepted" || q.Get("category") != "EXPOSED_SERVICE" {
		t.Errorf("query %v", q)
	}
	for _, want := range []string{"high (exploited)", "instance web-1 (24377)", "MySQL is open to the internet", "81", "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e12"} {
		if !strings.Contains(out, want) {
			t.Errorf("findings miss %q:\n%s", want, out)
		}
	}
	// --limit stops reading pages once it has enough.
	before := len(f.calls)
	out, _, err = run(t, srv, t.TempDir(), "security", "findings", "--limit", "2", "-q")
	if err != nil || strings.Count(out, "\n") != 2 || len(f.calls)-before != 1 {
		t.Fatalf("limit 2: %q, %v, %d call(s)", out, err, len(f.calls)-before)
	}
	// What the API would refuse is refused before asking it.
	before = len(f.calls)
	if _, _, err := run(t, srv, t.TempDir(), "security", "findings", "--resource-id", "b-1"); err == nil || !strings.Contains(err.Error(), "--resource-type") {
		t.Fatalf("resource id alone: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "security", "findings", "--severity", "urgent"); err == nil || !strings.Contains(err.Error(), "critical, high") {
		t.Fatalf("bad severity: %v", err)
	}
	if len(f.calls) != before {
		t.Fatal("a refused filter reached the API")
	}
}

func TestSecurityFindingSaysWhyAndWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	detail := findingJSON(findingID, "critical", true)
	detail["cve_ids"] = []string{"CVE-2024-6387"}
	detail["references"] = []string{"https://example.com/advice"}
	detail["events"] = []interface{}{map[string]interface{}{"type": "severity_increased", "occurred_at": "2026-10-25T12:00:00Z"}}
	f.answer("GET /v1/security/findings/"+findingID, 200, detail)
	out, _, err := run(t, srv, t.TempDir(), "security", "finding", strings.ToUpper(findingID))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"actively exploited in the wild (CISA KEV)", "TCP 3306", "CVE-2024-6387", "Why it matters", "Anyone can try passwords.",
		"What to do", "Allow only your addresses.", "https://example.com/advice", "severity increased", "Seen", "3 times", "mw security accept-risk --finding " + findingID} {
		if !strings.Contains(out, want) {
			t.Errorf("finding misses %q:\n%s", want, out)
		}
	}
	// The same through `findings get`.
	if out2, _, err := run(t, srv, t.TempDir(), "security", "findings", "get", findingID); err != nil || out2 != out {
		t.Fatalf("findings get: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "security", "finding", "42"); err == nil || !strings.Contains(err.Error(), "not a finding id") {
		t.Fatalf("not an id: %v", err)
	}
}

func TestAcceptRiskSendsOneScopeAJustificationAndAnEnd(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances", 200, page(instanceJSON("24377", "db-1", "running")))
	// The platform keeps it shorter than asked: 30 days.
	capped := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	f.answer("POST /v1/security/exceptions", 201, map[string]interface{}{
		"object": "security_exception", "id": exceptionID, "scope": "port", "status": "active", "rule_id": nil,
		"port": 53, "protocol": "udp", "instance_id": "24377", "resource": nil,
		"justification": "A resolver for the office only, behind its own ACL.", "starts_at": "2026-10-26T12:00:00Z",
		"expires_at": capped, "revoked_at": nil,
	})
	why := "A resolver for the office only, behind its own ACL."
	out, _, err := run(t, srv, t.TempDir(), "security", "accept-risk", "--port", "53/udp", "--instance", "db-1", "--justification", why, "--until", "90d")
	if err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("POST /v1/security/exceptions").Body)
	if b["scope"] != "port" || b["port"] != float64(53) || b["protocol"] != "udp" || b["instance_id"] != "24377" || b["justification"] != why {
		t.Errorf("body %v", b)
	}
	end, err := time.Parse(time.RFC3339, b["expires_at"].(string))
	if err != nil || end.Sub(time.Now()) < 89*24*time.Hour || end.Sub(time.Now()) > 91*24*time.Hour {
		t.Errorf("expires_at %v, %v", b["expires_at"], err)
	}
	if f.last("POST /v1/security/exceptions").Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key")
	}
	for _, want := range []string{"Risk accepted until", "UDP 53 no longer lowers the score", "The platform's maximum applies", "mw security revoke " + exceptionID} {
		if !strings.Contains(out, want) {
			t.Errorf("accept-risk misses %q:\n%s", want, out)
		}
	}

	before := len(f.calls)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--port", "53", "--rule", "x", "--justification", why, "--until", "9d"}, "exactly one of"},
		{[]string{"--port", "53", "--justification", "because", "--until", "9d"}, "30 to 2000 characters"},
		{[]string{"--port", "53", "--justification", why}, "--until says when"},
		{[]string{"--port", "70000", "--justification", why, "--until", "9d"}, "1 to 65535"},
		{[]string{"--finding", findingID, "--instance", "db-1", "--justification", why, "--until", "9d"}, "--instance goes with --rule or --port"},
		{[]string{"--rule", "exposed-mysql", "--justification", why, "--until", "2020-01-01"}, "in the past"},
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"security", "accept-risk"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (want %q)", c.args, err, c.want)
		}
	}
	for _, c := range f.calls[before:] {
		if c.Method == "POST" {
			t.Fatal("a refused request reached the API")
		}
	}
}

func TestSecurityExceptionsListAndRevoke(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/security/exceptions", 200, map[string]interface{}{"object": "list", "data": []interface{}{
		map[string]interface{}{"object": "security_exception", "id": exceptionID, "scope": "rule", "status": "active", "rule_id": "exposed-mysql",
			"port": nil, "protocol": nil, "instance_id": "24377", "resource": nil, "justification": "Replica for the vendor.",
			"starts_at": "2026-10-26T12:00:00Z", "expires_at": "2026-12-01T00:00:00Z", "revoked_at": nil},
	}})
	out, _, err := run(t, srv, t.TempDir(), "security", "exceptions", "--all")
	if err != nil || !strings.Contains(out, "check exposed-mysql") || !strings.Contains(out, "Replica for the vendor.") {
		t.Fatalf("exceptions: %q, %v", out, err)
	}
	if url.Values(f.last("GET /v1/security/exceptions").Query).Get("include_inactive") != "true" {
		t.Error("--all did not ask for the inactive ones")
	}
	f.answer("DELETE /v1/security/exceptions/"+exceptionID, 204, nil)
	out, _, err = run(t, srv, t.TempDir(), "security", "revoke", exceptionID)
	if err != nil || !strings.Contains(out, "count against the score again") {
		t.Fatalf("revoke: %q, %v", out, err)
	}
	if f.last("DELETE /v1/security/exceptions/"+exceptionID).Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key on the revoke")
	}
}

func TestSecuritySettingsSetSendsOnlyWhatChanged(t *testing.T) {
	f, srv := newFakeAPI(t)
	settings := map[string]interface{}{"object": "security_settings", "enabled": true, "default_frequency": "monthly", "notify_on_new_critical": true, "notify_on_score_drop": false}
	f.answer("GET /v1/security/settings", 200, settings)
	f.answer("PATCH /v1/security/settings", 200, settings)
	out, _, err := run(t, srv, t.TempDir(), "security", "settings")
	if err != nil || !strings.Contains(out, "monthly") || !strings.Contains(out, "Email when the score drops") {
		t.Fatalf("settings: %q, %v", out, err)
	}
	out, _, err = run(t, srv, t.TempDir(), "security", "settings", "set", "--frequency", "Monthly", "--notify-score-drop", "off")
	if err != nil || !strings.Contains(out, "Every instance is now checked monthly.") {
		t.Fatalf("set: %q, %v", out, err)
	}
	b := jsonOf(t, f.last("PATCH /v1/security/settings").Body)
	if len(b) != 2 || b["default_frequency"] != "monthly" || b["notify_on_score_drop"] != false {
		t.Errorf("body %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "security", "settings", "set"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("nothing: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "security", "settings", "set", "--scanning", "maybe"); err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Fatalf("maybe: %v", err)
	}
}

func instanceSecurityJSON(state string) map[string]interface{} {
	return map[string]interface{}{
		"object": "instance_security", "instance_id": "24377", "state": state, "excluded_reason": nil,
		"score": 64, "grade": "C", "last_scan_at": "2026-10-26T12:00:00Z", "next_scan_due_at": "2026-11-02T12:00:00Z",
		"schedule_frequency": "weekly", "open_findings": counts(0, 1, 0, 0, 0), "engines": []interface{}{}, "scanning_available": true,
	}
}

func TestInstanceSecurityReadsAndTurnsScanningOff(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/security", 200, instanceSecurityJSON("assessed"))
	f.answer("PATCH /v1/instances/24377/security", 200, instanceSecurityJSON("opted_out"))
	out, _, err := run(t, srv, t.TempDir(), "instances", "security", "24377")
	if err != nil || !strings.Contains(out, "64/100 (C)") || !strings.Contains(out, "mw security findings --instance 24377") {
		t.Fatalf("security: %q, %v", out, err)
	}
	out, _, err = run(t, srv, t.TempDir(), "instances", "security", "24377", "--disable", "--frequency", "monthly")
	if err != nil || !strings.Contains(out, "--enable` turns it back on") {
		t.Fatalf("disable: %q, %v", out, err)
	}
	b := jsonOf(t, f.last("PATCH /v1/instances/24377/security").Body)
	if b["enabled"] != false || b["schedule_frequency"] != "monthly" {
		t.Errorf("body %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "instances", "security", "24377", "--enable", "--disable"); err == nil {
		t.Fatal("--enable and --disable together")
	}
}

func scanJSON(status string) map[string]interface{} {
	return map[string]interface{}{
		"object": "security_scan", "id": "scan-1", "instance_id": "24377", "status": status, "trigger": "on_demand",
		"queued_at": "2026-10-26T12:00:00Z", "started_at": nil, "finished_at": nil, "score": 80, "grade": "B",
		"findings_new": 1, "findings_resolved": 2, "findings_reopened": 0,
	}
}

func TestInstanceScanWaitsUntilItEnds(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("POST /v1/instances/24377/security/scans", 202, scanJSON("queued"))
	polls := 0
	f.on("GET /v1/instances/24377/security/scans", func(w http.ResponseWriter, _ call) {
		polls++
		status := map[int]string{1: "queued", 2: "running"}[polls]
		if status == "" {
			status = "completed"
		}
		// An older scan first would be the API's newest-first order gone wrong: the id decides.
		reply(w, 200, map[string]interface{}{"object": "list", "data": []interface{}{scanJSON(status)}})
	})
	out, errOut, err := run(t, srv, t.TempDir(), "instances", "scan", "24377", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Scan of web-1 (24377) done: score 80/100 (B); 1 new finding, 2 resolved, 0 reopened.") || !strings.Contains(errOut, "scan scan-1: running") {
		t.Fatalf("scan: %q / %q", out, errOut)
	}
	if f.last("POST /v1/instances/24377/security/scans").Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key on the scan")
	}

	// A refusal says what to do next.
	f.on("POST /v1/instances/24377/security/scans", func(w http.ResponseWriter, _ call) {
		reply(w, 409, map[string]interface{}{"status": 409, "code": "conflict", "reason": "INSTANCE_NOT_RUNNING",
			"detail": "The instance must be running to be scanned.", "instance": "/v1/instances/24377/security/scans"})
	})
	_, _, err = run(t, srv, t.TempDir(), "instances", "scan", "24377")
	if err == nil || !strings.Contains(Describe(err), "mw instances start 24377 --wait") {
		t.Fatalf("not running: %v", err)
	}
}

func TestScanRefusalsAndRebuildsSayWhatToDo(t *testing.T) {
	for reason, want := range map[string]string{
		"SECURITY_ACCOUNT_OPTED_OUT":   "mw security settings set --scanning on",
		"SECURITY_INSTANCE_OPTED_OUT":  "mw instances security 24377 --enable",
		"VPN_GATEWAY_REBUILD_REQUIRED": "--rebuild",
	} {
		msg := Describe(&api.Problem{Status: 403, Code: "forbidden", Reason: reason, Detail: "Refused.", Instance: "/v1/instances/24377/security/scans"})
		if !strings.Contains(msg, want) {
			t.Errorf("%s: %s", reason, msg)
		}
	}
}

func TestInstanceScansListsThemNewestFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	// An API that answers the bare array reads the same.
	f.answer("GET /v1/instances/24377/security/scans", 200, []interface{}{scanJSON("completed")})
	out, _, err := run(t, srv, t.TempDir(), "instances", "scans", "24377", "--limit", "5")
	if err != nil || !strings.Contains(out, "scan-1") || !strings.Contains(out, "on-demand") {
		t.Fatalf("scans: %q, %v", out, err)
	}
	if url.Values(f.last("GET /v1/instances/24377/security/scans").Query).Get("limit") != "5" {
		t.Error("--limit not sent")
	}
}

func TestInstanceISOAttachAndDetach(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	iso := map[string]interface{}{"id": "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e30", "name": "alpine-3.20", "filename": "alpine.iso", "kind": "iso", "status": "ready", "instance_count": 0, "created_at": "2026-10-20T12:00:00Z"}
	disk := map[string]interface{}{"id": "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e31", "name": "debian-12", "filename": "debian.qcow2", "kind": "disk_image", "status": "ready", "instance_count": 0, "created_at": "2026-10-20T12:00:00Z"}
	f.answer("GET /v1/images", 200, map[string]interface{}{"object": "list", "data": []interface{}{iso, disk}})
	attached := map[string]interface{}{"object": "iso_attachment", "instance_id": "24377", "attached": true, "image_id": iso["id"], "image_name": "alpine-3.20",
		"attached_at": "2026-10-26T12:00:00Z", "ejects_at": "2026-10-27T12:00:00Z"}
	f.answer("POST /v1/instances/24377/iso", 200, attached)
	out, _, err := run(t, srv, t.TempDir(), "instances", "iso", "attach", "24377", "alpine-3.20")
	if err != nil || !strings.Contains(out, "reboot it to boot from the ISO") || !strings.Contains(out, "ejected on its own") {
		t.Fatalf("attach: %q, %v", out, err)
	}
	if jsonOf(t, f.last("POST /v1/instances/24377/iso").Body)["image_id"] != iso["id"] {
		t.Error("attached the wrong image")
	}
	if _, _, err := run(t, srv, t.TempDir(), "instances", "iso", "attach", "24377", "debian-12"); err == nil || !strings.Contains(err.Error(), "not an ISO") {
		t.Fatalf("a disk image: %v", err)
	}

	f.answer("GET /v1/instances/24377/iso", 200, attached)
	f.answer("DELETE /v1/instances/24377/iso", 204, nil)
	out, _, err = run(t, srv, t.TempDir(), "instances", "iso", "detach", "24377")
	if err != nil || !strings.Contains(out, "ISO alpine-3.20 ejected from web-1 (24377)") {
		t.Fatalf("detach: %q, %v", out, err)
	}
	// Nothing in the drive: nothing to eject, and no DELETE.
	f.answer("GET /v1/instances/24377/iso", 200, map[string]interface{}{"object": "iso_attachment", "instance_id": "24377", "attached": false,
		"image_id": nil, "image_name": nil, "attached_at": nil, "ejects_at": nil})
	deletes := 0
	for _, c := range f.calls {
		if c.Method == "DELETE" {
			deletes++
		}
	}
	out, _, err = run(t, srv, t.TempDir(), "instances", "iso", "detach", "24377")
	if err != nil || !strings.Contains(out, "nothing to eject") {
		t.Fatalf("empty detach: %q, %v", out, err)
	}
	after := 0
	for _, c := range f.calls {
		if c.Method == "DELETE" {
			after++
		}
	}
	if after != deletes {
		t.Error("ejected an empty drive")
	}
	out, _, err = run(t, srv, t.TempDir(), "instances", "iso", "24377")
	if err != nil || !strings.Contains(out, "No ISO in web-1 (24377)'s drive") {
		t.Fatalf("iso get: %q, %v", out, err)
	}
}
