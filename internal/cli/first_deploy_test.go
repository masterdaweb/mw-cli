package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masterdaweb/mw-cli/internal/api"
	"github.com/masterdaweb/mw-cli/internal/config"
)

const firstPID = "0193f0e2-2222-7000-8000-000000000009"

// appFolder is a folder named `name` holding files, with no service yet.
func appFolder(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	for file, content := range files {
		p := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// liveService is the service a first deployment makes: a web one, with its address.
func liveService() map[string]interface{} {
	s := service("WEB", "UPLOAD")
	s["url"] = "https://web-ab12cd.bh.meu-app.net"
	return s
}

// newAccount answers what making a service in a new project asks: the wallet,
// no project yet, the project and the service made.
func newAccount(f *fakeAPI) {
	f.answer("GET /v1/serverless/wallet", 200, wallet("60000000"))
	f.answer("GET /v1/serverless/projects", 200, page())
	f.answer("POST /v1/serverless/projects", 200, map[string]interface{}{"id": firstPID, "name": "minha-loja"})
	f.answer("POST /v1/serverless/projects/"+firstPID+"/services", 200, map[string]interface{}{"service": liveService()})
	f.answer("GET /v1/serverless/access", 200, map[string]interface{}{"enabled": true, "upload_max_bytes": 1 << 20})
}

// answers gives a route one answer after another, the last one from then on.
func (f *fakeAPI) answers(route string, bodies ...map[string]interface{}) {
	n := 0
	f.on(route, func(w http.ResponseWriter, _ call) {
		body := bodies[min(n, len(bodies)-1)]
		n++
		reply(w, 200, body)
	})
}

func step(kind, status string, ms int64) map[string]interface{} {
	s := map[string]interface{}{"kind": kind, "status": status}
	if ms > 0 {
		s["duration_ms"] = ms
	}
	return s
}

func TestAFirstDeployMakesTheServiceFromTheFolderAndSaysItsAddress(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "Minha Loja", map[string]string{
		"package.json": `{"name":"loja"}`, "server.js": "console.log(1)", "mw.toml": "[deploy]\nport = 3000\n",
		// Sorted ahead of mw.toml, which is still what the archive starts with.
		"app/routes.js": "1", "README.md": "loja", ".env.example": "A=1",
	})
	newAccount(f)
	const up = "POST /v1/serverless/services/" + svcID + "/uploads"
	f.answer(up, 200, map[string]interface{}{"id": "dep-1", "number": 1, "status": "QUEUED", "created_at": "2026-11-19T10:00:00Z", "steps": []interface{}{}})
	building := []interface{}{step("SOURCE", "SUCCEEDED", 400), step("BUILD", "RUNNING", 0)}
	f.answers("GET /v1/serverless/deployments/dep-1",
		map[string]interface{}{"id": "dep-1", "number": 1, "status": "BUILDING", "created_at": "2026-11-19T10:00:00Z", "steps": building},
		map[string]interface{}{"id": "dep-1", "number": 1, "status": "BUILDING", "created_at": "2026-11-19T10:00:00Z", "steps": building},
		map[string]interface{}{"id": "dep-1", "number": 1, "status": "BUILDING", "created_at": "2026-11-19T10:00:00Z", "steps": building},
		map[string]interface{}{"id": "dep-1", "number": 1, "status": "LIVE", "created_at": "2026-11-19T10:00:00Z", "finished_at": "2026-11-19T10:02:30Z",
			"steps": []interface{}{step("SOURCE", "SUCCEEDED", 400), step("BUILD", "SUCCEEDED", 125_000), step("HEALTH_CHECK", "SUCCEEDED", 9_000), step("TRAFFIC", "SUCCEEDED", 2_000)}},
	)
	f.answers("GET /v1/serverless/deployments/dep-1/build-log",
		map[string]interface{}{"state": "running", "log": "#1 load build definition\n#2 npm install\n#2 ...\n"},
		map[string]interface{}{"state": "running", "log": "#1 load build definition\n#2 npm install\n#2 ...\n\n#3 exporting to image\n"},
		map[string]interface{}{"state": "complete", "log": "#1 load build definition\n#2 npm install\n#3 exporting to image\n#3 DONE 4.1s\n"},
	)
	// Enter is yes.
	out, _, err := runWithInput(t, srv, dir, "\n", "deploy")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"No service was made from this folder yet.",
		"Found a Node.js app (package.json): built as detected, no Dockerfile needed. Its mw.toml sets the rest.",
		`Make the service "minha-loja" (web, in the project "minha-loja") and deploy this folder? [Y/n]`,
		"Service web created in project minha-loja, and this folder linked to it (.mw/link.json).",
		"Deployment #1 of web:",
		"  build         running\n  │ #1 load build definition\n  │ #2 npm install\n  │ #3 exporting to image\n  │ #3 DONE 4.1s\n  build         succeeded in 2m 05s\n",
		"  health check  succeeded in 9s\n",
		"Deployment #1 is live in 2m 30s: https://web-ab12cd.bh.meu-app.net\n",
		"Next: `mw logs -f`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "#2 ...") {
		t.Errorf("BuildKit's still-running lines were printed:\n%s", out)
	}
	// Named after the folder, a web service deployed from a folder.
	if body := jsonOf(t, f.last("POST /v1/serverless/projects").Body); body["name"] != "minha-loja" {
		t.Fatalf("project %v", body)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body)
	if body["name"] != "minha-loja" || body["kind"] != "WEB" || body["source"].(map[string]interface{})["kind"] != "UPLOAD" {
		t.Fatalf("service %v", body)
	}
	if l, at, _ := config.FindLink(dir); l == nil || l.ServiceID != svcID || at != dir {
		t.Fatalf("link %+v at %s", l, at)
	}
	// The settings file leads the archive, where the platform reads it.
	gz, err := gzip.NewReader(bytes.NewReader(f.last(up).Body))
	if err != nil {
		t.Fatal(err)
	}
	if h, err := tar.NewReader(gz).Next(); err != nil || h.Name != "mw.toml" {
		t.Fatalf("first in the archive: %v %v", h, err)
	}

	// A second `mw deploy` there is a plain deployment: nothing is asked or made.
	f.answer("GET /v1/serverless/services/"+svcID, 200, liveService())
	f.answers("GET /v1/serverless/deployments/dep-1",
		map[string]interface{}{"id": "dep-1", "number": 2, "status": "BUILDING", "steps": building},
		map[string]interface{}{"id": "dep-1", "number": 2, "status": "BUILDING", "steps": building},
		map[string]interface{}{"id": "dep-1", "number": 2, "status": "LIVE", "steps": []interface{}{step("BUILD", "SUCCEEDED", 125_000)}},
	)
	made := len(f.calls)
	out, _, err = run(t, srv, dir, "deploy", "--no-build-log")
	if err != nil || strings.Contains(out, "No service was made") || strings.Contains(out, "│") || !strings.Contains(out, "is live") {
		t.Fatalf("second deploy: %v\n%s", err, out)
	}
	for _, c := range f.calls[made:] {
		if c.Method == "POST" && !strings.HasSuffix(c.Path, "/uploads") {
			t.Fatalf("a second deploy made something: %s %s", c.Method, c.Path)
		}
		if strings.HasSuffix(c.Path, "/build-log") {
			t.Fatal("--no-build-log read the build log")
		}
	}
}

func TestAFirstDeployAsksAndMakesNothingWithoutAYes(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "loja", map[string]string{"notes.txt": "nothing a build knows"})
	newAccount(f)
	// Nobody to ask (a pipeline): refused, saying the three ways on.
	out, _, err := run(t, srv, dir, "deploy")
	if err == nil || !strings.Contains(err.Error(), "`mw deploy --yes`") || !strings.Contains(err.Error(), "`mw init`") || !strings.Contains(err.Error(), "--service") {
		t.Fatalf("no terminal: %v", err)
	}
	// What the folder is was said first, and that a build may not take it.
	if !strings.Contains(out, "Found no Dockerfile") || !strings.Contains(out, "a Dockerfile always works") {
		t.Fatalf("said %q", out)
	}
	// "n": nothing is made, and what else there is is said.
	out, _, err = runWithInput(t, srv, dir, "n\n", "deploy")
	if err != nil || !strings.Contains(out, "Nothing was made.") {
		t.Fatalf("declined: %v %q", err, out)
	}
	// -o json never asks.
	if _, _, err = run(t, srv, dir, "deploy", "-o", "json"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("json: %v", err)
	}
	for _, c := range f.calls {
		if c.Method != "GET" {
			t.Fatalf("something was made: %s %s", c.Method, c.Path)
		}
	}
	if l, _, _ := config.FindLink(dir); l != nil {
		t.Fatalf("linked: %+v", l)
	}
	// --yes makes it without a question.
	f.answer("POST /v1/serverless/services/"+svcID+"/uploads", 200, deployment("dep-1", 1, "QUEUED"))
	f.answer("GET /v1/serverless/deployments/dep-1", 200, deployment("dep-1", 1, "LIVE"))
	out, _, err = run(t, srv, dir, "deploy", "--yes")
	if err != nil || strings.Contains(out, "[Y/n]") || !strings.Contains(out, "Service web created in project minha-loja") || !strings.Contains(out, "is live") {
		t.Fatalf("--yes: %v %q", err, out)
	}
}

func TestANewServiceTheWalletCannotTakeIsRefusedBeforeAnythingIsMade(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "loja", map[string]string{"package.json": "{}"})
	f.answer("GET /v1/serverless/wallet", 200, wallet("4965217"))
	for _, args := range [][]string{{"init"}, {"deploy", "--yes"}} {
		_, _, err := run(t, srv, dir, args...)
		if err == nil {
			t.Fatalf("%v: made without credit", args)
		}
		for _, want := range []string{"a new service needs R$ 50,00 of credit in the wallet, and it has R$ 4,97", "`mw wallet topup 46`", "nothing was made"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: missing %q in %v", args, want, err)
			}
		}
	}
	for _, c := range f.calls {
		if c.Method != "GET" {
			t.Fatalf("something was made: %s %s", c.Method, c.Path)
		}
	}
	// The API's own refusal, when it comes to that, names the same amounts.
	err := &api.Problem{Status: 402, Detail: "Add credit to your serverless wallet to create a service.", Reason: "SERVERLESS_CREDIT_REQUIRED",
		Params: map[string]interface{}{"required_micro_cents": "50000000", "available_micro_cents": "4965217"}}
	if said := Describe(err); !strings.Contains(said, "A new service needs R$ 50,00 of credit in the wallet, and it has R$ 4,97") || !strings.Contains(said, "`mw wallet topup 46`") {
		t.Fatalf("said %q", said)
	}
}

func TestAProjectMadeForAServiceThatIsRefusedIsRemovedAgain(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "Minha Loja", map[string]string{"package.json": "{}"})
	newAccount(f)
	f.answer("POST /v1/serverless/projects/"+firstPID+"/services", 409, map[string]interface{}{
		"status": 409, "code": "conflict", "reason": "SERVERLESS_QUOTA_EXCEEDED",
		"detail": "This would reserve more vCPU than the account's quota allows.",
		"params": map[string]interface{}{"over": []map[string]interface{}{{"resource": "vcpu_millis", "limit": 4000, "used": 3000, "requested": 1500}}},
	})
	f.answer("PATCH /v1/serverless/projects/"+firstPID, 200, map[string]interface{}{"id": firstPID, "name": "minha-loja"})
	f.answer("DELETE /v1/serverless/projects/"+firstPID, 204, nil)
	out, _, err := run(t, srv, dir, "init")
	if err == nil {
		t.Fatal("made past the quota")
	}
	said := Describe(err)
	for _, want := range []string{"vCPU: 1 free of 4; this needs 1.5", "To reserve less: a smaller --size, fewer --max-instances"} {
		if !strings.Contains(said, want) {
			t.Errorf("missing %q in %q", want, said)
		}
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/projects/"+firstPID).Body); body["deletion_protection"] != false {
		t.Fatalf("protection %v", body)
	}
	if q := f.last("DELETE /v1/serverless/projects/" + firstPID).Query["confirm"]; len(q) != 1 || q[0] != "minha-loja" {
		t.Fatalf("confirmed %v", q)
	}
	if !strings.Contains(out, "The project minha-loja, made for it, was removed again.") {
		t.Fatalf("said %q", out)
	}
	if l, _, _ := config.FindLink(dir); l != nil {
		t.Fatalf("linked: %+v", l)
	}

	// The advice fits the limit passed: one project too many is not a matter of size.
	tooMany := &api.Problem{Status: 409, Detail: "The account's quota allows 5 projects.", Reason: "SERVERLESS_QUOTA_EXCEEDED",
		Params: map[string]interface{}{"resource": "projects", "limit": float64(5), "used": float64(5), "requested": float64(1)}}
	if said := Describe(tooMany); !strings.Contains(said, "projects: 5 of 5 in use") || !strings.Contains(said, "`mw init --project <name>`") || strings.Contains(said, "--size") {
		t.Fatalf("said %q", said)
	}

	// A project that was there before is never touched.
	f.answer("GET /v1/serverless/projects", 200, page(map[string]interface{}{"id": firstPID, "name": "minha-loja"}))
	before := len(f.calls)
	if _, _, err := run(t, srv, dir, "init"); err == nil {
		t.Fatal("made past the quota")
	}
	for _, c := range f.calls[before:] {
		if c.Method == "DELETE" || c.Method == "PATCH" {
			t.Fatalf("an existing project was touched: %s %s", c.Method, c.Path)
		}
	}
}

func TestInitTakesTheSizeAndTheInstancesTheQuotaCounts(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "loja", map[string]string{"Dockerfile": "FROM scratch"})
	newAccount(f)
	out, _, err := run(t, srv, dir, "init", "--size", "x-small", "--max-instances", "1", "--min-instances", "1")
	if err != nil {
		t.Fatal(err)
	}
	settings := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body)["settings"].(map[string]interface{})
	if settings["size"] != "x-small" || settings["max_instances"] != float64(1) || settings["min_instances"] != float64(1) {
		t.Fatalf("settings %v", settings)
	}
	if !strings.Contains(out, "Found a Dockerfile: the image is built from it.") {
		t.Fatalf("said %q", out)
	}
	// The port its Dockerfile exposes is the service's, unless one is given.
	exposes := appFolder(t, "api", map[string]string{"Dockerfile": "FROM node:22\nEXPOSE 9000\n"})
	if _, _, err := run(t, srv, exposes, "init"); err != nil {
		t.Fatal(err)
	}
	if s := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body)["settings"].(map[string]interface{}); s["port"] != float64(9000) {
		t.Fatalf("settings %v", s)
	}
	if _, _, err := run(t, srv, exposes, "init", "--force", "--port", "3000"); err != nil {
		t.Fatal(err)
	}
	if s := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body)["settings"].(map[string]interface{}); s["port"] != float64(3000) {
		t.Fatalf("settings %v", s)
	}
	// A worker listens nowhere: no port is sent for it.
	if _, _, err := run(t, srv, exposes, "init", "--force", "--kind", "worker"); err != nil {
		t.Fatal(err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body); body["settings"] != nil {
		t.Fatalf("worker settings %v", body["settings"])
	}
	// Scaling given to the wrong kind is said before anything is made.
	before := len(f.calls)
	for _, args := range [][]string{
		{"init", "--force", "--instances", "2"},
		{"init", "--force", "--kind", "worker", "--max-instances", "2"},
	} {
		if _, _, err := run(t, srv, dir, args...); err == nil || !strings.Contains(err.Error(), "--instances") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(f.calls) != before {
		t.Fatalf("asked the API: %v", f.calls[before:])
	}
	// A worker's instances.
	if _, _, err := run(t, srv, dir, "init", "--force", "--kind", "worker", "--instances", "2"); err != nil {
		t.Fatal(err)
	}
	if s := jsonOf(t, f.last("POST /v1/serverless/projects/"+firstPID+"/services").Body)["settings"].(map[string]interface{}); s["instances"] != float64(2) {
		t.Fatalf("worker settings %v", s)
	}
}

func TestAFollowOutlivesAnAPIThatStopsAnsweringForAWhile(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/deployments", 200, deployment("d9", 9, "BUILDING"))
	n := 0
	f.on("GET /v1/serverless/deployments/d9", func(w http.ResponseWriter, _ call) {
		// Away for longer than one request's own tries: a restart.
		if n++; n <= 2*(api.TransientRetries+1) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("Bad Gateway"))
			return
		}
		reply(w, 200, deployment("d9", 9, "LIVE"))
	})
	out, errOut, err := run(t, srv, t.TempDir(), "deploy", "--service", "web")
	if err != nil || !strings.Contains(out, "Deployment #9 is live") {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Count(errOut, "the API is not answering; the deployment goes on") != 1 || !strings.Contains(errOut, "the API answers again") {
		t.Fatalf("said %q", errOut)
	}

	// Past its patience it stops, saying the deployment goes on and how to pick it up.
	followPatience = 30 * time.Millisecond
	defer func() { followPatience = 5 * time.Minute }()
	f.on("GET /v1/serverless/deployments/d9", func(w http.ResponseWriter, _ call) { w.WriteHeader(502) })
	_, _, err = run(t, srv, t.TempDir(), "deploy", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "Deployment #9 goes on: `mw deployments follow` picks it up again") {
		t.Fatalf("got %v", err)
	}

	// A refusal is not waited on: it will not change by asking again.
	f.answer("GET /v1/serverless/deployments/d9", 403, map[string]interface{}{"status": 403, "code": "insufficient_scope", "detail": "The token cannot read deployments."})
	started := time.Now()
	_, _, err = run(t, srv, t.TempDir(), "deploy", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "cannot read deployments") || time.Since(started) > 5*time.Second {
		t.Fatalf("got %v after %s", err, time.Since(started))
	}
}

func TestInterruptingAFollowLeavesTheDeploymentAndSaysHowToPickItUp(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/deployments", 200, deployment("d9", 9, "BUILDING"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.on("GET /v1/serverless/deployments/d9", func(w http.ResponseWriter, _ call) {
		cancel() // Ctrl+C, while it builds
		reply(w, 200, deployment("d9", 9, "BUILDING"))
	})
	t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MW_TOKEN", "mwk_test")
	t.Setenv("MW_API_URL", srv.URL)
	pollEvery = 10 * time.Millisecond
	var out, errOut bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(""), Out: &out, Err: &errOut, Dir: t.TempDir()}
	root := NewRoot(a)
	root.SetArgs([]string{"deploy", "--service", "web"})
	err := root.ExecuteContext(ctx)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(exit.msg, "Stopped following: deployment #9 goes on.") || !strings.Contains(exit.msg, "`mw deployments follow`") || !strings.Contains(exit.msg, "`mw deployments cancel`") {
		t.Fatalf("said %q", exit.msg)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, "/cancel") {
			t.Fatal("the deployment was cancelled")
		}
	}
}

func TestAFailedBuildThatWasShownIsNotPrintedTwice(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/deployments", 200, deployment("d9", 9, "BUILDING"))
	building := map[string]interface{}{"id": "d9", "number": 9, "status": "BUILDING", "steps": []interface{}{step("BUILD", "RUNNING", 0)}}
	f.answers("GET /v1/serverless/deployments/d9", building, building, map[string]interface{}{
		"id": "d9", "number": 9, "status": "FAILED", "steps": []interface{}{step("BUILD", "FAILED", 31_000)},
		"failure": map[string]interface{}{"code": "BUILD_FAILED", "message": "The build failed.", "hint": "Fix the error and deploy again.", "log_tail": "npm ERR! missing script: build"},
	})
	f.answers("GET /v1/serverless/deployments/d9/build-log",
		map[string]interface{}{"state": "running", "log": "#4 npm run build\n"},
		map[string]interface{}{"state": "complete", "log": "#4 npm run build\nnpm ERR! missing script: build\n"},
	)
	out, _, err := run(t, srv, t.TempDir(), "deploy", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "deployment #9 failed: The build failed.\n  Fix the error and deploy again.") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "npm ERR!") || strings.Count(out, "npm ERR! missing script: build") != 1 || !strings.Contains(out, "  build         failed in 31s\n") {
		t.Fatalf("error %q\nout %s", err, out)
	}
}

func TestDeploymentsFollowPicksUpTheOneUnderWay(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(liveService()))
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d14", 14, "BUILDING"), deployment("d13", 13, "LIVE")))
	f.answer("GET /v1/serverless/deployments/d14", 200, deployment("d14", 14, "LIVE"))
	out, _, err := run(t, srv, t.TempDir(), "deployments", "follow", "--service", "web")
	if err != nil || !strings.Contains(out, "Deployment #14 of web:") || !strings.Contains(out, "Deployment #14 is live: https://web-ab12cd.bh.meu-app.net") {
		t.Fatalf("%v\n%s", err, out)
	}
	// With none under way, the latest says how it ended — and a failure exits non-zero.
	failed := deployment("d15", 15, "FAILED")
	failed["failure"] = map[string]interface{}{"code": "PORT_NOT_LISTENING", "message": "The app did not listen on port 8080."}
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(failed, deployment("d14", 14, "REPLACED")))
	if _, _, err = run(t, srv, t.TempDir(), "deployments", "follow", "--service", "web"); err == nil || !strings.Contains(err.Error(), "deployment #15 failed: The app did not listen on port 8080.") {
		t.Fatalf("got %v", err)
	}
	// By number; one that is not there is said.
	out, _, err = run(t, srv, t.TempDir(), "deployments", "follow", "14", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "deployment #14 ended replaced") || !strings.Contains(out, "Deployment #14 of web:") {
		t.Fatalf("by number: %v %s", err, out)
	}
	if _, _, err = run(t, srv, t.TempDir(), "deployments", "follow", "99", "--service", "web"); err == nil || !strings.Contains(err.Error(), "no deployment #99") {
		t.Fatalf("got %v", err)
	}
	// -o json: nothing but the deployment as it ended.
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d14", 14, "BUILDING")))
	out, _, err = run(t, srv, t.TempDir(), "deployments", "follow", "--service", "web", "-o", "json")
	if err != nil || jsonOf(t, []byte(out))["status"] != "LIVE" {
		t.Fatalf("json: %v %q", err, out)
	}
}

func TestOpenPrintsTheAddressWhereThereIsNoBrowser(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(liveService()))
	opened := ""
	was := openBrowser
	openBrowser = func(link string) error { opened = link; return nil }
	defer func() { openBrowser = was }()

	t.Setenv("MW_NO_BROWSER", "1")
	out, _, err := run(t, srv, t.TempDir(), "open", "--service", "web")
	if err != nil || out != "https://web-ab12cd.bh.meu-app.net\n" || opened != "" {
		t.Fatalf("%v %q opened %q", err, out, opened)
	}
	// A worker has no address.
	f.answer("GET /v1/serverless/services", 200, page(service("WORKER", "GIT")))
	if _, _, err = run(t, srv, t.TempDir(), "open", "--service", "web"); err == nil || !strings.Contains(err.Error(), "it has no address to open") {
		t.Fatalf("got %v", err)
	}
}

func TestWhatAFolderIsIsReadFromItsFiles(t *testing.T) {
	for _, c := range []struct {
		files      []string
		want       string
		recognized bool
	}{
		{[]string{"Dockerfile", "package.json"}, "Found a Dockerfile", true},
		{[]string{"package.json"}, "Found a Node.js app (package.json)", true},
		{[]string{"requirements.txt", "package.json"}, "Found a Python app (requirements.txt)", true},
		{[]string{"composer.json", "package.json"}, "Found a PHP app (composer.json)", true},
		{[]string{"go.mod"}, "Found a Go app (go.mod)", true},
		{[]string{"Loja.csproj"}, "Found a .NET app (Loja.csproj)", true},
		{[]string{"index.html"}, "Found a static site (index.html)", true},
		{[]string{"README.md"}, "Found no Dockerfile", false},
	} {
		files := map[string]string{}
		for _, name := range c.files {
			files[name] = "x"
		}
		got := detectApp(appFolder(t, "app", files))
		if !strings.HasPrefix(got.Text, c.want) || got.Recognized != c.recognized {
			t.Errorf("%v: %+v", c.files, got)
		}
	}
	// The port a Dockerfile exposes — its first, over TCP — is where the service listens.
	for content, want := range map[string]int{
		"FROM python:3.12\nEXPOSE 9000\nCMD [\"python\"]": 9000,
		"FROM x\nexpose 53/udp 8443/tcp 9000":             8443,
		"FROM x\nEXPOSE $PORT\n":                          0,
		"FROM x\n# EXPOSE 3000\nRUN echo EXPOSE 1\n":      0,
		"FROM scratch": 0,
	} {
		got := detectApp(appFolder(t, "app", map[string]string{"Dockerfile": content}))
		if got.Port != want || (want > 0) != strings.Contains(got.Text, "listens on the port it exposes") {
			t.Errorf("%q: %+v, want port %d", content, got, want)
		}
	}
	// A folder named like a marker is not one.
	dir := appFolder(t, "app", map[string]string{"package.json/x": "x"})
	if got := detectApp(dir); got.Recognized {
		t.Errorf("a folder taken for a file: %+v", got)
	}
}

func TestALogReadAgainGivesOnlyWhatIsNew(t *testing.T) {
	var tail logTail
	join := func(lines []string) string { return strings.Join(lines, "|") }
	if got := join(tail.next("a\nb\n#3 ...\n\nc")); got != "a|b|c" {
		t.Fatalf("first: %q", got)
	}
	if got := join(tail.next("a\nb\nc")); got != "" {
		t.Fatalf("nothing new: %q", got)
	}
	// A layer's every tick is left out; that it is done is said.
	layer := "#2 sha256:" + strings.Repeat("5f", 32)
	var ticks logTail
	if got := join(ticks.next(layer + " 0B / 8.85MB 0.2s\n" + layer + " 3.15MB / 8.85MB 0.5s\n" + layer + " 8.85MB / 8.85MB 1.5s done\n#2 extracting sha256:" + strings.Repeat("e2", 32) + "\n#2 DONE 2.5s")); got != layer+" 8.85MB / 8.85MB 1.5s done|#2 DONE 2.5s" {
		t.Fatalf("layers: %q", got)
	}
	// An image's import: what is copied and what it became, not every layer of every platform.
	var copied logTail
	digest := strings.Repeat("9b", 32)
	if got := join(copied.next("2026/11/19 04:31:19 Copying from docker.io/library/alpine@sha256:" + digest + " to web:1c55\n" +
		"2026/11/19 04:31:24 pushed blob: sha256:" + digest + "\n2026/11/19 04:31:24 existing blob: sha256:" + digest + "\n" +
		"2026/11/19 04:31:24 web@sha256:" + digest + ": digest: sha256:" + digest + " size: 566\n" +
		"2026/11/19 04:31:26 web:1c55: digest: sha256:" + digest + " size: 9226")); got != "2026/11/19 04:31:19 Copying from docker.io/library/alpine@sha256:"+digest+" to web:1c55|2026/11/19 04:31:26 web:1c55: digest: sha256:"+digest+" size: 9226" {
		t.Fatalf("import: %q", got)
	}
	// The same text with more at its end.
	if got := join(tail.next("a\nb\nc\nd\ne")); got != "d|e" {
		t.Fatalf("grown: %q", got)
	}
	// A window that moved: what was printed is found again by its last lines.
	if got := join(tail.next("c\nd\ne\nf")); got != "f" {
		t.Fatalf("moved: %q", got)
	}
	// A title that repeats is not taken for the place reached.
	tail = logTail{}
	tail.next("#6 extracting\n#8 install\n#6 extracting")
	if got := join(tail.next("#6 extracting\n#8 install\n#6 extracting\n#6 DONE\n#6 extracting\n#9 copy")); got != "#6 DONE|#6 extracting|#9 copy" {
		t.Fatalf("repeated: %q", got)
	}
	// Nothing printed is in it any more: its last lines, not all of it again.
	tail = logTail{}
	tail.next("x\ny")
	var long []string
	for i := 0; i < 500; i++ {
		long = append(long, "line "+strings.Repeat("z", i%7)+string(rune('a'+i%26))+time.Duration(i).String())
	}
	if got := tail.next(strings.Join(long, "\n")); len(got) != lostTail || got[len(got)-1] != long[len(long)-1] {
		t.Fatalf("lost: %d lines", len(got))
	}
}

func TestDurationsReadAsAPersonSaysThem(t *testing.T) {
	for d, want := range map[time.Duration]string{
		18 * time.Second: "18s", 125 * time.Second: "2m 05s", 120 * time.Second: "2m", 3725 * time.Second: "1h 02m", 400 * time.Millisecond: "0s",
	} {
		if got := durationText(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

// A second server, to prove a relative folder is read from where the command runs.
func TestAFolderGivenIsDeployedAsTheLinkedServiceOfWhereTheCommandRuns(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "loja", map[string]string{"dist/index.html": "<h1>oi</h1>", "src/app.js": "1"})
	if err := config.SaveLink(dir, config.Link{ServiceID: svcID, ServiceName: "web"}); err != nil {
		t.Fatal(err)
	}
	f.answer("GET /v1/serverless/services/"+svcID, 200, liveService())
	f.answer("GET /v1/serverless/access", 200, map[string]interface{}{"enabled": true, "upload_max_bytes": 1 << 20})
	const up = "POST /v1/serverless/services/" + svcID + "/uploads"
	f.answer(up, 200, deployment("dep-1", 3, "QUEUED"))
	f.answer("GET /v1/serverless/deployments/dep-1", 200, deployment("dep-1", 3, "LIVE"))
	if out, _, err := run(t, srv, dir, "deploy", "dist"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gz, err := gzip.NewReader(bytes.NewReader(f.last(up).Body))
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(gz).Next()
	if err != nil || h.Name != "index.html" {
		t.Fatalf("sent %v %v", h, err)
	}
	// Elsewhere, outside any linked folder: still the service of where the command runs.
	other := appFolder(t, "outra", map[string]string{"index.html": "x"})
	if out, _, err := run(t, srv, dir, "deploy", other); err != nil || strings.Contains(out, "No service was made") {
		t.Fatalf("%v\n%s", err, out)
	}
	_ = httptest.NewRecorder
}

func TestServicesUpdateChangesWhatTheServiceRunsWith(t *testing.T) {
	f, srv := newFakeAPI(t)
	dir := appFolder(t, "loja", map[string]string{"package.json": "{}"})
	if err := config.SaveLink(dir, config.Link{ServiceID: svcID, ServiceName: "web"}); err != nil {
		t.Fatal(err)
	}
	const patch = "PATCH /v1/serverless/services/" + svcID + "/settings"
	f.answer("GET /v1/serverless/services/"+svcID, 200, service("WEB", "UPLOAD"))
	f.answer(patch, 202, map[string]interface{}{"service": service("WEB", "UPLOAD"), "deployment": deployment("d5", 5, "QUEUED")})
	// The port the failed health check named, from the linked folder: no name needed.
	out, _, err := run(t, srv, dir, "services", "update", "--port", "4321", "--start-command", "node dist/main.js", "--health-check-path", "")
	if err != nil {
		t.Fatal(err)
	}
	body := jsonOf(t, f.last(patch).Body)
	settings := body["settings"].(map[string]interface{})
	if settings["port"] != float64(4321) || settings["start_command"] != "node dist/main.js" || body["deploy"] != true {
		t.Fatalf("sent %v", body)
	}
	// "" clears: sent as null, not as an empty command.
	if v, ok := settings["health_check_path"]; !ok || v != nil {
		t.Fatalf("health check path %v (sent: %v)", v, ok)
	}
	if !strings.Contains(out, "web saved. Applying with deployment #5: `mw deployments follow --service web` follows it.") {
		t.Fatalf("said %q", out)
	}
	// --no-deploy saves for the next one.
	f.answer(patch, 202, map[string]interface{}{"service": service("WEB", "UPLOAD"), "deployment": nil})
	out, _, err = run(t, srv, dir, "services", "update", "--release-command", "npx prisma migrate deploy", "--no-deploy")
	if err != nil || jsonOf(t, f.last(patch).Body)["deploy"] != false || !strings.Contains(out, "applied with the next deployment") {
		t.Fatalf("%v %q", err, out)
	}
	// A gradual rollout alone starts no deployment.
	if _, _, err = run(t, srv, dir, "services", "update", "--rollout", "10"); err != nil || jsonOf(t, f.last(patch).Body)["deploy"] != false {
		t.Fatalf("rollout: %v %v", err, jsonOf(t, f.last(patch).Body))
	}
	// With a setting beside it, the deployment that applies the setting is started.
	if _, _, err = run(t, srv, dir, "services", "update", "--rollout", "10", "--port", "3000"); err != nil || jsonOf(t, f.last(patch).Body)["deploy"] != true {
		t.Fatalf("rollout and port: %v %v", err, jsonOf(t, f.last(patch).Body))
	}
	if _, _, err = run(t, srv, dir, "services", "update"); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("got %v", err)
	}
}
