package codemode

import (
	"runtime/debug"
	"strings"
)

const (
	gomontyModulePath = "github.com/mdfranz/gomonty"

	// MontyVersion is the upstream Monty release embedded in the pinned
	// gomonty build. Update it whenever the gomonty dependency changes its
	// Cargo.toml Monty revision.
	MontyVersion = "v1.0.1"
)

// GomontyVersion returns the linked gomonty module version. Tagged versions
// are shown as-is; development pseudo-versions are shortened to their commit
// hash so they fit comfortably in the TUI header.
func GomontyVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path != gomontyModulePath {
			continue
		}
		if dep.Replace != nil {
			dep = dep.Replace
		}
		return compactModuleVersion(dep.Version)
	}
	return "unknown"
}

func compactModuleVersion(version string) string {
	if version == "" || version == "(devel)" {
		return "devel"
	}
	lastDash := strings.LastIndexByte(version, '-')
	if lastDash >= 0 {
		hash := version[lastDash+1:]
		if len(hash) >= 12 {
			return hash[:7]
		}
	}
	return version
}

// VersionSummary is the compact runtime label displayed in sparktea's
// header.
func VersionSummary() string {
	return "gomonty " + GomontyVersion() + " · monty " + MontyVersion
}
