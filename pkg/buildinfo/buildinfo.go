// Package buildinfo tells which build of the application is running, for the logs and the metrics.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Version is set at build time: -ldflags "-X github.com/cksidharthan/lavender/pkg/buildinfo.Version=1.2.3".
var Version = "dev"

// Revision is the git commit the binary was built from, "unknown" when the build has no version control information
// (the docker build has none, as .git is not part of its context).
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value[:min(len(setting.Value), 12)]
		}
	}

	return "unknown"
}

// GoVersion is the version of go the binary was built with.
func GoVersion() string {
	return runtime.Version()
}
