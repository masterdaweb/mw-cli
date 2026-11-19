package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// What a folder is, read from the files at its root before anything is sent:
// said before a first deployment, so a folder the build will not know what to
// do with is found out here and not ten minutes into a build. It decides
// nothing of the build — the build detects for itself — and reads one file's
// contents, for one thing: the port a Dockerfile says its app listens on.

type detectedApp struct {
	// Text is the sentence shown.
	Text string
	// Recognized: a Dockerfile, or an app the builder knows by its files.
	Recognized bool
	// Port is the one the Dockerfile exposes (its first, over TCP); 0, none
	// said. A service made from the folder listens there: an image that
	// exposes 9000 and a service that expects 8080 is a first deployment
	// that fails its health check five minutes in.
	Port int
}

// appMarkers are the files each kind of app is known by, in the order the
// builder looks; the first that is there names the app.
var appMarkers = []struct {
	what  string
	files []string
}{
	{"PHP app", []string{"composer.json", "index.php"}},
	{"Go app", []string{"go.mod", "go.work", "main.go"}},
	{"Java app", []string{"pom.xml", "gradlew", "build.gradle", "build.gradle.kts"}},
	{"Rust app", []string{"Cargo.toml"}},
	{"Ruby app", []string{"Gemfile"}},
	{"Elixir app", []string{"mix.exs"}},
	{"Deno app", []string{"deno.json", "deno.jsonc"}},
	{"Python app", []string{"requirements.txt", "pyproject.toml", "Pipfile", "main.py"}},
	{"Node.js app", []string{"package.json"}},
	{"static site", []string{"Staticfile", "index.html"}},
}

// dotnetSuffixes name a .NET solution or project, whatever the app is called.
var dotnetSuffixes = []string{".sln", ".slnx", ".csproj", ".fsproj", ".vbproj"}

func detectApp(dir string) detectedApp {
	has := func(name string) bool {
		st, err := os.Stat(filepath.Join(dir, name))
		return err == nil && !st.IsDir()
	}
	config := ""
	if has("mw.toml") {
		config = " Its mw.toml sets the rest."
	}
	if has("Dockerfile") {
		d := detectedApp{Text: "Found a Dockerfile: the image is built from it.", Recognized: true, Port: exposedPort(filepath.Join(dir, "Dockerfile"))}
		if d.Port > 0 {
			d.Text = fmt.Sprintf("Found a Dockerfile: the image is built from it, and listens on the port it exposes (%d).", d.Port)
		}
		d.Text += config
		return d
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			for _, suffix := range dotnetSuffixes {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), suffix) {
					return detectedApp{Text: "Found a .NET app (" + e.Name() + "): built as detected, no Dockerfile needed." + config, Recognized: true}
				}
			}
		}
	}
	for _, m := range appMarkers {
		for _, f := range m.files {
			if has(f) {
				return detectedApp{Text: "Found a " + m.what + " (" + f + "): built as detected, no Dockerfile needed." + config, Recognized: true}
			}
		}
	}
	return detectedApp{Text: "Found no Dockerfile, and none of the files an app is known by (package.json, requirements.txt, go.mod, composer.json, pom.xml, Gemfile, index.html…)."}
}

// exposedPort is the first TCP port a Dockerfile's EXPOSE lines name, 0
// without one (or when it is a variable: only the build knows its value).
func exposedPort(dockerfile string) int {
	f, err := os.Open(dockerfile)
	if err != nil {
		return 0
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "EXPOSE") {
			continue
		}
		for _, p := range fields[1:] {
			port, proto, _ := strings.Cut(p, "/")
			if proto != "" && !strings.EqualFold(proto, "tcp") {
				continue
			}
			if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 65536 {
				return n
			}
		}
	}
	return 0
}
