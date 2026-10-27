package cli

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const fwID = "0193f0e2-3333-7000-8000-000000000001"

func firewallJSON(instances ...string) map[string]interface{} {
	if instances == nil {
		instances = []string{}
	}
	return map[string]interface{}{
		"object": "firewall", "id": fwID, "name": "web", "description": nil, "enabled": true, "has_pending_changes": true,
		"policy_in": "DROP", "policy_out": "ACCEPT", "instance_ids": instances, "created_at": "2026-10-01T00:00:00Z",
		"rules": []interface{}{map[string]interface{}{"object": "firewall_rule", "id": "0193f0e2-4444-7000-8000-00000000000a", "position": 0, "direction": "in", "action": "ACCEPT", "macro": "SSH", "enabled": true}},
	}
}

func TestFirewallRulesAndAttach(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/firewalls", 200, page(firewallJSON("24377")))
	f.answer("POST /v1/firewalls/"+fwID+"/rules", 200, firewallJSON("24377"))
	_, _, err := run(t, srv, t.TempDir(), "firewalls", "rules", "add", "web", "--direction", "in", "--action", "accept", "--protocol", "TCP", "--port", "80,443", "--source", "0.0.0.0/0")
	if err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("POST /v1/firewalls/"+fwID+"/rules").Body)
	if b["direction"] != "in" || b["action"] != "ACCEPT" || b["protocol"] != "tcp" || b["dest_port"] != "80,443" || b["source"] != "0.0.0.0/0" {
		t.Errorf("rule body: %v", b)
	}
	if _, ok := b["macro"]; ok {
		t.Error("a flag not given was sent")
	}
	// A macro with ports is refused before anything is sent.
	if _, _, err := run(t, srv, t.TempDir(), "firewalls", "rules", "add", "web", "--direction", "in", "--action", "ACCEPT", "--macro", "SSH", "--port", "22"); err == nil {
		t.Error("macro + port accepted")
	}

	// attach: the commit's list is the WHOLE set — the old one plus the new.
	f.answer("GET /v1/instances/24378", 200, instanceJSON("24378", "web-2", "running"))
	f.answer("POST /v1/firewalls/"+fwID+"/commit", 202, map[string]interface{}{"object": "firewall_commit", "firewall_id": fwID, "operation_id": "op7"})
	f.answer("GET /v1/operations/op7", 200, map[string]interface{}{"id": "op7", "type": "firewall_commit", "status": "completed", "created_at": "2026-10-26T10:00:00Z"})
	out, _, err := run(t, srv, t.TempDir(), "firewalls", "attach", "web", "24378", "--wait")
	if err != nil || !strings.Contains(out, "applied to instance(s) 24377, 24378") {
		t.Fatalf("attach = %q, %v", out, err)
	}
	ids := jsonOf(t, f.last("POST /v1/firewalls/"+fwID+"/commit").Body)["instance_ids"].([]interface{})
	if len(ids) != 2 || ids[0] != "24377" || ids[1] != "24378" {
		t.Errorf("attach set: %v", ids)
	}
	// detach: the old set without it.
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	if _, _, err := run(t, srv, t.TempDir(), "firewalls", "detach", "web", "24377"); err != nil {
		t.Fatal(err)
	}
	ids = jsonOf(t, f.last("POST /v1/firewalls/"+fwID+"/commit").Body)["instance_ids"].([]interface{})
	if len(ids) != 0 {
		t.Errorf("detach set: %v", ids)
	}
}

func TestFirewallUpdatePolicies(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/firewalls/"+fwID, 200, firewallJSON())
	f.answer("PATCH /v1/firewalls/"+fwID, 200, firewallJSON())
	if _, _, err := run(t, srv, t.TempDir(), "firewalls", "update", fwID, "--policy-in", "drop", "--disable"); err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("PATCH /v1/firewalls/"+fwID).Body)
	if b["policy_in"] != "DROP" || b["enabled"] != false || len(b) != 2 {
		t.Errorf("patch body: %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "firewalls", "update", fwID, "--policy-in", "maybe"); err == nil {
		t.Error("a bad policy accepted")
	}
}

func TestDNSRecordSetAddAndDeleteValue(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/dns/zones", 200, page(map[string]interface{}{"object": "dns_zone", "id": "z1", "name": "loja.com.br"}))
	f.answer("POST /v1/dns/zones/z1/records", 200, page())
	f.answer("PUT /v1/dns/zones/z1/records", 200, page())
	f.answer("GET /v1/dns/zones/z1/records", 200, page(map[string]interface{}{"name": "loja.com.br.", "type": "MX", "ttl": 3600, "values": []string{"10 mail.loja.com.br."}, "disabled_values": []string{}}))

	// One value: the replace endpoint every API has.
	if _, _, err := run(t, srv, t.TempDir(), "dns", "records", "set", "loja.com.br", "www", "a", "203.0.113.10", "--ttl", "300"); err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("POST /v1/dns/zones/z1/records").Body)
	if b["name"] != "www" || b["type"] != "A" || b["content"] != "203.0.113.10" || b["ttl"] != float64(300) {
		t.Errorf("set body: %v", b)
	}
	// Several: the record-set endpoint.
	if _, _, err := run(t, srv, t.TempDir(), "dns", "records", "set", "loja.com.br", "@", "A", "203.0.113.10", "203.0.113.11", "--comment", "round robin"); err != nil {
		t.Fatal(err)
	}
	put := jsonOf(t, f.last("PUT /v1/dns/zones/z1/records").Body)
	if v := put["records"].([]interface{}); len(v) != 2 || put["comment"] != "round robin" {
		t.Errorf("put body: %v", put)
	}
	// add keeps what is there.
	out, _, err := run(t, srv, t.TempDir(), "dns", "records", "add", "loja.com.br", "@", "MX", "20 mail2.loja.com.br.")
	if err != nil || !strings.Contains(out, "10 mail.loja.com.br., 20 mail2.loja.com.br.") {
		t.Fatalf("add = %q, %v", out, err)
	}
	if put := jsonOf(t, f.last("PUT /v1/dns/zones/z1/records").Body); put["ttl"] != float64(3600) {
		t.Errorf("add lost the ttl: %v", put)
	}
	// Deleting the only value deletes the record set.
	f.answer("DELETE /v1/dns/zones/z1/records", 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "dns", "records", "delete", "loja.com.br", "@", "MX", "--value", "10 mail.loja.com.br.", "--yes"); err != nil {
		t.Fatal(err)
	}
	if d := jsonOf(t, f.last("DELETE /v1/dns/zones/z1/records").Body); d["name"] != "@" || d["type"] != "MX" {
		t.Errorf("delete body: %v", d)
	}
}

func TestFQDN(t *testing.T) {
	for in, want := range map[string]string{
		"@": "loja.com.br.", "www": "www.loja.com.br.", "www.loja.com.br": "www.loja.com.br.",
		"mail.loja.com.br.": "mail.loja.com.br.", "loja.com.br": "loja.com.br.",
	} {
		if got := fqdn(in, "loja.com.br"); got != want {
			t.Errorf("fqdn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVPCCreateAttachDetach(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "BH", "region_name": "br-bh", "available": true}))
	f.answer("POST /v1/vpcs", 201, map[string]interface{}{"object": "vpc", "id": "12", "name": "backend", "subnet": "10.20.0.0/24", "default": false, "instance_ids": []string{}})
	if _, _, err := run(t, srv, t.TempDir(), "vpcs", "create", "backend", "--subnet", "10.20.0.0/24"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("POST /v1/vpcs").Body); b["region"] != float64(3) || b["subnet"] != "10.20.0.0/24" {
		t.Errorf("create body: %v", b)
	}
	f.answer("GET /v1/vpcs", 200, page(map[string]interface{}{"object": "vpc", "id": "12", "name": "backend", "subnet": "10.20.0.0/24", "instance_ids": []string{}}))
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("POST /v1/vpcs/12/instances", 202, map[string]interface{}{"object": "operation_accepted", "action": "vpc_attach", "resource_type": "vpc", "resource_id": "12", "operation_id": nil})
	out, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "backend", "24377")
	if err != nil || !strings.Contains(out, "next restart") {
		t.Fatalf("attach = %q, %v", out, err)
	}
	// The API refuses a field it does not declare: instance_id alone.
	if b := jsonOf(t, f.last("POST /v1/vpcs/12/instances").Body); b["instance_id"] != "24377" || len(b) != 1 {
		t.Errorf("attach body: %v", b)
	}
	// An address of its choosing, inside the subnet; one outside is refused here.
	if _, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "backend", "24377", "--private-ip", "10.20.0.5"); err != nil {
		t.Fatal(err)
	}
	if b := jsonOf(t, f.last("POST /v1/vpcs/12/instances").Body); b["private_ip"] != "10.20.0.5" || len(b) != 2 {
		t.Errorf("attach body: %v", b)
	}
	f.mu.Lock()
	before := len(f.calls)
	f.mu.Unlock()
	for _, bad := range []string{"10.9.0.5", "2001:db8::5", "web"} {
		if _, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "backend", "24377", "--private-ip", bad); err == nil {
			t.Errorf("--private-ip %s accepted", bad)
		}
	}
	f.mu.Lock()
	for _, c := range f.calls[before:] {
		if c.Method == "POST" {
			t.Errorf("sent %s %s for a refused address", c.Method, c.Path)
		}
	}
	f.mu.Unlock()
	f.answer("DELETE /v1/vpcs/12/instances/24377", 202, map[string]interface{}{"object": "operation_accepted", "action": "vpc_detach", "operation_id": nil})
	if _, _, err := run(t, srv, t.TempDir(), "vpcs", "detach", "backend", "24377", "--restart"); err != nil {
		t.Fatal(err)
	}
	if q := url.Values(f.last("DELETE /v1/vpcs/12/instances/24377").Query); q.Get("restart") != "true" {
		t.Errorf("detach query: %v", q)
	}
}

// An attach answers before the job that plugs the NIC in exists (operation_id
// null): --wait finds the VPC's new operation and waits for it.
func TestVPCAttachWaitsForItsJob(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpcs/12", 200, map[string]interface{}{"object": "vpc", "id": "12", "name": "backend", "instance_ids": []string{}})
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/instances/24377/status", 200, map[string]interface{}{"object": "instance_status", "power_state": "running", "busy": false})
	old := map[string]interface{}{"object": "operation", "id": "op-1", "type": "vpc_refresh", "status": "completed", "created_at": "2026-10-20T12:00:00Z"}
	attached := false
	f.on("GET /v1/operations", func(w http.ResponseWriter, c call) {
		if q := url.Values(c.Query); q.Get("resource_type") != "vpc" || q.Get("resource_id") != "12" {
			t.Errorf("operations query: %v", q)
		}
		f.mu.Lock()
		now := attached
		f.mu.Unlock()
		if !now {
			reply(w, 200, page(old))
			return
		}
		reply(w, 200, page(map[string]interface{}{"object": "operation", "id": "op-2", "type": "vpc_refresh", "status": "in_progress", "created_at": "2026-10-26T12:00:00Z"}, old))
	})
	f.on("POST /v1/vpcs/12/instances", func(w http.ResponseWriter, c call) {
		f.mu.Lock()
		attached = true
		f.mu.Unlock()
		reply(w, 202, map[string]interface{}{"object": "operation_accepted", "action": "vpc_attach", "operation_id": nil})
	})
	polls := 0
	f.on("GET /v1/operations/op-2", func(w http.ResponseWriter, c call) {
		f.mu.Lock()
		polls++
		status := "in_progress"
		if polls > 1 {
			status = "completed"
		}
		f.mu.Unlock()
		reply(w, 200, map[string]interface{}{"object": "operation", "id": "op-2", "type": "vpc_refresh", "status": status, "created_at": "2026-10-26T12:00:00Z"})
	})
	out, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "12", "24377", "--restart", "--wait")
	if err != nil || !strings.Contains(out, "done") || strings.Contains(out, "next restart") {
		t.Fatalf("attach --wait = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/vpcs/12/instances").Body); b["restart"] != true {
		t.Errorf("attach body: %v", b)
	}
	f.mu.Lock()
	if polls < 2 {
		t.Errorf("op-2 polled %d times, want until completed", polls)
	}
	f.mu.Unlock()
	if f.called("GET /v1/operations/op-1") {
		t.Error("waited on an operation from before the attach")
	}
}

// An attach that names its job: --wait follows that operation.
func TestVPCAttachWaitsOnItsOperation(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpcs/12", 200, map[string]interface{}{"object": "vpc", "id": "12", "name": "backend", "subnet": "10.20.0.0/24", "instance_ids": []string{}})
	f.answer("GET /v1/instances/24377", 200, instanceJSON("24377", "web-1", "running"))
	f.answer("GET /v1/operations", 200, page())
	f.answer("POST /v1/vpcs/12/instances", 202, map[string]interface{}{"object": "operation_accepted", "action": "vpc_attach", "resource_type": "vpc", "resource_id": "12", "operation_id": "op-7"})
	f.answer("GET /v1/operations/op-7", 200, map[string]interface{}{"object": "operation", "id": "op-7", "type": "vpc_refresh", "status": "completed", "created_at": "2026-10-26T12:00:00Z"})
	out, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "12", "24377", "--wait")
	if err != nil || !strings.Contains(out, "done") || !f.called("GET /v1/operations/op-7") {
		t.Fatalf("attach --wait = %q, %v", out, err)
	}
	f.answer("GET /v1/operations/op-7", 200, map[string]interface{}{"object": "operation", "id": "op-7", "type": "vpc_refresh", "status": "failed", "error": "the host has no free VXLAN", "created_at": "2026-10-26T12:00:00Z"})
	if _, _, err := run(t, srv, t.TempDir(), "vpcs", "attach", "12", "24377", "--wait"); err == nil || !strings.Contains(err.Error(), "no free VXLAN") {
		t.Errorf("a failed job must fail the command: %v", err)
	}
}

func TestVPCGetShowsItsMembers(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpcs/12", 200, map[string]interface{}{"object": "vpc", "id": "12", "name": "backend", "subnet": "10.20.0.0/24", "default": false,
		"region_id": "3", "instance_ids": []string{"24377", "24378"},
		"members": []interface{}{
			map[string]interface{}{"instance_id": "24377", "private_ip": "10.20.0.5", "status": "online"},
			map[string]interface{}{"instance_id": "24378", "private_ip": nil, "status": "pending"},
		}})
	f.answer("GET /v1/instances", 200, page(instanceJSON("24377", "web-1", "running"), instanceJSON("24378", "db-1", "running")))
	f.answer("GET /v1/regions", 200, page(map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh", "available": true}))
	out, _, err := run(t, srv, t.TempDir(), "vpcs", "get", "12")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Belo Horizonte (br-bh)", "PRIVATE IP", "web-1 (24377)", "10.20.0.5", "online", "db-1 (24378)", "pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "db-1 (24378)") && strings.Join(strings.Fields(line), " ") != "db-1 (24378) — pending" {
			t.Errorf("a member without its address yet: %q", line)
		}
	}
}

func TestVPCDeleteRefusesTheDefault(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpcs/7", 200, map[string]interface{}{"object": "vpc", "id": "7", "name": "default", "default": true, "instance_ids": []string{"1"}})
	if _, _, err := run(t, srv, t.TempDir(), "vpcs", "delete", "7", "--yes"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("delete default = %v", err)
	}
	if f.called("DELETE /v1/vpcs/7") {
		t.Error("asked the API to delete the default VPC")
	}
}

func TestDNSZoneHealth(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/dns/zones", 200, page(map[string]interface{}{"object": "dns_zone", "id": "z1", "name": "loja.com.br"}))
	f.answer("GET /v1/dns/zones/z1/health", 200, map[string]interface{}{
		"object": "dns_zone_health", "zone_id": "z1", "delegated": false,
		"nameservers": []string{"a.dns.br"}, "expected_nameservers": []string{"ns1.masterdaweb.net", "ns2.masterdaweb.net"},
	})
	out, _, err := run(t, srv, t.TempDir(), "dns", "zones", "health", "loja.com.br")
	if err != nil || !strings.Contains(out, "a.dns.br") || !strings.Contains(out, "to ns1.masterdaweb.net and ns2.masterdaweb.net at your registrar") {
		t.Fatalf("health = %q, %v", out, err)
	}
	if out, _, err := run(t, srv, t.TempDir(), "dns", "zones", "health", "loja.com.br", "-q"); err != nil || strings.TrimSpace(out) != "no" {
		t.Fatalf("health -q = %q, %v", out, err)
	}
}

func TestReverseDNS(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("PUT /v1/reverse-dns/203.0.113.10", 200, map[string]interface{}{"object": "reverse_dns", "ip": "203.0.113.10", "version": 4, "hostname": "mail.loja.com.br"})
	out, _, err := run(t, srv, t.TempDir(), "rdns", "set", "203.0.113.10", "mail.loja.com.br.")
	if err != nil || !strings.Contains(out, "now answers to mail.loja.com.br") {
		t.Fatalf("set = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("PUT /v1/reverse-dns/203.0.113.10").Body); b["hostname"] != "mail.loja.com.br" {
		t.Errorf("trailing dot kept: %v", b)
	}
	f.answer("POST /v1/reverse-dns/203.0.113.10/reset", 200, map[string]interface{}{"object": "reverse_dns", "ip": "203.0.113.10", "version": 4, "hostname": "203-0-113-10.host.example", "default_hostname": "203-0-113-10.host.example"})
	out, _, err = run(t, srv, t.TempDir(), "rdns", "reset", "203.0.113.10")
	if err != nil || !strings.Contains(out, "back to 203-0-113-10.host.example") {
		t.Fatalf("reset = %q, %v", out, err)
	}
	f.answer("GET /v1/reverse-dns", 200, map[string]interface{}{"object": "list", "data": []interface{}{
		map[string]interface{}{"ip": "203.0.113.11", "version": 4, "hostname": nil, "default_hostname": "d", "instance_id": "24377", "editable": false, "editable_at": "2026-10-29T00:00:00Z"},
	}})
	out, _, err = run(t, srv, t.TempDir(), "rdns")
	if err != nil || !strings.Contains(out, "from 2026-10-2") {
		t.Fatalf("list = %q, %v", out, err)
	}
	f.on("GET /v1/reverse-dns", func(w http.ResponseWriter, c call) { routeMissingReply(w, c.Path) })
	_, _, err = run(t, srv, t.TempDir(), "rdns")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing endpoint: %v", err)
	}
}

func TestSubscriptionsTopUpAndInvoices(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/subscriptions/sub1/top-ups", 200, map[string]interface{}{"object": "top_up", "subscription_id": "sub1", "invoice_id": "INV-1", "amount_cents": "20000", "currency": "BRL", "pay_url": "https://pay.example/INV-1"})
	// Money moves only with an answer.
	if _, _, err := run(t, srv, t.TempDir(), "subscriptions", "top-up", "sub1", "--amount", "200"); err == nil {
		t.Fatal("topped up without a confirmation")
	}
	out, _, err := run(t, srv, t.TempDir(), "subscriptions", "top-up", "sub1", "--amount", "200,50", "--yes")
	if err != nil || !strings.Contains(out, "https://pay.example/INV-1") {
		t.Fatalf("top-up = %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("POST /v1/subscriptions/sub1/top-ups").Body); b["amount_cents"] != float64(20050) {
		t.Errorf("amount: %v", b)
	}
	f.answer("GET /v1/invoices", 200, page(
		map[string]interface{}{"id": "1", "external_id": "INV-1", "amount_cents": "20050", "currency": "BRL", "status": "OPEN", "overdue": true},
		map[string]interface{}{"id": "2", "external_id": "INV-0", "amount_cents": "4999", "currency": "BRL", "status": "PAID", "overdue": false},
	))
	out, _, err = run(t, srv, t.TempDir(), "invoices", "--open")
	if err != nil || !strings.Contains(out, "R$ 200,50") || !strings.Contains(out, "open (overdue)") || strings.Contains(out, "INV-0") {
		t.Fatalf("invoices = %q, %v", out, err)
	}
	if url.Values(f.last("GET /v1/invoices").Query).Get("limit") != "50" {
		t.Error("the default page size was not asked for")
	}
}

func TestPlansPricedPerMonth(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/catalog/offerings", 200, page(map[string]interface{}{
		"object": "offering", "code": "c2m4", "name": "2 vCPU · 4 GB", "kind": "COMPUTE", "vcpus": 2, "ram_mb": 4096, "base_disk_gb": 80, "currency": "BRL",
		"rates": []interface{}{map[string]interface{}{"term": "MONTHLY", "rate_micro_cents_per_hour": "74405"}},
	}))
	out, _, err := run(t, srv, t.TempDir(), "plans")
	if err != nil {
		t.Fatal(err)
	}
	// A month is the platform's 672 h: 74 405 µ¢/h × 672 h ≈ R$ 50,00, the
	// published price (730 h would say R$ 54,32). 1 cent = 10 000 µ¢.
	if !strings.Contains(out, "c2m4") || !strings.Contains(out, "4 GB") || !strings.Contains(out, "R$ 50,00/mo") {
		t.Fatalf("plans:\n%s", out)
	}
}
