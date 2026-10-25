package archive

import (
	"bufio"
	"os"
	"path"
	"regexp"
	"strings"
)

// rule is one line of an ignore file, as git reads it.
type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	// base is the directory the file sits in ("" at the root): a rule only
	// speaks of paths under it.
	base string
}

// parseIgnore reads an ignore file's rules. A missing file has none.
func parseIgnore(file, base string) ([]rule, error) {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rules []rule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if r, ok := parseRule(sc.Text(), base); ok {
			rules = append(rules, r)
		}
	}
	return rules, sc.Err()
}

func parseRule(line, base string) (rule, bool) {
	line = strings.TrimRight(line, "\r")
	// Trailing spaces are dropped unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	r := rule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	// A slash anywhere but the end anchors the pattern to the file's
	// directory; without one it matches a name at any depth.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	expr := globToRegexp(line)
	if !anchored {
		expr = `(?:.*/)?` + expr
	}
	re, err := regexp.Compile(`^` + expr + `$`)
	if err != nil {
		return rule{}, false
	}
	r.re = re
	return r, true
}

// globToRegexp turns a gitignore glob into a regular expression: `*` and `?`
// stop at a slash, `**` crosses them, `[...]` is a character class.
func globToRegexp(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && strings.HasPrefix(g[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 2
		case c == '*' && strings.HasPrefix(g[i:], "**"):
			b.WriteString(`.*`)
			i++
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		case c == '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(g):
			i++
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// ignored says whether rel (slash-separated, from the root) is left out: the
// last rule that matches decides, as in git.
func ignored(rules []rule, rel string, dir bool) bool {
	out := false
	for _, r := range rules {
		p := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			p = strings.TrimPrefix(rel, r.base+"/")
		}
		if r.dirOnly && !dir {
			continue
		}
		if r.re.MatchString(p) {
			out = !r.negate
		}
	}
	return out
}

// alwaysLeftOut are never sent: the repository's history, the CLI's own
// link, and macOS's folder metadata.
func alwaysLeftOut(rel string) bool {
	switch path.Base(rel) {
	case ".git", ".mw", ".DS_Store":
		return true
	}
	return false
}
