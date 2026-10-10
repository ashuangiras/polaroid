// Package version reports the build of the running binary from the metadata
// the Go toolchain embeds: the VCS revision, whether the tree was modified,
// the commit time and the Go version.
package version

import (
	"runtime"
	"runtime/debug"
)

// Build identifies a build. Revision is empty when the binary was built
// without VCS information, for example by go test or outside a checkout.
type Build struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
	Time     string `json:"time,omitempty"`
	Go       string `json:"go"`
}

// Current returns the build of the running binary, named name.
func Current(name string) Build {
	b := Build{Name: name, Go: runtime.Version()}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		case "vcs.time":
			b.Time = s.Value
		}
	}
	return b
}

// Same reports whether a and b are the same source build, whatever their
// names.
func Same(a, b Build) bool {
	return a.Revision == b.Revision && a.Modified == b.Modified && a.Go == b.Go
}
