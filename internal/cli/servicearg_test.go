package cli

import (
	"io"
	"strings"
	"testing"
)

// `mw open web` was `unknown command "web"`: the service is also an argument.
func TestTheServiceCanBeNamedAsTheArgument(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(liveService()))
	was := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = was }()
	t.Setenv("MW_NO_BROWSER", "1")

	byFlag, _, err := run(t, srv, t.TempDir(), "open", "--service", "web")
	if err != nil {
		t.Fatal(err)
	}
	byArg, _, err := run(t, srv, t.TempDir(), "open", "web")
	if err != nil || byArg != byFlag {
		t.Fatalf("mw open web: %q %v, want %q", byArg, err, byFlag)
	}
	// The same name twice is fine; two different ones are refused.
	if _, _, err := run(t, srv, t.TempDir(), "open", "web", "--service", "web"); err != nil {
		t.Fatalf("the same name twice: %v", err)
	}
	if _, _, err := run(t, srv, t.TempDir(), "open", "web", "--service", "api"); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("two names: %v", err)
	}
	// More than one argument is not a service.
	if _, _, err := run(t, srv, t.TempDir(), "open", "web", "api"); err == nil {
		t.Fatal("two arguments were taken")
	}
}

func TestEveryServiceCommandSaysItTakesTheService(t *testing.T) {
	root := NewRoot(&App{Version: "test", In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	for _, name := range serviceArgCommands {
		c, _, err := root.Find([]string{name})
		if err != nil || c.Name() != name {
			t.Fatalf("mw %s: %v", name, err)
		}
		if !strings.HasSuffix(c.Use, "[service]") || c.Args(c, []string{"web"}) != nil {
			t.Fatalf("mw %s does not take the service as its argument (%q)", name, c.Use)
		}
	}
	// A subcommand still wins over a service of the same name.
	c, _, err := root.Find([]string{"env", "set"})
	if err != nil || c.Name() != "set" {
		t.Fatalf("mw env set: %v", err)
	}
	// The list alias of a group takes it too: `mw env list web`.
	l, _, err := root.Find([]string{"env", "list"})
	if err != nil || l.Args(l, []string{"web"}) != nil {
		t.Fatalf("mw env list web: %v", err)
	}
}

// A cron job's status printed "0 ready (min 0, max 3)" and never its schedule.
func TestACronJobsStatusSaysWhenItRuns(t *testing.T) {
	f, srv := newFakeAPI(t)
	tick := service("CRON", "IMAGE")
	settings := tick["settings"].(map[string]interface{})
	settings["cron_schedule"] = "*/2 * * * *"
	settings["cron_timezone"] = "America/Sao_Paulo"
	f.answer("GET /v1/serverless/services", 200, page(tick))
	out, _, err := run(t, srv, t.TempDir(), "status", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "*/2 * * * * (America/Sao_Paulo)") || strings.Contains(out, "Instances") {
		t.Fatalf("status %q", out)
	}
	// And -o json keeps the zone the API answered.
	out, _, err = run(t, srv, t.TempDir(), "status", "web", "-o", "json")
	if err != nil || !strings.Contains(out, `"cron_timezone": "America/Sao_Paulo"`) {
		t.Fatalf("json %q %v", out, err)
	}
}
