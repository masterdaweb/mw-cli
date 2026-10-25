package archive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// entries lists the regular files in an archive, and checks its first header
// is what the API looks for (ustar at 257).
func entries(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 512 || string(raw[257:262]) != "ustar" {
		t.Fatalf("the first header is not ustar: %q", raw[257:262])
	}
	tr := tar.NewReader(strings.NewReader(string(raw)))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Uname != "" {
			t.Errorf("%s keeps this machine's owner (%d %q)", h.Name, h.Uid, h.Uname)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestPackHonoursGitignoreFilesAndLeavesOutHistory(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".gitignore":          "node_modules/\n*.log\n!keep.log\n/build\n# a comment\n\ndocs/**/*.tmp\n",
		"package.json":        "{}",
		"index.js":            "console.log(1)",
		"debug.log":           "x",
		"keep.log":            "kept",
		"node_modules/a/i.js": "dep",
		"build/out.js":        "built",
		"src/build/keep.js":   "not the root build/",
		"docs/a/b/c.tmp":      "tmp",
		"docs/a/readme.md":    "doc",
		"web/.gitignore":      "dist\n",
		"web/dist/app.js":     "web build",
		"web/src/app.ts":      "src",
		"api/dist/server.js":  "the api's dist: not web's rule",
		".git/HEAD":           "ref: refs/heads/main",
		".mw/link.json":       "{}",
		"assets/logo.svg":     "<svg/>",
		"assets/.DS_Store":    "mac",
	})
	res, err := Pack(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.Path)
	got := strings.Join(entries(t, res.Path), " ")
	want := ".gitignore api/dist/server.js assets/logo.svg docs/a/readme.md index.js keep.log package.json src/build/keep.js web/.gitignore web/src/app.ts"
	if got != want {
		t.Fatalf("packed:\n  %s\nwant:\n  %s", got, want)
	}
	if res.Files != 10 || res.Rules != ".gitignore" || res.Size == 0 {
		t.Fatalf("result %+v", res)
	}
}

func TestMwignoreReplacesGitignore(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".gitignore":  "dist/\n",
		".mwignore":   "tests/\n*.md\n",
		"dist/app.js": "the build needs it",
		"tests/a.js":  "x",
		"README.md":   "x",
		"main.go":     "package main",
	})
	res, err := Pack(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(res.Path)
	if got := strings.Join(entries(t, res.Path), " "); got != ".gitignore .mwignore dist/app.js main.go" {
		t.Fatalf("packed %s", got)
	}
	if res.Rules != ".mwignore" {
		t.Fatalf("rules %q", res.Rules)
	}
}

func TestPackRefusesWhatCannotBeDeployed(t *testing.T) {
	big := t.TempDir()
	// Random bytes do not compress: well past 1 KB once packed.
	noise := make([]byte, 8192)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	write(t, big, map[string]string{"data.bin": string(noise)})
	if _, err := Pack(big, 1024); err == nil || !strings.Contains(err.Error(), "over 1 KB compressed") {
		t.Fatalf("too large: %v", err)
	}

	empty := t.TempDir()
	write(t, empty, map[string]string{".gitignore": "*\n"})
	if _, err := Pack(empty, 0); err == nil || !strings.Contains(err.Error(), "nothing to deploy") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Pack(filepath.Join(empty, "missing"), 0); err == nil {
		t.Fatal("a missing folder was packed")
	}
}

func TestIgnoreRules(t *testing.T) {
	cases := []struct {
		rules []string
		path  string
		dir   bool
		want  bool
	}{
		{[]string{"*.log"}, "a/b/c.log", false, true},
		{[]string{"/*.log"}, "a/c.log", false, false},
		{[]string{"/*.log"}, "c.log", false, true},
		{[]string{"logs/"}, "logs", false, false},
		{[]string{"logs/"}, "logs", true, true},
		{[]string{"a/**/z"}, "a/z", false, true},
		{[]string{"a/**/z"}, "a/b/c/z", false, true},
		{[]string{"**/tmp"}, "x/y/tmp", true, true},
		{[]string{"file?.txt"}, "file1.txt", false, true},
		{[]string{"file?.txt"}, "file10.txt", false, false},
		{[]string{"[ab].c"}, "b.c", false, true},
		{[]string{"[!ab].c"}, "b.c", false, false},
		{[]string{"*.env", "!.env.example"}, ".env.example", false, false},
		{[]string{"*.env", "!.env.example"}, "prod.env", false, true},
		{[]string{`\#notes`}, "#notes", false, true},
		{[]string{"a+b(1).txt"}, "a+b(1).txt", false, true},
	}
	for _, c := range cases {
		var rules []rule
		for _, l := range c.rules {
			if r, ok := parseRule(l, ""); ok {
				rules = append(rules, r)
			}
		}
		if got := ignored(rules, c.path, c.dir); got != c.want {
			t.Errorf("%v on %s (dir %v): %v, want %v", c.rules, c.path, c.dir, got, c.want)
		}
	}
}
