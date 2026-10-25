package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
