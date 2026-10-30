// Package api is the Master da Web public API (v1) as the CLI uses it: a
// personal access token, an Idempotency-Key on every request that changes
// something (a retried timeout never does the work twice), and the API's
// RFC 9457 problems turned into errors that say what to do.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://apiv2.masterdaweb.com"

// Client talks to /v1 with one token.
type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
	// OnWait, when set, hears of each wait before a retry.
	OnWait func(time.Duration)
}

// New returns a client for a base URL (no trailing slash, no /v1) and token.
func New(baseURL, token, version string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		UserAgent: "mw-cli/" + version,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Problem is an API refusal (RFC 9457), with the product's own stable code
// (`reason`, e.g. DATABASE_IN_USE) and its parameters when there is one.
type Problem struct {
	Status    int                    `json:"status"`
	Code      string                 `json:"code"`
	Title     string                 `json:"title"`
	Detail    string                 `json:"detail"`
	Reason    string                 `json:"reason"`
	Params    map[string]interface{} `json:"params"`
	RequestID string                 `json:"request_id"`
	// Instance is the path of the request that failed.
	Instance string `json:"instance"`
	// Errors names the fields a request got wrong, each with why.
	Errors []FieldError `json:"errors"`
	// insufficient_balance: how much is missing, on which subscription.
	SubscriptionID string      `json:"subscription_id"`
	ShortfallCents json.Number `json:"shortfall_cents"`
}

// FieldError is one field of a refused request: `field` is its path in the
// body (app_inputs.licence_key), `issues` what is wrong with it.
type FieldError struct {
	Field   string   `json:"field"`
	Issues  []string `json:"issues"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
}

func (p *Problem) Error() string {
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	if msg == "" {
		msg = http.StatusText(p.Status)
	}
	return msg
}

// Is lets callers branch on a stable code: errors.Is(err, api.Code("DATABASE_IN_USE")).
func (p *Problem) Is(target error) bool {
	var c Code
	if errors.As(target, &c) {
		return string(c) == p.Code || string(c) == p.Reason
	}
	return false
}

// Code is a stable API code to compare a Problem with.
type Code string

func (c Code) Error() string { return string(c) }

// ErrUnauthorized is a missing or refused token.
var ErrUnauthorized = errors.New("not signed in, or the token is no longer valid: run `mw login`")

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Do sends one request. `in` is JSON-encoded when not nil; `out` is decoded
// from a 2xx answer when not nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, in, out interface{}) error {
	var raw []byte
	if in != nil {
		var err error
		if raw, err = json.Marshal(in); err != nil {
			return err
		}
	}
	return c.send(ctx, c.HTTP, method, path, query, func() (io.Reader, int64, string, func(), error) {
		if in == nil {
			return nil, 0, "", func() {}, nil
		}
		return bytes.NewReader(raw), int64(len(raw)), "application/json", func() {}, nil
	}, func(res *http.Response) error { return c.answer(res, out) })
}

// Upload sends a file as the raw body of a POST (a folder's .tar.gz). It may
// take long on a slow link: only ctx bounds it, not the client's timeout.
func (c *Client) Upload(ctx context.Context, path string, query url.Values, file string, contentType string, out interface{}) error {
	return c.SendFile(ctx, http.MethodPost, path, query, file, contentType, out)
}

// SendFile is Upload with another method: a file PUT into a volume, read
// from the disk as it goes, with its length.
func (c *Client) SendFile(ctx context.Context, method, path string, query url.Values, file string, contentType string, out interface{}) error {
	long := *c.HTTP
	long.Timeout = 0
	return c.send(ctx, &long, method, path, query, func() (io.Reader, int64, string, func(), error) {
		f, err := os.Open(file)
		if err != nil {
			return nil, 0, "", nil, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, "", nil, err
		}
		return f, st.Size(), contentType, func() { f.Close() }, nil
	}, func(res *http.Response) error { return c.answer(res, out) })
}

// Download writes the body of a GET to w as it comes (a volume's .tar.gz) and
// returns the name the API gives the file (Content-Disposition), "" without
// one. Like Upload, only ctx bounds it; a refusal is read as any other.
func (c *Client) Download(ctx context.Context, path string, query url.Values, w io.Writer) (string, error) {
	long := *c.HTTP
	long.Timeout = 0
	name := ""
	err := c.send(ctx, &long, http.MethodGet, path, query, func() (io.Reader, int64, string, func(), error) {
		return nil, 0, "", func() {}, nil
	}, func(res *http.Response) error {
		if res.StatusCode >= 300 {
			return c.answer(res, nil)
		}
		name = attachmentName(res.Header.Get("Content-Disposition"))
		n, err := io.Copy(w, res.Body)
		if err != nil {
			return fmt.Errorf("the download stopped after %d bytes: %w", n, err)
		}
		return nil
	})
	return name, err
}

// attachmentName is the file name a Content-Disposition gives, without the
// folders it may name: something to save under here, never a path.
func attachmentName(header string) string {
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	name := path.Base(strings.ReplaceAll(params["filename"], `\`, "/"))
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}

// Retries is how many times a request refused for its rate (429), or by an
// API that says when to come back (503 with Retry-After), is sent again.
var Retries = 4

// longestWait is the most one retry waits; a longer Retry-After is reported.
const longestWait = 60 * time.Second

// send makes a request, again after the wait the API asks for when it
// refuses for the rate. A change keeps its Idempotency-Key across attempts,
// so a retried request is never done twice. read takes the answer.
func (c *Client) send(ctx context.Context, h *http.Client, method, path string, query url.Values,
	body func() (io.Reader, int64, string, func(), error), read func(*http.Response) error) error {
	u := c.BaseURL + "/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	key := ""
	if method != http.MethodGet && method != http.MethodHead {
		key = newIdempotencyKey()
	}
	for attempt := 0; ; attempt++ {
		r, size, contentType, done, err := body()
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, u, r)
		if err != nil {
			done()
			return err
		}
		if r != nil {
			req.ContentLength = size
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.UserAgent)
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := h.Do(req)
		done()
		if err != nil {
			return fmt.Errorf("could not reach %s: %w", c.BaseURL, err)
		}
		if wait, ok := retryAfter(res, attempt); ok && attempt < Retries {
			res.Body.Close()
			if c.OnWait != nil {
				c.OnWait(wait)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		defer res.Body.Close()
		return read(res)
	}
}

// retryAfter is how long to wait before sending a refused request again, and
// whether to: a rate refusal always (the API's Retry-After, else a growing
// backoff), an unavailable API only when it says when.
func retryAfter(res *http.Response, attempt int) (time.Duration, bool) {
	header := strings.TrimSpace(res.Header.Get("Retry-After"))
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
	case res.StatusCode == http.StatusServiceUnavailable && header != "":
	default:
		return 0, false
	}
	wait := time.Duration(500<<attempt) * time.Millisecond
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	}
	if wait > longestWait {
		return 0, false
	}
	return wait, true
}

// answer reads a response: the decoded body of a 2xx, else the refusal.
func (c *Client) answer(res *http.Response, out interface{}) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if res.StatusCode >= 300 {
		p := &Problem{Status: res.StatusCode}
		if json.Unmarshal(raw, p) != nil || (p.Code == "" && p.Detail == "") {
			p.Detail = strings.TrimSpace(string(raw))
		}
		if p.RequestID == "" {
			p.RequestID = res.Header.Get("X-Request-Id")
		}
		return p
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from the API: %w", err)
	}
	return nil
}

// Page is a v1 list.
type Page[T any] struct {
	Data       []T    `json:"data"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// All follows a list's cursor to the end.
func All[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var out []T
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for {
		var p Page[T]
		if err := c.Do(ctx, http.MethodGet, path, q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if !p.HasMore || p.NextCursor == "" {
			return out, nil
		}
		q.Set("cursor", p.NextCursor)
	}
}
