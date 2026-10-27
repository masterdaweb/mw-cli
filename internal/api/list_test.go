package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAListReadsTheWrappedAndTheBareShape(t *testing.T) {
	for _, raw := range []string{
		`{"object":"list","data":[{"id":"a"},{"id":"b"}]}`,
		`[{"id":"a"},{"id":"b"}]`,
	} {
		var l List[struct {
			ID string `json:"id"`
		}]
		if err := json.Unmarshal([]byte(raw), &l); err != nil || len(l.Data) != 2 || l.Data[1].ID != "b" {
			t.Fatalf("%s: %+v, %v", raw, l, err)
		}
	}
}

func TestTakeStopsAtWhatWasAskedFor(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"has_more":true,"next_cursor":"c2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"3"},{"id":"4"}],"has_more":false,"next_cursor":null}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "mwk_x", "test")
	type item struct {
		ID string `json:"id"`
	}
	got, err := Take[item](context.Background(), c, "/security/findings", nil, 3)
	if err != nil || len(got) != 3 || got[2].ID != "3" || len(calls) != 2 {
		t.Fatalf("3 of them: %+v, %v, calls %v", got, err, calls)
	}
	if calls[0] != "limit=3" {
		t.Errorf("first page asked %q", calls[0])
	}
	calls = nil
	all, err := Take[item](context.Background(), c, "/security/findings", nil, 0)
	if err != nil || len(all) != 4 || calls[0] != "limit=100" {
		t.Fatalf("all: %+v, %v, calls %v", all, err, calls)
	}
}
