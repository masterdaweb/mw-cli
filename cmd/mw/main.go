// Command mw is the Master da Web command line.
package main

import (
	"os"
	// The zone database, so TZ works where the system has none: the
	// image's Alpine, where `--expires 2027-01-31` would otherwise end the
	// day in UTC whatever TZ says.
	_ "time/tzdata"

	"github.com/masterdaweb/mw-cli/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:]))
}
