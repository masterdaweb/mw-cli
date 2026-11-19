package cli

import (
	"strings"
	"testing"
)

// A cron job was offered by --kind and could not be made: nothing sent its
// schedule, and the API refuses a cron service without one.
func TestServicesCreateCronSendsItsSchedule(t *testing.T) {
	f, srv := newFakeAPI(t)
	pid := "0193f0e2-2222-7000-8000-000000000009"
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": pid, "name": "loja"}))
	f.answer("POST /v1/serverless/projects/"+pid+"/services", 200, map[string]interface{}{"service": service("CRON", "IMAGE")})

	// No schedule: said before anything is sent.
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "limpeza", "--project", "loja", "--image", "ghcr.io/acme/jobs:1", "--kind", "cron"); err == nil || !strings.Contains(err.Error(), "--schedule") {
		t.Fatalf("cron without a schedule: %v", err)
	}
	if f.count("POST /v1/serverless/projects/"+pid+"/services") != 0 {
		t.Fatal("a cron job without a schedule reached the API")
	}
	// A schedule on a web service is a mistake, not a value to drop.
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "api", "--project", "loja", "--image", "nginx:1", "--schedule", "* * * * *"); err == nil || !strings.Contains(err.Error(), "--kind cron") {
		t.Fatalf("schedule on a web service: %v", err)
	}

	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "limpeza", "--project", "loja", "--image", "ghcr.io/acme/jobs:1", "--kind", "cron", "--schedule", " 0 3 * * * ", "--timezone", "America/Sao_Paulo"); err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/services").Body)
	settings, _ := body["settings"].(map[string]interface{})
	if body["kind"] != "CRON" || settings["cron_schedule"] != "0 3 * * *" || settings["cron_timezone"] != "America/Sao_Paulo" {
		t.Fatalf("sent kind %v, settings %v", body["kind"], settings)
	}
	// The time zone is the platform's default when not given: nothing sent.
	if _, _, err := run(t, srv, t.TempDir(), "services", "create", "limpeza2", "--project", "loja", "--image", "ghcr.io/acme/jobs:1", "--kind", "cron", "--schedule", "*/15 * * * *"); err != nil {
		t.Fatal(err)
	}
	settings, _ = jsonOf(t, f.last("POST /v1/serverless/projects/"+pid+"/services").Body)["settings"].(map[string]interface{})
	if _, has := settings["cron_timezone"]; has || settings["cron_schedule"] != "*/15 * * * *" {
		t.Fatalf("settings %v", settings)
	}
}

func TestCronSettings(t *testing.T) {
	if s, err := cronSettings("web", "", ""); err != nil || s != nil {
		t.Fatalf("web: %v %v", s, err)
	}
	if _, err := cronSettings("worker", "", "UTC"); err == nil {
		t.Fatal("a time zone on a worker passed")
	}
	if _, err := cronSettings("CRON", "  ", ""); err == nil {
		t.Fatal("a blank schedule passed")
	}
	if s, err := cronSettings("Cron", "0 * * * *", ""); err != nil || s["cron_schedule"] != "0 * * * *" || len(s) != 1 {
		t.Fatalf("cron: %v %v", s, err)
	}
}
