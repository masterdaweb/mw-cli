package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Signing in from a terminal: the OAuth 2.0 Device Authorization Grant
// (RFC 8628) — the flow `gh auth login` and `az login --use-device-code`
// use. The CLI asks for a code, the person approves it in the console (on any
// device, so it works over SSH too), and the CLI receives a token of its own,
// named after the computer and revocable under Settings → API tokens.

// CLIClientID is the CLI's OAuth client.
const CLIClientID = "mw-cli"

// DeviceGrantType is the token request's grant_type for a device code.
const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceAuthorization is the answer to POST /oauth/device_authorization.
type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse is a successful POST /oauth/token.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int    `json:"expires_in"`
}

// OAuthError is an RFC 6749 §5.2 error: `authorization_pending`,
// `slow_down`, `access_denied`, `expired_token`, …
type OAuthError struct {
	Status      int    `json:"-"`
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	if e.Code != "" {
		return e.Code
	}
	return http.StatusText(e.Status)
}

// StartDeviceAuthorization asks for a device code and the code a person types.
// deviceName (the computer's name) names the token the approval creates, so
// the person can tell their machines apart under Settings → API tokens.
func (c *Client) StartDeviceAuthorization(ctx context.Context, clientID, scope, deviceName string) (*DeviceAuthorization, error) {
	form := url.Values{"client_id": {clientID}}
	if scope != "" {
		form.Set("scope", scope)
	}
	if deviceName != "" {
		form.Set("device_name", deviceName)
	}
	var out DeviceAuthorization
	if err := c.oauthPost(ctx, "/oauth/device_authorization", form, &out); err != nil {
		return nil, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return nil, fmt.Errorf("the API did not start a sign-in (no device code)")
	}
	return &out, nil
}

// PollDeviceToken asks once whether the code was approved. An *OAuthError
// with Code authorization_pending or slow_down means "not yet".
func (c *Client) PollDeviceToken(ctx context.Context, clientID, deviceCode string) (*TokenResponse, error) {
	form := url.Values{
		"grant_type":  {DeviceGrantType},
		"device_code": {deviceCode},
		"client_id":   {clientID},
	}
	var out TokenResponse
	if err := c.oauthPost(ctx, "/oauth/token", form, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("the API approved the sign-in but sent no token")
	}
	return &out, nil
}

// oauthPost posts a form to an OAuth endpoint (no /v1 prefix, no token).
func (c *Client) oauthPost(ctx context.Context, path string, form url.Values, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", c.BaseURL, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		e := &OAuthError{Status: res.StatusCode}
		if json.Unmarshal(raw, e) != nil || e.Code == "" {
			// Not an OAuth error: an API without the endpoint (404), a proxy.
			p := &Problem{Status: res.StatusCode}
			if json.Unmarshal(raw, p) != nil || (p.Code == "" && p.Detail == "") {
				p.Detail = strings.TrimSpace(string(raw))
			}
			if p.Detail == "" && p.Title == "" {
				p.Detail = http.StatusText(res.StatusCode)
			}
			return p
		}
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
