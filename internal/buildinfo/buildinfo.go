// Package buildinfo reports which version of jhh is running.
package buildinfo

import "runtime/debug"

// version is set at release time with
// -ldflags "-X github.com/drewsokol/job-hunt-harness/internal/buildinfo.version=v0.1.0".
var version string

// Version returns the release version, the module version for `go install`
// builds, or "dev" for local builds.
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
