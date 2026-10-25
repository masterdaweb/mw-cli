package cli

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (f *fakeAPI) called(route string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method+" "+c.Path == route {
			return true
		}
	}
	return false
}

func deployment(id string, number int, status string) map[string]interface{} {
	return map[string]interface{}{"id": id, "number": number, "service_id": svcID, "status": status, "trigger": "PUSH", "steps": []interface{}{}, "created_at": "2026-10-25T10:00:00Z"}
}

func TestDeploymentsCancelStopsTheOneUnderWay(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d13", 13, "BUILDING"), deployment("d12", 12, "LIVE")))
	f.answer("POST /v1/serverless/deployments/d13/cancel", 200, deployment("d13", 13, "CANCELLED"))
	out, _, err := run(t, srv, t.TempDir(), "deployments", "cancel", "--service", "web")
	if err != nil || !strings.Contains(out, "Deployment #13 of web cancelled; what was live stays live.") {
		t.Fatalf("cancel: %v %q", err, out)
	}
	if f.last("POST /v1/serverless/deployments/d13/cancel").Header.Get("Idempotency-Key") == "" {
		t.Fatal("a cancel without an Idempotency-Key")
	}

	// Two under way: which one is asked for, by number.
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d15", 15, "QUEUED"), deployment("d14", 14, "BUILDING")))
	if _, _, err := run(t, srv, t.TempDir(), "deployments", "cancel", "--service", "web"); err == nil || !strings.Contains(err.Error(), "2 deployments that can be cancelled (#15, #14)") {
		t.Fatalf("two open: %v", err)
	}
	f.answer("POST /v1/serverless/deployments/d14/cancel", 200, deployment("d14", 14, "CANCELLED"))
	if out, _, err := run(t, srv, t.TempDir(), "deployments", "cancel", "14", "--service", "web"); err != nil || !strings.Contains(out, "#14") {
		t.Fatalf("by number: %v %q", err, out)
	}

	// Releasing: past the point, and the way out said.
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d16", 16, "RELEASING")))
	if _, _, err := run(t, srv, t.TempDir(), "deployments", "cancel", "--service", "web"); err == nil || !strings.Contains(err.Error(), "past the point it can be cancelled") {
		t.Fatalf("releasing: %v", err)
	}
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d12", 12, "LIVE")))
	if _, _, err := run(t, srv, t.TempDir(), "deployments", "cancel", "--service", "web"); err == nil || !strings.Contains(err.Error(), "nothing to cancel") {
		t.Fatalf("nothing open: %v", err)
	}
	// By its id (from a CI log): no service needed.
	const id = "0193f0e2-3333-7000-8000-000000000017"
	f.answer("GET /v1/serverless/deployments/"+id, 200, deployment(id, 17, "QUEUED"))
	f.answer("POST /v1/serverless/deployments/"+id+"/cancel", 200, deployment(id, 17, "CANCELLED"))
	if out, _, err := run(t, srv, t.TempDir(), "--json", "deployments", "cancel", id); err != nil || jsonOf(t, []byte(out))["status"] != "CANCELLED" {
		t.Fatalf("by id: %v %q", err, out)
	}
}

func TestMetricsSummarisesTheWindowAndSaysTheSizeToHave(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/metrics", 200, map[string]interface{}{
		"range": "24h", "step_seconds": 300, "from": "2026-10-24T10:00:00Z", "to": "2026-10-25T10:00:00Z", "available": true, "kind": "WEB",
		"size":         map[string]interface{}{"vcpu": 0.5, "memory_bytes": 1 << 30},
		"times":        []int{1, 2, 3},
		"requests":     map[string]interface{}{"ok": []interface{}{1.0, 2.0, nil}, "client_errors": []interface{}{0.1, 0.0, nil}, "server_errors": []interface{}{0.0, 0.2, nil}},
		"latency_ms":   map[string]interface{}{"p50": []interface{}{80.0, 120.0, nil}, "p95": []interface{}{400.0, 1500.0, nil}},
		"instances":    []interface{}{1.0, 3.0, 2.0},
		"cpu":          map[string]interface{}{"avg": []interface{}{0.1, 0.3}, "max": []interface{}{0.2, 0.45}},
		"memory_bytes": map[string]interface{}{"avg": []interface{}{200 << 20, 300 << 20}, "max": []interface{}{400 << 20, 900 << 20}},
		"totals":       map[string]interface{}{"requests": 1000, "server_errors": 60, "error_rate": 0.06, "restarts": 3, "out_of_memory": 2, "cold_starts": 4, "egress_bytes": 2.5e9},
	})
	f.answer("GET /v1/serverless/services/"+svcID+"/recommendation", 200, map[string]interface{}{"recommendation": map[string]interface{}{
		"direction": "up", "reason": "out_of_memory", "from": "small", "to": "medium", "measure": 3, "monthly_delta_micro_cents": "25550000", "days": 7,
	}})
	out, _, err := run(t, srv, t.TempDir(), "metrics", "--service", "web", "--range", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.last("GET /v1/serverless/services/" + svcID + "/metrics").Query["range"]; len(got) != 1 || got[0] != "24h" {
		t.Fatalf("range sent: %v", got)
	}
	for _, want := range []string{
		"web, the last 24 hours (a point every 5 min)",
		"1000 (", "60 server errors (5xx), 6% of the requests",
		"median 120 ms, 95% under 1.5 s",
		"2 now, 3 at most",
		"of 0.5 vCPU", "900 MiB at the busiest, of 1.0 GiB",
		"3 (2 out of memory)", "Cold starts", "2.5 GB",
		"go up from small to medium — instances ran out of memory 3 times (the last 7 days); about R$ 25,55 more a month.",
		"mw scale --size medium",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if _, _, err := run(t, srv, t.TempDir(), "metrics", "--service", "web", "--range", "2d"); err == nil || !strings.Contains(err.Error(), "1h, 6h, 24h or 7d") {
		t.Fatalf("bad range: %v", err)
	}
	// Without a recommendation (or metrics just now), it says so plainly.
	f.answer("GET /v1/serverless/services/"+svcID+"/recommendation", 200, map[string]interface{}{"recommendation": nil})
	f.answer("GET /v1/serverless/services/"+svcID+"/metrics", 200, map[string]interface{}{"range": "1h", "available": false, "kind": "WEB"})
	if out, _, err := run(t, srv, t.TempDir(), "metrics", "--service", "web"); err != nil || !strings.Contains(out, "could not be read just now") {
		t.Fatalf("unavailable: %v %q", err, out)
	}
}

func TestLogsAccessPrintsTheRequestsAndFiltersThem(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/access-logs", 200, map[string]interface{}{"lines": []interface{}{
		map[string]interface{}{"ts": "2026-10-25T10:00:01Z", "instance": "i-1", "revision": 3, "method": "POST", "host": "loja.com.br", "path": "/checkout", "status": 500, "bytes": 2048, "latency_ms": 1234, "ip": "203.0.113.7", "user_agent": "x"},
	}, "truncated": true})
	out, errOut, err := run(t, srv, t.TempDir(), "logs", "--access", "--status", "5xx", "--search", "/checkout", "--service", "web")
	if err != nil || !strings.Contains(out, "POST   500  loja.com.br/checkout  1.2 s  2.0 KB") {
		t.Fatalf("access: %v %q", err, out)
	}
	q := url.Values(f.last("GET /v1/serverless/services/" + svcID + "/access-logs").Query)
	if q.Get("status") != "5xx" || q.Get("search") != "/checkout" || q.Get("since") == "" {
		t.Fatalf("query: %v", q)
	}
	if !strings.Contains(errOut, "narrow it") {
		t.Fatalf("a truncated answer not said: %q", errOut)
	}
	if _, _, err := run(t, srv, t.TempDir(), "logs", "--status", "5xx", "--service", "web"); err == nil || !strings.Contains(err.Error(), "add --access") {
		t.Fatalf("status without access: %v", err)
	}
	f.answer("GET /v1/serverless/services", 200, page(service("WORKER", "GIT")))
	if _, _, err := run(t, srv, t.TempDir(), "logs", "--access", "--service", "web"); err == nil || !strings.Contains(err.Error(), "no access logs") {
		t.Fatalf("worker: %v", err)
	}
}

func TestAlertsListThemAndSetThemInTheirUnits(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/alerts", 200, []interface{}{
		map[string]interface{}{"kind": "ERROR_RATE", "enabled": true, "threshold": 0.05, "window_minutes": 5, "email": true, "state": "FIRING", "last_value": 0.12, "since": "2026-10-25T10:00:00Z"},
		map[string]interface{}{"kind": "LATENCY_P95", "enabled": false, "threshold": 2000, "window_minutes": 10, "email": true, "state": "OK", "last_value": nil},
	})
	out, _, err := run(t, srv, t.TempDir(), "alerts", "--service", "web")
	if err != nil || !strings.Contains(out, "error-rate") || !strings.Contains(out, "FIRING since") || !strings.Contains(out, "over 5%") || !strings.Contains(out, "12%") || !strings.Contains(out, "over 2.0 s") {
		t.Fatalf("list: %v %q", err, out)
	}
	f.on("PATCH /v1/serverless/services/"+svcID+"/alerts/LATENCY_P95", func(w http.ResponseWriter, c call) {
		reply(w, 200, map[string]interface{}{"kind": "LATENCY_P95", "enabled": true, "threshold": 1500, "window_minutes": 15, "email": false, "state": "OK"})
	})
	out, _, err = run(t, srv, t.TempDir(), "alerts", "set", "latency", "--threshold", "1.5s", "--window", "15", "--on", "--email=false", "--service", "web")
	if err != nil || !strings.Contains(out, "the latency alert fires over 1.5 s for 15 minutes, in the console and on your webhooks.") {
		t.Fatalf("set: %v %q", err, out)
	}
	body := jsonOf(t, f.last("PATCH /v1/serverless/services/"+svcID+"/alerts/LATENCY_P95").Body)
	if body["threshold"] != 1500.0 || body["window_minutes"] != 15.0 || body["enabled"] != true || body["email"] != false {
		t.Fatalf("sent %v", body)
	}
	f.answer("PATCH /v1/serverless/services/"+svcID+"/alerts/MEMORY", 200, map[string]interface{}{"kind": "MEMORY", "enabled": true, "threshold": 0.85, "window_minutes": 10, "email": true, "state": "OK"})
	if _, _, err := run(t, srv, t.TempDir(), "alerts", "set", "memory", "--threshold", "85%", "--service", "web"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/services/"+svcID+"/alerts/MEMORY").Body); body["threshold"] != 0.85 || len(body) != 1 {
		t.Fatalf("memory sent %v", body)
	}
	for args, want := range map[string]string{
		"error-rate --threshold 5": "write a share as a percentage (5%) or a fraction (0.05)",
		"bogus --on":               "it is error-rate, latency, memory or max-instances",
		"memory --window 7":        "5, 10, 15, 30 or 60",
		"memory":                   "nothing to change",
		"memory --on --off":        "not both",
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"alerts", "set"}, append(strings.Fields(args), "--service", "web")...)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", args, err)
		}
	}
}

func webhook(id, kind string) map[string]interface{} {
	return map[string]interface{}{"id": id, "kind": kind, "url": "https://loja.com.br/…/mw", "description": "loja", "events": []string{}, "enabled": true, "consecutive_failures": 0, "created_at": "2026-10-25T10:00:00Z"}
}

func TestWebhooksAddShowTheSecretOnceAndRemovalAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	const id = "0193f0e2-9999-7000-8000-000000000001"
	f.on("POST /v1/webhooks", func(w http.ResponseWriter, c call) {
		kind := jsonOf(t, c.Body)["kind"]
		var secret interface{}
		if kind == "GENERIC" {
			secret = "whsec_abc"
		}
		reply(w, 200, map[string]interface{}{"webhook": webhook(id, kind.(string)), "secret": secret})
	})
	out, _, err := run(t, srv, t.TempDir(), "webhooks", "add", "https://loja.com.br/hooks/mw", "--events", "deployment.failed,alert.*")
	if err != nil || !strings.Contains(out, "whsec_abc") || !strings.Contains(out, "shown this once") || !strings.Contains(out, "MW-Signature") {
		t.Fatalf("add: %v %q", err, out)
	}
	body := jsonOf(t, f.last("POST /v1/webhooks").Body)
	if body["kind"] != "GENERIC" || len(body["events"].([]interface{})) != 2 {
		t.Fatalf("sent %v", body)
	}
	if _, _, err := run(t, srv, t.TempDir(), "webhooks", "add", "https://hooks.slack.com/services/T0/B0/X"); err != nil || jsonOf(t, f.last("POST /v1/webhooks").Body)["kind"] != "SLACK" {
		t.Fatalf("slack: %v", err)
	}

	f.answer("GET /v1/webhooks", 200, []interface{}{webhook(id, "GENERIC")})
	f.answer("DELETE /v1/webhooks/"+id, 200, map[string]interface{}{"deleted": true})
	// No: nothing removed. No answer at all (a script): the flag that answers.
	if out, _, err := runWithInput(t, srv, t.TempDir(), "n\n", "webhooks", "remove", "0193f0e2"); err != nil || !strings.Contains(out, "Nothing removed.") || f.called("DELETE /v1/webhooks/"+id) {
		t.Fatalf("no: %v %q", err, out)
	}
	if _, _, err := run(t, srv, t.TempDir(), "webhooks", "remove", "0193f0e2"); err == nil || !strings.Contains(err.Error(), "add --yes") || f.called("DELETE /v1/webhooks/"+id) {
		t.Fatalf("no answer: %v", err)
	}
	if out, _, err := runWithInput(t, srv, t.TempDir(), "y\n", "webhooks", "remove", "0193f0e2"); err != nil || !strings.Contains(out, "removed") || !f.called("DELETE /v1/webhooks/"+id) {
		t.Fatalf("yes: %v %q", err, out)
	}

	f.answer("POST /v1/webhooks/"+id+"/rotate-secret", 200, map[string]interface{}{"secret": "whsec_new"})
	if out, _, err := run(t, srv, t.TempDir(), "webhooks", "rotate", id, "--yes"); err != nil || !strings.Contains(out, "whsec_new") {
		t.Fatalf("rotate: %v %q", err, out)
	}
	f.answer("POST /v1/webhooks/"+id+"/test", 200, map[string]interface{}{"ok": true, "status": 204, "error": nil})
	if out, _, err := run(t, srv, t.TempDir(), "webhooks", "test", "loja"); err != nil || !strings.Contains(out, "Delivered") || !strings.Contains(out, "HTTP 204") {
		t.Fatalf("test: %v %q", err, out)
	}
	f.answer("POST /v1/webhooks/"+id+"/test", 200, map[string]interface{}{"ok": false, "status": 500, "error": "Internal Server Error"})
	if _, _, err := run(t, srv, t.TempDir(), "webhooks", "test", "loja"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("failed test: %v", err)
	}
	f.answer("GET /v1/webhooks", 200, []interface{}{webhook(id, "SLACK")})
	if _, _, err := run(t, srv, t.TempDir(), "webhooks", "rotate", id, "--yes"); err == nil || !strings.Contains(err.Error(), "only your own addresses are signed") {
		t.Fatalf("rotate slack: %v", err)
	}
	f.on("PATCH /v1/webhooks/"+id, func(w http.ResponseWriter, c call) {
		reply(w, 200, map[string]interface{}{"id": id, "kind": "SLACK", "url": "https://hooks.slack.com/…X", "events": []string{"alert.*"}, "enabled": false, "consecutive_failures": 0, "created_at": "2026-10-25T10:00:00Z"})
	})
	if out, _, err := run(t, srv, t.TempDir(), "webhooks", "update", id, "--events", "alert.*", "--off"); err != nil || !strings.Contains(out, "off, for alert.*") {
		t.Fatalf("update: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PATCH /v1/webhooks/"+id).Body); body["enabled"] != false || len(body["events"].([]interface{})) != 1 {
		t.Fatalf("update sent %v", body)
	}
}

func TestEgressSaysWhatWasSentOfWhatIsIncluded(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/egress", 200, map[string]interface{}{
		"period_from": "2026-10-01T00:00:00Z", "period_to": "2026-11-01T00:00:00Z",
		"used_bytes": "180000000000", "allowance_bytes": "250000000000", "price_micro_cents_per_gb": "200000", "charged_micro_cents": "0",
		"services": []interface{}{map[string]interface{}{"service_id": svcID, "name": "web", "bytes": "180000000000"}},
	})
	out, _, err := run(t, srv, t.TempDir(), "egress")
	if err != nil || !strings.Contains(out, "180.0 GB of 250.0 GB included (72%)") || !strings.Contains(out, "R$ 0,20 per GB") || !strings.Contains(out, "web") {
		t.Fatalf("egress: %v %q", err, out)
	}
}
