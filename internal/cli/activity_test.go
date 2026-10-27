package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func activityJSON(id, at, action, actorKind, actor, outcome string) map[string]interface{} {
	e := map[string]interface{}{"object": "activity", "id": id, "created_at": at, "action": action, "category": "instance", "significant": false,
		"actor":   map[string]interface{}{"kind": actorKind, "label": actor},
		"target":  map[string]interface{}{"type": "instance", "id": "24377", "label": "web-1"},
		"outcome": outcome, "status_code": 200, "error_message": nil, "request_id": "req_" + id, "ip": "203.0.113.5", "user_agent": "mw/1.0"}
	if outcome != "success" {
		e["error_message"], e["status_code"] = "Token missing required scope(s): vm:write", 403
	}
	return e
}

func TestActivityTableAndFilters(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/activity", 200, page(
		activityJSON("a2", "2026-10-26T12:05:00Z", "instance.delete", "API_TOKEN", "deploy", "denied"),
		activityJSON("a1", "2026-10-26T12:00:00Z", "instance.power.reboot", "CONSOLE", "ana@example.com", "success"),
	))
	out, _, err := run(t, srv, t.TempDir(), "activity", "--category", "instance", "--actor", "api-token", "--outcome", "denied",
		"--target-type", "instance", "--target-id", "24377", "--search", "web", "--since", "24h", "--until", "2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ana@example.com (console)", "instance.power.reboot", "instance web-1", "deploy (API token)", "denied: Token missing required scope(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	q := url.Values(f.last("GET /v1/activity").Query)
	for key, want := range map[string]string{"category": "instance", "actor_kind": "API_TOKEN", "outcome": "denied", "target_type": "instance", "target_id": "24377", "search": "web", "limit": "50"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil || time.Since(from) < 23*time.Hour || time.Since(from) > 25*time.Hour {
		t.Errorf("from = %q", q.Get("from"))
	}
	want, _ := time.ParseInLocation("2006-01-02", "2026-10-26", time.Local)
	if q.Get("to") != want.UTC().Format(time.RFC3339) {
		t.Errorf("to = %q", q.Get("to"))
	}
	for _, bad := range [][]string{{"--category", "food"}, {"--actor", "robot"}, {"--outcome", "maybe"}, {"--since", "yesterday"}} {
		if _, _, err := run(t, srv, t.TempDir(), append([]string{"activity"}, bad...)...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}

	// --limit pages through the cursor.
	f.on("GET /v1/activity", func(w http.ResponseWriter, c call) {
		if url.Values(c.Query).Get("cursor") == "c2" {
			reply(w, 200, page(activityJSON("a1", "2026-10-26T12:00:00Z", "x.three", "SYSTEM", "system", "success"), activityJSON("a0", "2026-10-26T11:00:00Z", "x.four", "SYSTEM", "system", "success")))
			return
		}
		p := page(activityJSON("a3", "2026-10-26T12:10:00Z", "x.one", "SYSTEM", "system", "success"), activityJSON("a2", "2026-10-26T12:05:00Z", "x.two", "SYSTEM", "system", "success"))
		p["has_more"], p["next_cursor"] = true, "c2"
		reply(w, 200, p)
	})
	out, _, err = run(t, srv, t.TempDir(), "activity", "--limit", "3", "-q")
	if err != nil || out != "a3\na2\na1\n" {
		t.Errorf("--limit 3 -q = %q, %v", out, err)
	}
}

// runCtx is run with the caller's context: --follow stops when it is done.
func runCtx(t *testing.T, ctx context.Context, srv *httptest.Server, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MW_TOKEN", "mwk_test")
	t.Setenv("MW_API_URL", srv.URL)
	var out, errOut bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(""), Out: &out, Err: &errOut, Dir: t.TempDir()}
	root := NewRoot(a)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func TestActivityFollowPrintsWhatIsNew(t *testing.T) {
	old := followEvery
	followEvery = 10 * time.Millisecond
	t.Cleanup(func() { followEvery = old })
	e1 := activityJSON("a1", "2026-10-26T12:00:00Z", "instance.power.start", "CONSOLE", "ana@example.com", "success")
	e2 := activityJSON("a2", "2026-10-26T12:01:00Z", "instance.power.reboot", "CONSOLE", "ana@example.com", "success")
	e3 := activityJSON("a3", "2026-10-26T12:02:00Z", "instance.delete", "API_TOKEN", "deploy", "denied")

	for _, format := range []string{"table", "json"} {
		f, srv := newFakeAPI(t)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		f.on("GET /v1/activity", func(w http.ResponseWriter, c call) {
			f.mu.Lock()
			calls++
			n := calls
			f.mu.Unlock()
			switch {
			case n == 1:
				reply(w, 200, page(e2, e1)) // newest first
			case n == 2:
				reply(w, 200, page(e3, e2, e1)) // a3 is new
			default:
				cancel()
				reply(w, 200, page(e3, e2, e1))
			}
		})
		out, _, err := runCtx(t, ctx, srv, "activity", "--follow", "-o", format)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if format == "json" {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var ids []string
			for _, l := range lines {
				var e struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(l), &e) != nil {
					t.Fatalf("not one JSON document a line: %q", l)
				}
				ids = append(ids, e.ID)
			}
			if strings.Join(ids, ",") != "a1,a2,a3" {
				t.Errorf("json follow: %v", ids)
			}
			continue
		}
		i1, i2, i3 := strings.Index(out, "instance.power.start"), strings.Index(out, "instance.power.reboot"), strings.Index(out, "instance.delete")
		if !strings.HasPrefix(out, "TIME") || i1 < 0 || !(i1 < i2 && i2 < i3) || strings.Count(out, "instance.delete") != 1 {
			t.Errorf("follow, oldest first and each once:\n%s", out)
		}
	}
}
