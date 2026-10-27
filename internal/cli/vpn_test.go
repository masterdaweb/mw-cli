package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gwID = "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e40"
const vpnUserID = "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e41"

func gatewayJSON(over map[string]interface{}) map[string]interface{} {
	g := map[string]interface{}{
		"object": "vpn_gateway", "id": gwID, "instance_id": "24390", "name": "office", "location": "Belo Horizonte",
		"public_ip": "203.0.113.9", "running": true, "protocol": "udp", "remote_access_pool": "10.8.0.0/24",
		"remote_access_pool_issue": nil, "route_all_traffic": false, "allow_shared_logins": false, "mtu": 1420,
		"wireguard":       map[string]interface{}{"public_key": "pubkey=", "listen_port": 51820},
		"pending_changes": false, "apply_mode": "in_place", "apply_state": "idle", "apply_operation_id": nil,
		"applied_at": "2026-10-26T12:00:00Z", "last_apply_error_code": nil, "update_available": false,
		"users_count": 3, "site_connections_count": 1, "included_site_connections": 2, "created_at": "2026-10-01T12:00:00Z",
	}
	for k, v := range over {
		g[k] = v
	}
	return g
}

func vpnUserJSON(password interface{}) map[string]interface{} {
	u := map[string]interface{}{"object": "vpn_user", "id": vpnUserID, "gateway_id": gwID, "username": "maria", "email": "maria@loja.com.br",
		"status": "active", "created_at": "2026-10-26T12:00:00Z", "updated_at": "2026-10-26T12:00:00Z"}
	if password != nil {
		u["password"] = password
	}
	return u
}

func TestVPNListAndGetByNameOrInstance(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(map[string]interface{}{"pending_changes": true, "apply_mode": "rebuild"})))
	out, _, err := run(t, srv, t.TempDir(), "vpn")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"office", "Belo Horizonte", "203.0.113.9", "running", "pending (rebuild)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list misses %q:\n%s", want, out)
		}
	}
	for _, ref := range []string{"Office", "24390", "203.0.113.9"} {
		out, _, err = run(t, srv, t.TempDir(), "vpn", "get", ref)
		if err != nil || !strings.Contains(out, "10.8.0.0/24") || !strings.Contains(out, "`mw vpn apply office --rebuild`") || !strings.Contains(out, "1 of 2 included") {
			t.Fatalf("get %s: %q, %v", ref, out, err)
		}
	}
	if _, _, err := run(t, srv, t.TempDir(), "vpn", "get", "home"); err == nil || !strings.Contains(err.Error(), "no VPN gateway home") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestVPNApplyStopsForARebuildAndWaits(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	f.on("POST /v1/vpn-gateways/"+gwID+"/apply", func(w http.ResponseWriter, c call) {
		if jsonOf(t, c.Body)["rebuild"] != true {
			reply(w, 409, map[string]interface{}{"status": 409, "code": "conflict", "reason": "VPN_GATEWAY_REBUILD_REQUIRED",
				"detail": "Applying these changes rebuilds this gateway."})
			return
		}
		reply(w, 202, map[string]interface{}{"object": "vpn_gateway_apply", "gateway_id": gwID, "mode": "rebuild", "operation_id": "op-9", "coalesced": false})
	})
	_, _, err := run(t, srv, t.TempDir(), "vpn", "apply", "office")
	if err == nil || !strings.Contains(err.Error(), "rebuilds office") || !strings.Contains(err.Error(), "`mw vpn apply office --rebuild`") {
		t.Fatalf("rebuild required: %v", err)
	}
	// --rebuild asks first; with nothing on standard input it names --yes.
	if _, _, err := run(t, srv, t.TempDir(), "vpn", "apply", "office", "--rebuild"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unconfirmed rebuild: %v", err)
	}
	f.answer("GET /v1/operations/op-9", 200, map[string]interface{}{"id": "op-9", "type": "vm_reinstall", "status": "completed", "created_at": "2026-10-26T12:00:00Z"})
	out, _, err := runWithInput(t, srv, t.TempDir(), "y\n", "vpn", "apply", "office", "--rebuild", "--wait")
	if err != nil || !strings.Contains(out, "office rebuilt with the changes in place.") {
		t.Fatalf("rebuild: %q, %v", out, err)
	}
	if f.last("POST /v1/vpn-gateways/"+gwID+"/apply").Header.Get("Idempotency-Key") == "" {
		t.Error("no Idempotency-Key on the apply")
	}
}

func TestVPNProfileIsWrittenForTheOwnerOnly(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	f.answer("GET /v1/vpn-gateways/"+gwID+"/profile", 200, map[string]interface{}{"object": "vpn_profile", "gateway_id": gwID, "format": "openvpn",
		"filename": "../Office.ovpn", "content": "client\nremote 203.0.113.9 1194\n<key>SECRET</key>\n"})
	dir := t.TempDir()
	file := filepath.Join(dir, "office.ovpn")
	_, errOut, err := run(t, srv, dir, "vpn", "profile", "office", file)
	if err != nil || !strings.Contains(errOut, "readable by you only") {
		t.Fatalf("profile: %q, %v", errOut, err)
	}
	st, _ := os.Stat(file)
	data, _ := os.ReadFile(file)
	if st.Mode().Perm() != 0o600 || !strings.Contains(string(data), "remote 203.0.113.9") {
		t.Fatalf("file %v: %q", st.Mode(), data)
	}
	if _, _, err := run(t, srv, dir, "vpn", "profile", "office", file); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("over an existing file: %v", err)
	}
	// --force over a file anyone could read leaves it readable by its owner only.
	os.Chmod(file, 0o644)
	if _, _, err := run(t, srv, dir, "vpn", "profile", "office", file, "--force"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatalf("after --force: %v", st.Mode())
	}
	// --save takes the API's name, but never a path out of here.
	saveDir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	os.Chdir(saveDir)
	if _, _, err := run(t, srv, saveDir, "vpn", "profile", "office", "--save"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(saveDir, "Office.ovpn")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("--save: %v", err)
	}
	out, _, err := run(t, srv, dir, "vpn", "profile", "office")
	if err != nil || !strings.HasPrefix(out, "client\n") {
		t.Fatalf("to standard output: %q, %v", out, err)
	}
}

func TestVPNUsersCreateShowsAGeneratedPasswordOnceAndApplies(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	f.on("POST /v1/vpn-gateways/"+gwID+"/users", func(w http.ResponseWriter, c call) {
		if _, chosen := jsonOf(t, c.Body)["password"]; chosen {
			reply(w, 201, vpnUserJSON(nil))
			return
		}
		reply(w, 201, vpnUserJSON("Zq8sR2vX9pLm4TnB7wYc1KdH"))
	})
	f.answer("POST /v1/vpn-gateways/"+gwID+"/apply", 202, map[string]interface{}{"object": "vpn_gateway_apply", "gateway_id": gwID, "mode": "in_place", "operation_id": "op-1", "coalesced": false})
	out, errOut, err := run(t, srv, t.TempDir(), "vpn", "users", "create", "office", "maria", "--email", "maria@loja.com.br", "--send-credentials", "--apply")
	if err != nil {
		t.Fatal(err)
	}
	b := jsonOf(t, f.last("POST /v1/vpn-gateways/"+gwID+"/users").Body)
	if b["username"] != "maria" || b["email"] != "maria@loja.com.br" || b["send_credentials"] != true {
		t.Errorf("body %v", b)
	}
	if strings.Count(out, "Zq8sR2vX9pLm4TnB7wYc1KdH") != 1 || strings.Contains(errOut, "Zq8sR2vX9pLm4TnB7wYc1KdH") {
		t.Fatalf("the password, once and on standard output only: %q / %q", out, errOut)
	}
	for _, want := range []string{"shown here once", "emailed to maria@loja.com.br", "Applying the changes to office (operation op-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("create misses %q:\n%s", want, out)
		}
	}
	if !f.called("POST /v1/vpn-gateways/" + gwID + "/apply") {
		t.Fatal("--apply did not apply")
	}

	// A password of their own, from standard input: sent, never printed back.
	out, _, err = runWithInput(t, srv, t.TempDir(), "correct horse battery\n", "vpn", "users", "create", "office", "joao", "--email", "joao@loja.com.br", "--password-stdin")
	if err != nil || strings.Contains(out, "correct horse") || !strings.Contains(out, "after `mw vpn apply office`") {
		t.Fatalf("own password: %q, %v", out, err)
	}
	if jsonOf(t, f.last("POST /v1/vpn-gateways/"+gwID+"/users").Body)["password"] != "correct horse battery" {
		t.Error("the password from standard input was not sent")
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"maria.s", "--email", "m@x.com"}, "letters and digits"},
		{[]string{"maria", "--email", "nope"}, "--email"},
		{[]string{"maria", "--email", "m@x.com", "--password", "short"}, "8 to 128"},
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"vpn", "users", "create", "office"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}

func TestVPNUsersUpdateAndDeleteByUsername(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	f.answer("GET /v1/vpn-gateways/"+gwID+"/users", 200, page(vpnUserJSON(nil)))
	inactive := vpnUserJSON(nil)
	inactive["status"] = "inactive"
	f.answer("PATCH /v1/vpn-gateways/"+gwID+"/users/"+vpnUserID, 200, inactive)
	out, _, err := run(t, srv, t.TempDir(), "vpn", "users", "update", "office", "Maria", "--status", "inactive")
	if err != nil || !strings.Contains(out, "inactive (the login is refused)") || !strings.Contains(out, "after `mw vpn apply office`") {
		t.Fatalf("update: %q, %v", out, err)
	}
	if b := jsonOf(t, f.last("PATCH /v1/vpn-gateways/"+gwID+"/users/"+vpnUserID).Body); len(b) != 1 || b["status"] != "inactive" {
		t.Errorf("body %v", b)
	}
	if _, _, err := run(t, srv, t.TempDir(), "vpn", "users", "update", "office", "maria", "--send-credentials"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("nothing: %v", err)
	}
	f.answer("DELETE /v1/vpn-gateways/"+gwID+"/users/"+vpnUserID, 204, nil)
	if _, _, err := run(t, srv, t.TempDir(), "vpn", "users", "delete", "office", "maria"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unconfirmed delete: %v", err)
	}
	out, _, err = run(t, srv, t.TempDir(), "vpn", "users", "delete", "office", "maria@loja.com.br", "--yes")
	if err != nil || !strings.Contains(out, "maria's login removed from office") || !strings.Contains(out, "can still connect until") {
		t.Fatalf("delete: %q, %v", out, err)
	}
	out, _, err = run(t, srv, t.TempDir(), "vpn", "users", "office")
	if err != nil || !strings.Contains(out, "maria@loja.com.br") {
		t.Fatalf("list: %q, %v", out, err)
	}
}

func TestVPNUpdateChecksAndSendsTheAPINames(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	f.answer("PATCH /v1/vpn-gateways/"+gwID, 200, gatewayJSON(map[string]interface{}{"pending_changes": true, "remote_access_pool": "10.9.0.0/24"}))
	f.answer("POST /v1/vpn-gateways/"+gwID+"/apply", 202, map[string]interface{}{"object": "vpn_gateway_apply", "gateway_id": gwID, "mode": "in_place", "operation_id": nil, "coalesced": true})
	out, _, err := run(t, srv, t.TempDir(), "vpn", "update", "office", "--pool", "10.9.0.5/24", "--route-all-traffic", "on", "--protocol", "TCP", "--apply")
	if err != nil || !strings.Contains(out, "10.9.0.0/24") || !strings.Contains(out, "already being updated") {
		t.Fatalf("update: %q, %v", out, err)
	}
	b := jsonOf(t, f.last("PATCH /v1/vpn-gateways/"+gwID).Body)
	if b["remote_access_pool"] != "10.9.0.0/24" || b["route_all_traffic"] != true || b["protocol"] != "tcp" || len(b) != 3 {
		t.Errorf("body %v", b)
	}
	before := len(f.calls)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--mtu", "100"}, "576 to 1500"},
		{[]string{"--pool", "2001:db8::/64"}, "IPv4 network"},
		{[]string{"--protocol", "ipsec"}, "udp or tcp"},
		{nil, "nothing to change"},
	} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"vpn", "update", "office"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.args, err)
		}
	}
	if len(f.calls) != before {
		t.Fatal("a refused change reached the API")
	}
}

func TestVPNSitesShowTunnelsWithoutKeys(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/vpn-gateways", 200, page(gatewayJSON(nil)))
	site := map[string]interface{}{"object": "vpn_site_connection", "id": "0192f5c4-3b7a-7cc1-9a51-6b1f9c4d2e50", "gateway_id": gwID, "name": "matriz",
		"protocol": "ipsec_ikev2", "status": "active", "enabled": true, "peer_vendor": "mikrotik", "peer_address": "198.51.100.4",
		"remote_networks": []string{"192.168.10.0/24"}, "tunnel_state": "down", "error_class": "peer_unreachable", "error_detail": "No answer on UDP 500.",
		"last_handshake_at": "2026-10-26T11:00:00Z", "last_up_at": "2026-10-26T11:00:00Z", "bytes_in": "12345678901", "bytes_out": "5", "applied_at": nil, "created_at": "2026-10-01T12:00:00Z"}
	f.answer("GET /v1/vpn-gateways/"+gwID+"/site-connections", 200, page(site))
	out, _, err := run(t, srv, t.TempDir(), "vpn", "sites", "office")
	if err != nil || !strings.Contains(out, "IPsec (IKEv2)") || !strings.Contains(out, "down (peer unreachable)") || !strings.Contains(out, "192.168.10.0/24") {
		t.Fatalf("sites: %q, %v", out, err)
	}
	out, _, err = run(t, srv, t.TempDir(), "vpn", "sites", "get", "office", "Matriz")
	if err != nil || !strings.Contains(out, "No answer on UDP 500.") || !strings.Contains(out, "12.3 GB in, 5 B out") {
		t.Fatalf("site: %q, %v", out, err)
	}
}
