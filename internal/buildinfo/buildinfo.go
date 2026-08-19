// Package buildinfo reports the identity of the running binary.
//
// It is deliberately not configurable. A version that the environment can
// override is a version that may disagree with the code actually running, and
// that is precisely when the information stops being useful.
//
// Only the version needs to be injected at link time; the revision and the
// build time are stamped into the binary by the Go toolchain, provided the
// build happens inside the repository.
package buildinfo

import (
	"runtime/debug"
	"sync"
)

// version is injected at link time:
//
//	-X github.com/rootless-dev/aegis/internal/buildinfo.version=v0.0.1
var version string

const (
	developmentVersion = "devel"
	unknownValue       = "unknown"
	shortRevisionSize  = 7
)

type Info struct {
	Version   string
	Revision  string
	Time      string
	GoVersion string

	// Modified reports a build made from a dirty working tree, which means the
	// binary does not correspond to any commit.
	Modified bool
}

// ShortRevision is the revision trimmed for log lines.
func (i Info) ShortRevision() string {
	if len(i.Revision) < shortRevisionSize {
		return i.Revision
	}

	return i.Revision[:shortRevisionSize]
}

var read = sync.OnceValue(func() Info {
	info := Info{
		Version:  version,
		Revision: unknownValue,
		Time:     unknownValue,
	}

	if info.Version == "" {
		info.Version = developmentVersion
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	info.GoVersion = build.GoVersion

	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Revision = setting.Value
		case "vcs.time":
			info.Time = setting.Value
		case "vcs.modified":
			info.Modified = setting.Value == "true"
		}
	}

	return info
})

func Read() Info {
	return read()
}
