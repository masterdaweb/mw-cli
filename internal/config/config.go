// Package config is where the CLI keeps what it needs between runs: the API
// and the token (in the user's config directory, readable by them alone), and
// which service a project directory is linked to (`.mw/link.json`, meant to
// be committed or ignored as the team prefers — it holds no secret).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Credentials is the signed-in state.
type Credentials struct {
	APIURL string `json:"api_url,omitempty"`
	Token  string `json:"token,omitempty"`
	// Source is how the token came: "device" (`mw login`, a token of this
	// computer's own, revoked on logout) or "token" (a pasted personal
	// access token, maybe in use elsewhere too).
	Source string `json:"source,omitempty"`
}

// Link ties a directory to a service.
type Link struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
}

// Path is the credentials file: $MW_CONFIG, else <user config dir>/mw/config.json.
func Path() (string, error) {
	if p := os.Getenv("MW_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mw", "config.json"), nil
}

// Load reads the credentials; MW_TOKEN and MW_API_URL win over the file, so
// CI needs no file at all.
func Load() (Credentials, error) {
	var c Credentials
	p, err := Path()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, err
		}
	}
	if v := os.Getenv("MW_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("MW_API_URL"); v != "" {
		c.APIURL = v
	}
	return c, nil
}

// Save writes the credentials, readable by the user alone.
func Save(c Credentials) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Remove forgets the credentials.
func Remove() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

const linkFile = ".mw/link.json"

// FindLink looks for a link in dir and its parents (as git finds .git).
func FindLink(dir string) (*Link, string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		raw, err := os.ReadFile(filepath.Join(d, linkFile))
		if err == nil {
			var l Link
			if err := json.Unmarshal(raw, &l); err != nil {
				return nil, "", err
			}
			return &l, d, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		if filepath.Dir(d) == d {
			return nil, "", nil
		}
	}
}

// SaveLink writes dir/.mw/link.json.
func SaveLink(dir string, l Link) error {
	if err := os.MkdirAll(filepath.Join(dir, ".mw"), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, linkFile), append(raw, '\n'), 0o644)
}
