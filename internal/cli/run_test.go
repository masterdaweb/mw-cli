package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/masterdaweb/mw-cli/internal/api"
)

// wsServer is the platform's end of the WebSockets: handle runs per connection.
func wsServer(t *testing.T, handle func(ctx context.Context, c *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		handle(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestShellCommandQuotesOnlyWhatTheShellWouldSplit(t *testing.T) {
	cases := map[string][]string{
		"php artisan migrate --force": {"php", "artisan", "migrate", "--force"},
		"psql -c 'select 1'":          {"psql", "-c", "select 1"},
		`echo 'it'\''s'`:              {"echo", "it's"},
		"ls -la | head":               {"ls -la | head"},
		"":                            nil,
	}
	for want, args := range cases {
		if got := shellCommand(args); got != want {
			t.Errorf("%q: got %q, want %q", args, got, want)
		}
	}
}

func TestRunSendsTheInputThenItsEndAndExitsWithTheCommandsCode(t *testing.T) {
	var mu sync.Mutex
	var got []string
	url := wsServer(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			kind, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			if kind == websocket.MessageBinary {
				got = append(got, "in:"+string(data))
			} else {
				got = append(got, "ctl:"+string(data))
			}
			mu.Unlock()
			if kind == websocket.MessageText && strings.Contains(string(data), `"eof"`) {
				break
			}
		}
		_ = c.Write(ctx, websocket.MessageBinary, []byte("saída do comando\n"))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","code":3}`))
		c.Close(websocket.StatusNormalClosure, "ended")
	})
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/runs", 201, map[string]interface{}{
		"run":        map[string]interface{}{"id": "r1", "service_id": svcID, "status": "starting", "tty": false, "actor_kind": "api_token"},
		"attach_url": url + "/serverless/runs/attach/ticket",
		"expires_at": time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	out, errOut, err := runWithInput(t, srv, t.TempDir(), "select 1;\n", "run", "--service", "web", "--", "psql", "-c", "select 1")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 3 || exit.msg != "" {
		t.Fatalf("got %v", err)
	}
	if out != "saída do comando\n" || !strings.Contains(errOut, "Running psql -c 'select 1' in web") {
		t.Fatalf("out %q, err %q", out, errOut)
	}
	body := jsonOf(t, f.last("POST /v1/serverless/services/"+svcID+"/runs").Body)
	// Not a terminal here: no tty asked for, the input piped in, then its end.
	if body["command"] != "psql -c 'select 1'" || body["tty"] != false {
		t.Fatalf("sent %v", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, "|") != `in:select 1;`+"\n"+`|ctl:{"type":"eof"}` {
		t.Fatalf("the platform got %q", got)
	}
}

func TestRunSaysWhyTheCommandCouldNotStart(t *testing.T) {
	url := wsServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","code":"no_shell","message":"the service's image has no shell"}`))
		c.Close(websocket.StatusInternalError, "the command did not start")
	})
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/serverless/services", 200, page(service("WEB", "GIT")))
	f.answer("POST /v1/serverless/services/"+svcID+"/runs", 201, map[string]interface{}{
		"run": map[string]interface{}{"id": "r1"}, "attach_url": url, "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339),
	})
	_, _, err := run(t, srv, t.TempDir(), "run", "--service", "web")
	if err == nil || !strings.Contains(err.Error(), "has no shell (/bin/sh)") {
		t.Fatalf("got %v", err)
	}
	if body := jsonOf(t, f.last("POST /v1/serverless/services/"+svcID+"/runs").Body); body["command"] != nil {
		t.Fatalf("a shell was asked with a command: %v", body)
	}
}

// The platform's end of a tunnel: the "database" answers each chunk upper-cased.
func upperTunnel(t *testing.T) string {
	return wsServer(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if c.Write(ctx, websocket.MessageBinary, []byte(strings.ToUpper(string(data)))) != nil {
				return
			}
		}
	})
}

func tunnelAnswer(url string) map[string]interface{} {
	return map[string]interface{}{
		"url": url + "/databases/tunnel/ticket", "expires_at": time.Now().Add(time.Minute).Format(time.RFC3339),
		"hostname": "db1.db.mwapp.test", "port": 5432,
	}
}

func TestTunnelCarriesEachConnectionThroughATunnelOfItsOwn(t *testing.T) {
	url := upperTunnel(t)
	f, srv := newFakeAPI(t)
	f.answer("POST /v1/databases/loja-prod/tunnel", 200, tunnelAnswer(url))
	t.Setenv("MW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MW_TOKEN", "mwk_test")
	t.Setenv("MW_API_URL", srv.URL)
	a := &App{Version: "test", In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := &api.Database{Name: "loja-prod", Engine: "postgres", Status: "running", Connection: api.DatabaseConnection{Host: "db1.db.mwapp.test", Port: 5432}}
	r, err := a.listenTunnel(ctx, d, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if r.host != "db1.db.mwapp.test" || r.addr != "127.0.0.1" || !r.tunnel {
		t.Fatalf("route %+v", r)
	}
	for i, word := range []string{"startup", "segunda"} {
		conn, err := net.Dial("tcp", net.JoinHostPort(r.addr, itoa(r.port)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte(word)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil || string(buf[:n]) != strings.ToUpper(word) {
			t.Fatalf("connection %d: %q %v", i, buf[:n], err)
		}
		conn.Close()
	}
	// The first connection took the ticket asked for up front; the second, one of its own.
	f.mu.Lock()
	n := 0
	for _, c := range f.calls {
		if c.Method == "POST" && c.Path == "/v1/databases/loja-prod/tunnel" {
			n++
			if pooler := jsonOf(t, c.Body)["pooler"]; pooler != true {
				t.Errorf("pooler %v", pooler)
			}
		}
	}
	f.mu.Unlock()
	if n != 2 {
		t.Fatalf("%d tunnels asked for", n)
	}
}

func TestDbConnectGoesThroughThePlatformByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for psql")
	}
	url := upperTunnel(t)
	f, srv := newFakeAPI(t)
	f.answer("GET /v1/databases/loja-prod", 200, database("running"))
	f.answer("POST /v1/databases/loja-prod/reveal-password", 200, map[string]string{"password": "s3cr3t-pw"})
	f.answer("GET /v1/databases/ca-certificate", 200, map[string]interface{}{"ca_certificate": nil})
	f.answer("POST /v1/databases/loja-prod/tunnel", 200, tunnelAnswer(url))
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'psql (PostgreSQL) 17.2'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + record + "\nexit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, err := run(t, srv, t.TempDir(), "db", "connect", "loja-prod")
	var exit *exitError
	// Through the platform the address is not the question: no hint to allow it.
	if !errors.As(err, &exit) || exit.code != 2 || strings.Contains(exit.msg, "mw db allow") {
		t.Fatalf("got %v", err)
	}
	got, _ := os.ReadFile(record)
	dsn := strings.Split(string(got), "\n")[0]
	if !strings.HasPrefix(dsn, "host=db1.db.mwapp.test hostaddr=127.0.0.1 port=") || !strings.Contains(dsn, "dbname=loja_prod user=loja_prod sslmode=verify-full sslrootcert=system") {
		t.Fatalf("dsn %q", dsn)
	}
	if strings.Contains(dsn, "port=5432") {
		t.Fatalf("the database's own port: %q", dsn)
	}
	if body := jsonOf(t, f.last("POST /v1/databases/loja-prod/tunnel").Body); body["pooler"] != false {
		t.Fatalf("tunnel %v", body)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
