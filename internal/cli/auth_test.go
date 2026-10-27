package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masterdaweb/mw-cli/internal/config"
)

// authServer is an API with the OAuth device endpoints: the token request
// answers each of `answers` in turn, then the token.
type authServer struct {
	mu       sync.Mutex
	answers  []string
	revoked  []string
	forms    []map[string]string
	noDevice bool
	startErr *oauthReply // the device authorization's answer, when not a code
	*httptest.Server
}

type oauthReply struct {
	status            int
	code, description string
}

func newAuthServer(t *testing.T, answers ...string) *authServer {
	s := &authServer{answers: answers}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = r.ParseForm()
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/oauth/device_authorization":
			if s.noDevice {
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"statusCode": 404, "message": "Cannot POST /oauth/device_authorization"})
				return
			}
			s.forms = append(s.forms, form)
			if e := s.startErr; e != nil {
				reply(w, e.status, map[string]interface{}{"error": e.code, "error_description": e.description})
				return
			}
			reply(w, 200, map[string]interface{}{"device_code": "dev-123", "user_code": "ABCD-EFGH", "verification_uri": "https://console.example/device",
				"verification_uri_complete": "https://console.example/device?code=ABCD-EFGH", "expires_in": 600, "interval": 1})
		case r.Method == "POST" && r.URL.Path == "/oauth/token":
			s.forms = append(s.forms, form)
			if len(s.answers) > 0 {
				e := s.answers[0]
				s.answers = s.answers[1:]
				reply(w, 400, map[string]interface{}{"error": e})
				return
			}
			reply(w, 200, map[string]interface{}{"access_token": "mwk_device", "token_type": "Bearer", "scope": "vm:read"})
		case r.Method == "GET" && r.URL.Path == "/v1/me":
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if tok != "mwk_device" && tok != "mwk_pasted" {
				w.WriteHeader(401)
				return
			}
			reply(w, 200, map[string]interface{}{"object": "me", "account_id": "62", "token_id": "t1", "scopes": []string{"vm:read"}})
		case r.Method == "DELETE" && r.URL.Path == "/v1/me/token":
			s.revoked = append(s.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// runAuth runs `mw` with no token in the environment, so the saved one counts.
func runAuth(t *testing.T, srvURL, cfg, input string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("MW_CONFIG", cfg)
	t.Setenv("MW_TOKEN", "")
	t.Setenv("MW_API_URL", srvURL)
	t.Setenv("MW_NO_BROWSER", "1")
	pollEvery = 10 * time.Millisecond
	var out, errOut bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(input), Out: &out, Err: &errOut, Dir: t.TempDir()}
	root := NewRoot(a)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func savedCredentials(t *testing.T, cfg string) config.Credentials {
	t.Helper()
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var c config.Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginWithTheDeviceFlow(t *testing.T) {
	srv := newAuthServer(t, "authorization_pending", "slow_down", "authorization_pending")
	cfg := filepath.Join(t.TempDir(), "config.json")
	out, errOut, err := runAuth(t, srv.URL, cfg, "", "login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "ABCD-EFGH") || !strings.Contains(errOut, "https://console.example/device") {
		t.Errorf("the code and the link were not shown:\n%s", errOut)
	}
	if !strings.Contains(out, "Signed in to account 62") {
		t.Errorf("output: %s", out)
	}
	c := savedCredentials(t, cfg)
	if c.Token != "mwk_device" || c.Source != "device" || c.APIURL != srv.URL {
		t.Errorf("saved: %+v", c)
	}
	// RFC 8628's form fields, and the computer's name for the token.
	if start := srv.forms[0]; start["client_id"] != "mw-cli" || start["device_name"] == "" {
		t.Errorf("device authorization request: %v", start)
	}
	last := srv.forms[len(srv.forms)-1]
	if last["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" || last["device_code"] != "dev-123" || last["client_id"] != "mw-cli" {
		t.Errorf("token request: %v", last)
	}

	// Logout revokes a token the CLI got for itself.
	if _, _, err := runAuth(t, srv.URL, cfg, "", "logout"); err != nil {
		t.Fatal(err)
	}
	if len(srv.revoked) != 1 || srv.revoked[0] != "mwk_device" {
		t.Errorf("revoked: %v", srv.revoked)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Error("the credentials were kept")
	}
}

func TestLoginRefusedOrExpired(t *testing.T) {
	for answer, want := range map[string]string{"access_denied": "refused", "expired_token": "expired"} {
		srv := newAuthServer(t, answer)
		_, _, err := runAuth(t, srv.URL, filepath.Join(t.TempDir(), "c.json"), "", "login")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", answer, err)
		}
	}
}

func TestLoginFallsBackToAPastedToken(t *testing.T) {
	srv := newAuthServer(t)
	srv.noDevice = true
	cfg := filepath.Join(t.TempDir(), "config.json")
	out, _, err := runAuth(t, srv.URL, cfg, "mwk_pasted\n", "login")
	if err != nil || !strings.Contains(out, "Signed in") {
		t.Fatalf("fallback = %q, %v", out, err)
	}
	if c := savedCredentials(t, cfg); c.Token != "mwk_pasted" || c.Source != "token" {
		t.Errorf("saved: %+v", c)
	}
	// A pasted token may be in use elsewhere: logout forgets it, never revokes…
	if _, _, err := runAuth(t, srv.URL, cfg, "", "logout"); err != nil {
		t.Fatal(err)
	}
	if len(srv.revoked) != 0 {
		t.Errorf("revoked a pasted token: %v", srv.revoked)
	}
	// …unless asked.
	if _, _, err := runAuth(t, srv.URL, cfg, "mwk_pasted\n", "login", "--with-token"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runAuth(t, srv.URL, cfg, "", "logout", "--revoke"); err != nil {
		t.Fatal(err)
	}
	if len(srv.revoked) != 1 {
		t.Errorf("--revoke did not revoke: %v", srv.revoked)
	}
}

func TestLoginStartErrorsSayWhatToDo(t *testing.T) {
	for _, c := range []struct {
		reply oauthReply
		args  []string
		want  []string
	}{
		{oauthReply{429, "too_many_requests", "Too many sign-ins were started from this address: wait a few minutes and try again."}, nil,
			[]string{"wait a few minutes", "--with-token"}},
		{oauthReply{400, "invalid_scope", "Unknown scope(s): vm:fly."}, []string{"--scopes", "vm:fly"},
			[]string{"Unknown scope(s): vm:fly.", "Leave out --scopes"}},
		{oauthReply{401, "invalid_client", "Unknown client_id for a device sign-in."}, nil, []string{"--with-token"}},
	} {
		srv := newAuthServer(t)
		reply := c.reply
		srv.startErr = &reply
		_, _, err := runAuth(t, srv.URL, filepath.Join(t.TempDir(), "c.json"), "", append([]string{"login"}, c.args...)...)
		for _, want := range c.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v, want %q", c.reply.code, err, want)
			}
		}
	}
}

func TestLoginAsksForTheScopesGiven(t *testing.T) {
	srv := newAuthServer(t)
	if _, _, err := runAuth(t, srv.URL, filepath.Join(t.TempDir(), "c.json"), "", "login", "--scopes", "vm:read,dns:read"); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	if got := srv.forms[0]["scope"]; got != "vm:read dns:read" {
		t.Errorf("scope = %q", got)
	}
	srv.mu.Unlock()
	// Without --scopes none is sent: every scope the account can grant.
	all := newAuthServer(t)
	if _, _, err := runAuth(t, all.URL, filepath.Join(t.TempDir(), "c.json"), "", "login"); err != nil {
		t.Fatal(err)
	}
	all.mu.Lock()
	if _, sent := all.forms[0]["scope"]; sent {
		t.Errorf("sent a scope: %v", all.forms[0])
	}
	all.mu.Unlock()
}

func TestLoginWithAUsedCode(t *testing.T) {
	srv := newAuthServer(t, "invalid_grant")
	if _, _, err := runAuth(t, srv.URL, filepath.Join(t.TempDir(), "c.json"), "", "login"); err == nil || !strings.Contains(err.Error(), "no longer valid") {
		t.Errorf("invalid_grant: %v", err)
	}
}
