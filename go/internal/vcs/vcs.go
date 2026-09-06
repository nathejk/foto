// Package vcs reports the build identity of this binary.
//
// Two sources, in order: the -ldflags value CI injects, then Go's own VCS
// stamping. The prod build context has no .git, so runtime/debug cannot work
// there — and a dev build has no ldflags — so neither mechanism alone covers
// both environments.
package vcs

import (
	"fmt"
	"runtime/debug"
)

// version is set at build time with
// -ldflags "-X foto.nathejk.dk/internal/vcs.version=<value>".
// Keep that path in sync with docker/Dockerfile.
var version string

// Version returns the build identity, or "unknown" when there is none.
//
// It is stamped onto every event this service publishes (see the metatagger in
// cmd/api/eventing.go), which is why it degrades to a string rather than
// returning an error: a missing version must not stop a photo being recorded.
func Version() string {
	if version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return "unknown"
	}
	if modified == "true" {
		return fmt.Sprintf("%s-dirty", revision)
	}
	return revision
}
