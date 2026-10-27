package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// List is a v1 list that is not paged — {"object": "list", "data": […]} —
// read the same when an answer is the bare array.
type List[T any] struct {
	Data []T
}

func (l *List[T]) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		return json.Unmarshal(raw, &l.Data)
	}
	var wrapped struct {
		Data []T `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return err
	}
	l.Data = wrapped.Data
	return nil
}

// Take follows a list's cursor until it has n items (0: all of them).
func Take[T any](ctx context.Context, c *Client, path string, query url.Values, n int) ([]T, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	size := n
	if size <= 0 || size > 100 {
		size = 100
	}
	q.Set("limit", strconv.Itoa(size))
	var out []T
	for {
		var p Page[T]
		if err := c.Do(ctx, http.MethodGet, path, q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if (n > 0 && len(out) >= n) || !p.HasMore || p.NextCursor == "" {
			break
		}
		q.Set("cursor", p.NextCursor)
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out, nil
}
