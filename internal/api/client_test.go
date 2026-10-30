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
	"sync"
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
	if out.Name != "loja" || len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] || len(waits) != 2 {
		t.Fatalf("name %q, keys %v, waits %v", out.Name, keys, waits)
	}
}

func TestWhatIsNotARateRefusalIsNotRetried(t *testing.T) {
	for _, c := range []struct {
		status     int
		retryAfter string
	}{{503, ""}, {500, "1"}, {429, "3600"}, {409, ""}} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if c.retryAfter != "" {
				w.Header().Set("Retry-After", c.retryAfter)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"code":"x","detail":"refused","reason":"SOMETHING"}`))
		}))
		err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "GET", "/me", nil, nil, nil)
		srv.Close()
		var p *Problem
		if !errors.As(err, &p) || p.Status != c.status || calls != 1 {
			t.Errorf("%d (Retry-After %q): %v after %d call(s)", c.status, c.retryAfter, err, calls)
		}
		if !errors.Is(err, Code("SOMETHING")) {
			t.Errorf("%d: the reason does not match: %v", c.status, err)
		}
	}
}

func TestAnUnavailableAPIThatSaysWhenIsRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if err := New(srv.URL, "mwk_x", "test").Do(context.Background(), "DELETE", "/serverless/domains/x", nil, nil, nil); err != nil || calls != 2 {
		t.Fatalf("%v after %d calls", err, calls)
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
