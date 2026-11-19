package cli

import (
	"strings"
	"testing"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// The revisions table printed the platform registry's address as a
// revision's source (registry.platform.internal/…, bh-dev, 2026-11-15).
func TestRevisionSourceNeverNamesTheRegistry(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		rev  api.Revision
		want string
	}{
		{"a commit", api.Revision{CommitSHA: str("0123456789abcdef"), Image: str("registry.platform.internal/p-1/web@" + digest)}, shortSHA("0123456789abcdef")},
		{"the digest the API now sends", api.Revision{Image: str("p-1/web@" + digest), Digest: str(digest)}, digest[:19]},
		{"an older API: the full reference", api.Revision{Image: str("registry.platform.internal/p-1/web@" + digest)}, digest[:19]},
		{"nothing to say", api.Revision{}, ""},
	}
	for _, c := range cases {
		got := revisionSource(c.rev)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "registry.") {
			t.Errorf("%s: %q names the registry", c.name, got)
		}
	}
}
