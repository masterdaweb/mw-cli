package cli

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strings"
)

// A database's extensions are the platform's to manage (`mw db extensions`):
// its owner may use them but not create, drop or comment on them, as on any
// managed PostgreSQL. A dump made elsewhere carries those statements, so an
// import leaves them out and checks instead that the database has each
// extension the dump needs.

var (
	tocExtension        = regexp.MustCompile(`^\d+; \d+ \d+ EXTENSION - (\S+)`)
	tocExtensionComment = regexp.MustCompile(`^\d+; \d+ \d+ COMMENT - EXTENSION (\S+)`)
	sqlCreateExtension  = regexp.MustCompile(`(?i)^CREATE EXTENSION (?:IF NOT EXISTS )?"?([^\s";]+)"?`)
	sqlCommentExtension = regexp.MustCompile(`(?i)^COMMENT ON EXTENSION `)
)

// builtIn extensions every database has.
var builtIn = map[string]bool{"plpgsql": true}

// filterArchiveList takes `pg_restore --list` and drops the extensions and
// their comments; the extensions the archive creates come back too.
func filterArchiveList(list string) (string, []string) {
	var kept strings.Builder
	exts := map[string]bool{}
	for _, line := range strings.Split(list, "\n") {
		if m := tocExtension.FindStringSubmatch(line); m != nil {
			exts[m[1]] = true
			continue
		}
		if m := tocExtensionComment.FindStringSubmatch(line); m != nil {
			exts[m[1]] = true
			continue
		}
		kept.WriteString(line)
		kept.WriteByte('\n')
	}
	return kept.String(), names(exts)
}

// filterSQL copies a plain dump without its CREATE EXTENSION and COMMENT ON
// EXTENSION statements (one comment may run over lines), and says which
// extensions it wanted.
func filterSQL(r io.Reader, w io.Writer) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	bw := bufio.NewWriter(w)
	exts := map[string]bool{}
	inComment := false
	for sc.Scan() {
		line := sc.Text()
		if inComment {
			inComment = !strings.HasSuffix(strings.TrimSpace(line), "';")
			continue
		}
		if m := sqlCreateExtension.FindStringSubmatch(line); m != nil {
			exts[m[1]] = true
			continue
		}
		if sqlCommentExtension.MatchString(line) {
			inComment = !strings.HasSuffix(strings.TrimSpace(line), "';")
			continue
		}
		if _, err := bw.WriteString(line + "\n"); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return names(exts), bw.Flush()
}

func names(set map[string]bool) []string {
	var out []string
	for n := range set {
		if !builtIn[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// missingExtensions are those wanted that the database does not have.
func missingExtensions(wanted, enabled []string) []string {
	on := map[string]bool{}
	for _, e := range enabled {
		on[e] = true
	}
	var out []string
	for _, w := range wanted {
		if !on[w] {
			out = append(out, w)
		}
	}
	return out
}
