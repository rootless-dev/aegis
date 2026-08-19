// Package banner prints the startup banner.
//
// It writes plain text rather than going through the logger: the art spans
// several lines and would be neither readable nor parseable as structured log
// entries.
package banner

import (
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/rootless-dev/aegis/internal/buildinfo"
)

const art = `    _     _____   ____  ___  ____
   / \   | ____| / ___||_ _|/ ___|
  / _ \  |  _|  | |  _  | | \___ \
 / ___ \ | |___ | |_| | | |  ___) |
/_/   \_\|_____| \____||___||____/`

const tagline = "identity & access management"

const dirtyMarker = " (dirty)"

// Print writes the banner followed by the identity of the running binary. A
// disabled banner writes nothing at all, which is what keeps a log collector
// from tripping over non structured lines in production.
func Print(w io.Writer, enabled bool) {
	if !enabled {
		return
	}

	info := buildinfo.Read()

	revision := info.ShortRevision()
	if info.Modified {
		// A build from a dirty tree matches no commit, and that is worth
		// seeing at startup rather than discovering during an incident.
		revision += dirtyMarker
	}

	details := strings.Join([]string{
		info.Version,
		revision,
		info.GoVersion,
		runtime.GOOS + "/" + runtime.GOARCH,
	}, " · ")

	fmt.Fprintf(w, "\n%s\n\n  %s\n  %s\n\n", art, tagline, details)
}
