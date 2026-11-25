package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestARateRefusalIsRetriedWithTheSameIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"code":"rate_limited","detail":"Rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"loja"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "mwk_x", "test")
	var waits []time.Duration
	c.OnWait = func(d time.Duration) { waits = append(waits, d) }
	var out struct{ Name string }
	if err := c.Do(context.Background(), "POST", "/serverless/projects", nil, map[string]string{"name": "loja"}, &out); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if out.Name != "loja" || len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] || len(waits) != 2 {
		t.Fatalf("name %q, keys %v, waits %v", out.Name, keys, waits)
	}
}

func TestWhatIsNotARateRefusalIsNotRetried(t *testing.T) {
	for _, c := range []struct {
		method     string
		status     int
		retryAfter string
	}{
		{"GET", 500, "1"}, {"GET", 429, "3600"}, {"GET", 409, ""},
		// A change is never sent again on a gateway's answer: only a read is.
		{"POST", 503, ""}, {"POST", 502, ""}, {"DELETE", 504, ""},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if c.retryAfter != "" {
				w.Header().Set("Retry-After", c.retryAfter)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":"x","detail":"refused","reason":"SOMETHING"}`))
		}))
		err := New(srv.URL, "mwk_x", "test").Do(context.Background(), c.method, "/me", nil, nil, nil)
		srv.Close()
		var p *Problem
		if !errors.As(err, &p) || p.Status != c.status || calls.Load() != 1 {
			t.Errorf("%s %d (Retry-After %q): %v after %d call(s)", c.method, c.status, c.retryAfter, err, calls.Load())
		}
		if !errors.Is(err, Code("SOMETHING")) {
			t.Errorf("%d: the reason does not match: %v", c.status, err)
		}
	}
}

func TestAReadThatMeetsAGatewayIsAskedAgain(t *testing.T) {
	TransientWait = time.Millisecond
	defer func() { TransientWait = 500 * time.Millisecond }()
	for _, status := range []int{502, 503, 504} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) < 3 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("Bad Gateway"))
				return
			}
			_, _ = w.Write([]byte(`{"name":"loja"}`))
		}))
		var out struct{ Name string }
		err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "GET", "/me", nil, nil, &out)
		srv.Close()
		if err != nil || out.Name != "loja" || calls.Load() != 3 {
			t.Errorf("%d: %v, %q after %d call(s)", status, err, out.Name, calls.Load())
		}
	}
	// One that stays away is reported as it is, after the tries.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(502)
	}))
	defer srv.Close()
	err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "GET", "/me", nil, nil, nil)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 502 || int(calls.Load()) != TransientRetries+1 {
		t.Fatalf("%v after %d call(s)", err, calls.Load())
	}
}

func TestARequestThatRanOutOfTimeIsNotAskedAgain(t *testing.T) {
	TransientWait = time.Millisecond
	defer func() { TransientWait = 500 * time.Millisecond }()
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c := New(srv.URL, "mwk_x", "test")
	c.HTTP = &http.Client{Timeout: 50 * time.Millisecond}
	started := time.Now()
	err := c.Do(context.Background(), "GET", "/me", nil, nil, nil)
	// Whoever asked has waited the limit once, not four times over.
	if err == nil || calls.Load() != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("%v after %d call(s) and %s", err, calls.Load(), time.Since(started))
	}
}

func TestAConnectionThatDropsIsTriedAgainForAReadOnly(t *testing.T) {
	TransientWait = time.Millisecond
	defer func() { TransientWait = 500 * time.Millisecond }()
	for _, method := range []string{"GET", "POST"} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				// The connection closed with no answer.
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		err := New(srv.URL, "mwk_x", "test").Do(context.Background(), method, "/me", nil, nil, nil)
		srv.Close()
		if method == "GET" && (err != nil || calls.Load() != 2) {
			t.Errorf("GET: %v after %d call(s)", err, calls.Load())
		}
		if method == "POST" && (err == nil || calls.Load() != 1) {
			t.Errorf("POST: %v after %d call(s)", err, calls.Load())
		}
	}
}

func TestAnUnavailableAPIThatSaysWhenIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "DELETE", "/serverless/domains/x", nil, nil, nil); err != nil || calls.Load() != 2 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

func TestAnExpiredTokenSaysToSignInAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	if err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "GET", "/me", nil, nil, nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestDownloadWritesTheBodyAsItComesAndNamesTheFile(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") == "missing" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","reason":"SERVERLESS_VOLUME_PATH_NOT_FOUND","detail":"No such file or folder in the volume."}`))
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/gzip")
		// A name that tries to leave the folder it is saved in keeps its last part.
		w.Header().Set("Content-Disposition", `attachment; filename="../../uploads.tar.gz"`)
		_, _ = w.Write([]byte("archive bytes"))
	}))
	defer srv.Close()
	c := New(srv.URL, "mwk_x", "test")
	c.HTTP.Timeout = time.Nanosecond // a download is bounded by its context alone
	var got bytes.Buffer
	name, err := c.Download(context.Background(), "/serverless/services/s1/volume/archive", url.Values{"path": {"uploads"}}, &got)
	if err != nil || name != "uploads.tar.gz" || got.String() != "archive bytes" || auth != "Bearer mwk_x" {
		t.Fatalf("name %q, body %q, auth %q, %v", name, got.String(), auth, err)
	}
	got.Reset()
	_, err = c.Download(context.Background(), "/serverless/services/s1/volume/archive", url.Values{"path": {"missing"}}, &got)
	if !errors.Is(err, Code("SERVERLESS_VOLUME_PATH_NOT_FOUND")) || got.Len() != 0 {
		t.Fatalf("a refusal: %v, wrote %q", err, got.String())
	}
}

func TestAttachmentNameIsAFileNameOnly(t *testing.T) {
	for header, want := range map[string]string{
		`attachment; filename="web-volume.tar.gz"`: "web-volume.tar.gz",
		`attachment; filename="a/b/c.tar.gz"`:      "c.tar.gz",
		`attachment; filename="..\\..\\x.tar.gz"`:  "x.tar.gz",
		`attachment; filename=".."`:                "",
		`attachment`:                               "",
		``:                                         "",
	} {
		if got := attachmentName(header); got != want {
			t.Errorf("%s: %q, want %q", header, got, want)
		}
	}
}

func TestSendFileSendsTheFileWithItsLengthAndMethod(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(file, []byte("SQLite format 3"), 0o600); err != nil {
		t.Fatal(err)
	}
	var method, query, contentType, key string
	var length int64
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, query, contentType, key, length = r.Method, r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key"), r.ContentLength
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(srv.URL, "mwk_x", "test")
	q := url.Values{"path": {"data/app.db"}, "as": {"file"}}
	if err := c.SendFile(context.Background(), "PUT", "/serverless/services/s1/volume/files", q, file, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || query != "as=file&path=data%2Fapp.db" || contentType != "application/octet-stream" || key == "" ||
		length != 15 || string(body) != "SQLite format 3" {
		t.Fatalf("%s ?%s %s key %q length %d body %q", method, query, contentType, key, length, body)
	}
}

// Production, 2026-11-25: during an API deploy, `mw db` printed Cloudflare's
// whole 502 page as its error, after giving up in 3.5 s.
func TestAGatewaysPageIsWordedByItsStatusNeverPrinted(t *testing.T) {
	TransientWait = time.Millisecond
	defer func() { TransientWait = 500 * time.Millisecond }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Header().Set("Cf-Ray", "a5fe0671a987016e-GRU")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<!doctype html><html><head><style>:root{}</style></head><body>Bad gateway</body></html>"))
	}))
	defer srv.Close()
	c := New(srv.URL, "mwk_x", "test")
	var waits []time.Duration
	c.OnUnavailable = func(d time.Duration) { waits = append(waits, d) }
	err := c.Do(context.Background(), "GET", "/databases", nil, nil, nil)
	var p *Problem
	if !errors.As(err, &p) || strings.Contains(err.Error(), "<") || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error %q", err)
	}
	if p.RequestID != "a5fe0671a987016e-GRU" {
		t.Fatalf("request id %q", p.RequestID)
	}
	if len(waits) != TransientRetries {
		t.Fatalf("%d waits heard, want %d", len(waits), TransientRetries)
	}
	// About half a minute in all, at the real first wait.
	total := time.Duration(0)
	for i := 0; i < TransientRetries; i++ {
		total += (500 * time.Millisecond) << i
	}
	if total < 25*time.Second || total > 40*time.Second {
		t.Fatalf("a deploy outlasts the retries: %s", total)
	}
}

func TestAShortPlainRefusalIsKeptAsItCame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(418)
		_, _ = w.Write([]byte("short and plain"))
	}))
	defer srv.Close()
	err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "GET", "/x", nil, nil, nil)
	if err == nil || err.Error() != "short and plain" {
		t.Fatalf("error %q", err)
	}
}
