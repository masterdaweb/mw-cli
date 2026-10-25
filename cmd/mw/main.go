// Command mw is the Master da Web command line.
package main

import (
	"os"

	"github.com/masterdaweb/mw-cli/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:]))
}
