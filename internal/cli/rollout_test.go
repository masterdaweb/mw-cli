package cli

import (
	"strings"
	"testing"
)

func rollingDeployment(id string, number int) map[string]interface{} {
	d := deployment(id, number, "DEPLOYING")
	d["rollout"] = map[string]interface{}{"percent": 10, "started_at": "2026-10-26T10:00:00Z", "until": "2026-10-26T10:10:00Z", "pending_action": nil}
	return d
}

func TestDeploymentsPromoteAndAbortTheOneRollingOut(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(rollingDeployment("d21", 21), deployment("d20", 20, "LIVE")))
	f.answer("POST /v1/serverless/deployments/d21/rollout/promote", 200, rollingDeployment("d21", 21))
	out, _, err := run(t, srv, t.TempDir(), "deployments", "promote", "--service", "web")
	if err != nil || !strings.Contains(out, "Deployment #21: its new version takes every request") {
		t.Fatalf("promote: %v %q", err, out)
	}
	f.answer("POST /v1/serverless/deployments/d21/rollout/abort", 200, rollingDeployment("d21", 21))
	if out, _, err := run(t, srv, t.TempDir(), "deployments", "abort", "21", "--service", "web"); err != nil || !strings.Contains(out, "every request goes back") {
		t.Fatalf("abort by number: %v %q", err, out)
	}
	// Nothing rolling out: said, with nothing sent.
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d20", 20, "LIVE")))
	if _, _, err := run(t, srv, t.TempDir(), "deployments", "promote", "--service", "web"); err == nil || !strings.Contains(err.Error(), "no deployment of web is rolling out") {
		t.Fatalf("none rolling out: %v", err)
	}
}

func TestDeploymentsBuildLogPrintsTheKeptLogOrFollowsOne(t *testing.T) {
	f, srv := newFakeAPI(t)
	built := deployment("d30", 30, "LIVE")
	built["steps"] = []interface{}{map[string]interface{}{"kind": "BUILD", "status": "SUCCEEDED"}}
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("GET /v1/serverless/services/"+svcID+"/deployments", 200, page(deployment("d31", 31, "LIVE"), built))
	f.answer("GET /v1/serverless/deployments/d30/build-log", 200, map[string]interface{}{
		"deployment_id": "d30", "state": "complete", "log": "building src/MwApp/MwApp.csproj (net10.0, a web app)\n#9 DONE", "truncated": false,
	})
	out, _, err := run(t, srv, t.TempDir(), "deployments", "build-log", "--service", "web")
	if err != nil || !strings.Contains(out, "building src/MwApp/MwApp.csproj") || !strings.Contains(out, "#9 DONE\n") {
		t.Fatalf("build-log: %v %q", err, out)
	}
	f.answer("GET /v1/serverless/deployments/d30/build-log", 200, map[string]interface{}{"deployment_id": "d30", "state": "none", "log": "", "truncated": false})
	if _, _, err := run(t, srv, t.TempDir(), "deployments", "build-log", "30", "--service", "web"); err == nil || !strings.Contains(err.Error(), "has no build log") {
		t.Fatalf("none: %v", err)
	}
}

func TestNewLinesPrintsOnlyWhatALiveLogAdded(t *testing.T) {
	cases := []struct{ before, now, want string }{
		{"", "a\nb", "a\nb\n"},
		{"a\nb", "a\nb\nc", "\nc\n"},
		// The live view dropped its first lines: found again by the last one printed.
		{"a\nb\nc", "b\nc\nd\ne", "d\ne\n"},
		{"a\nb", "x\ny", "x\ny\n"},
	}
	for _, c := range cases {
		if got := newLines(c.before, c.now); got != c.want {
			t.Errorf("newLines(%q, %q) = %q, want %q", c.before, c.now, got, c.want)
		}
	}
}

func TestServicesUpdateSetsTheRollout(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	saved := service("WEB", "GIT")
	saved["settings"] = map[string]interface{}{"rollout_percent": 10, "rollout_minutes": 15}
	f.answer("PATCH /v1/serverless/services/"+svcID+"/settings", 200, map[string]interface{}{"service": saved, "deployment": nil})
	out, _, err := run(t, srv, t.TempDir(), "services", "update", "web", "--rollout", "10", "--rollout-minutes", "15")
	if err != nil || !strings.Contains(out, "new versions take 10% of the requests for 15 minutes first") {
		t.Fatalf("rollout: %v %q", err, out)
	}
	body := jsonOf(t, f.last("PATCH /v1/serverless/services/"+svcID+"/settings").Body)
	settings, _ := body["settings"].(map[string]interface{})
	if body["deploy"] != false || settings["rollout_percent"] != float64(10) || settings["rollout_minutes"] != float64(15) {
		t.Fatalf("sent %v", body)
	}
	saved["settings"] = map[string]interface{}{"rollout_percent": nil, "rollout_minutes": 15}
	if out, _, err := run(t, srv, t.TempDir(), "services", "update", "web", "--rollout", "0"); err != nil || !strings.Contains(out, "every request at once") {
		t.Fatalf("off: %v %q", err, out)
	}
	if settings, _ := jsonOf(t, f.last("PATCH /v1/serverless/services/"+svcID+"/settings").Body)["settings"].(map[string]interface{}); settings["rollout_percent"] != nil {
		t.Fatalf("off sent %v", settings)
	}
}

func TestProjectsPreviewsTurnOnAndList(t *testing.T) {
	f, srv := newFakeAPI(t)
	const projID = "0193f0e2-2222-7000-8000-000000000002"
	proj := map[string]interface{}{"id": projID, "name": "loja", "previews_enabled": false}
	f.answer("GET /v1/serverless/projects/"+projID, 200, proj)
	f.answer("PATCH /v1/serverless/projects/"+projID, 200, map[string]interface{}{"id": projID, "name": "loja", "previews_enabled": true})
	out, _, err := run(t, srv, t.TempDir(), "projects", "update", projID, "--previews")
	if err != nil || !strings.Contains(out, "Preview environments on.") {
		t.Fatalf("update: %v %q", err, out)
	}
	if body := jsonOf(t, f.last("PATCH /v1/serverless/projects/"+projID).Body); body["previews_enabled"] != true {
		t.Fatalf("sent %v", body)
	}
	f.answer("GET /v1/serverless/projects/"+projID+"/previews", 200, []interface{}{})
	if out, _, err := run(t, srv, t.TempDir(), "projects", "previews", projID); err != nil || !strings.Contains(out, "are off for loja") {
		t.Fatalf("off: %v %q", err, out)
	}
	f.answer("GET /v1/serverless/projects/"+projID+"/previews", 200, []interface{}{map[string]interface{}{
		"project":  map[string]interface{}{"id": "p42", "name": "loja-pr-42", "preview": map[string]interface{}{"number": 42, "branch": "feature/checkout"}},
		"services": []interface{}{map[string]interface{}{"name": "web", "kind": "WEB", "status": "LIVE", "url": "https://web-pr42.mwapp.net"}},
	}})
	if out, _, err := run(t, srv, t.TempDir(), "projects", "previews", projID); err != nil || !strings.Contains(out, "#42") || !strings.Contains(out, "https://web-pr42.mwapp.net") {
		t.Fatalf("list: %v %q", err, out)
	}
}
