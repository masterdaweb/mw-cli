package cli

import (
	"net/http"
	"strings"
	"testing"
)

// The locations as the API lists them: Belo Horizonte takes apps and
// databases; Dallas takes apps, its databases are temporarily unavailable.
func withLocations(f *fakeAPI) {
	f.answer("GET /v1/regions", 200, page(
		map[string]interface{}{"id": "3", "name": "Belo Horizonte", "region_name": "br-bh", "available": true, "ipv4_available": true, "ipv6_available": true},
		map[string]interface{}{"id": "4", "name": "Dallas", "region_name": "us-dal", "available": true, "ipv4_available": true, "ipv6_available": false},
	))
	f.answer("GET /v1/serverless/locations", 200, []map[string]interface{}{
		{"id": 3, "name": "Belo Horizonte", "region_name": "br-bh", "deployable": true},
		{"id": 4, "name": "Dallas", "region_name": "us-dal", "deployable": true},
	})
	f.answer("GET /v1/databases/locations", 200, []map[string]interface{}{
		{"id": 3, "name": "Belo Horizonte", "region_name": "br-bh", "deployable": true},
		{"id": 4, "name": "Dallas", "region_name": "us-dal", "deployable": false},
	})
}

func TestRegionsSaysWhereAppsAndDatabasesGo(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	out, _, err := run(t, srv, t.TempDir(), "regions")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.Contains(lines[0], "APPS") || !strings.Contains(lines[0], "DATABASES") {
		t.Fatalf("headers: %q", lines[0])
	}
	if f := strings.Fields(lines[2]); len(f) < 6 || f[1] != "Dallas" || f[4] != "yes" || f[5] != "no" {
		t.Fatalf("Dallas: %q", lines[2])
	}
}

func TestProjectsCreateInTheLocationNamedAnyWay(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	f.answer("POST /v1/serverless/projects", 201, map[string]interface{}{"id": "p1", "name": "loja-us", "region": "dal", "datacenter_id": 4})
	for _, ref := range []string{"4", "dallas", "DÁLLAS", "us-dal"} {
		out, _, err := run(t, srv, t.TempDir(), "projects", "create", "loja-us", "--location", ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if body := jsonOf(t, f.last("POST /v1/serverless/projects").Body); body["datacenter_id"] != float64(4) {
			t.Fatalf("%s sent %v", ref, body)
		}
		if !strings.Contains(out, "created in Dallas") {
			t.Fatalf("%s said %q", ref, out)
		}
	}
	// One that is not offered: what is, and nothing made.
	before := len(f.calls)
	_, _, err := run(t, srv, t.TempDir(), "projects", "create", "loja-x", "--location", "atlantis")
	if err == nil || !strings.Contains(err.Error(), `no location "atlantis": one of Belo Horizonte, Dallas`) {
		t.Fatalf("got %v", err)
	}
	for _, c := range f.calls[before:] {
		if c.Method == "POST" {
			t.Fatalf("made something: %s %s", c.Method, c.Path)
		}
	}
}

func TestDatabasesGoWhereAskedAndNeverWhereTheyCannot(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	f.answer("GET /v1/databases/info", 200, map[string]interface{}{
		"available": true,
		"engines": []map[string]interface{}{{"engine": "postgres", "versions": []int{18},
			"sizes": []map[string]interface{}{{"code": "pg-1-1", "cpus": 1, "memory_mb": 1024, "disk_gb": 10}}}},
	})
	made := database("provisioning")
	made["datacenter_id"] = 3
	f.answer("POST /v1/databases", 200, map[string]interface{}{"database": made, "password": "Zq8sR2vX9pLm4TnB7wYc1KdH6gFj3eAu"})
	if _, _, err := run(t, srv, t.TempDir(), "db", "create", "loja-prod", "--location", "belo horizonte", "--detach"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/databases").Body); body["datacenter_id"] != float64(3) {
		t.Fatalf("sent %v", body)
	}
	_, _, err := run(t, srv, t.TempDir(), "db", "create", "loja-us", "--location", "dallas", "--detach")
	if err == nil || !strings.Contains(err.Error(), "Dallas is temporarily unavailable for new databases") {
		t.Fatalf("got %v", err)
	}

	// A restore into another location, and the listing says where each runs.
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/restore", 200, map[string]interface{}{"database": made, "password": "x"})
	if _, _, err := run(t, srv, t.TempDir(), "db", "restore", "loja-prod", "--to", "loja-copia", "--location", "3", "--detach"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/databases/loja-prod/restore").Body); body["datacenter_id"] != float64(3) {
		t.Fatalf("restore sent %v", body)
	}
	running := database("running")
	running["datacenter_id"] = 3
	f.answer("GET /v1/databases", 200, []interface{}{running})
	out, _, err := run(t, srv, t.TempDir(), "db")
	if err != nil || !strings.Contains(out, "LOCATION") || !strings.Contains(out, "Belo Horizonte") {
		t.Fatalf("listing: %q %v", out, err)
	}
}

func TestTemplatesDeployInTheLocationNamed(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	f.on("POST /v1/serverless/templates/laravel/deploy", func(w http.ResponseWriter, _ call) {
		reply(w, 201, map[string]interface{}{"project": map[string]interface{}{"id": "p2", "name": "loja"}, "services": []interface{}{}, "databases": []interface{}{}})
	})
	if _, _, err := run(t, srv, t.TempDir(), "templates", "deploy", "laravel", "--name", "loja", "--location", "br-bh", "--detach"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/templates/laravel/deploy").Body); body["datacenter_id"] != float64(3) {
		t.Fatalf("sent %v", body)
	}
}
