package cli

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Build-time variables injected via -ldflags by goreleaser. Defaults
// match the "no goreleaser, plain `go build`" path.
//
//	-X github.com/Jacob2017/wiki-audio/internal/cli.Version=v0.3.1
//	-X github.com/Jacob2017/wiki-audio/internal/cli.Commit=abc1234
//	-X github.com/Jacob2017/wiki-audio/internal/cli.BuildTime=2026-05-08T14:00:00Z
//
// Without ldflags, Version stays "dev" and the §6 schema-mismatch
// guard refuses to overwrite a manifest with tool_version != "dev".
// See wa-8gt.4.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// VersionLine returns the single-line --version output:
//
//	wiki-audio <Version> (commit <Commit>, built <BuildTime>)
//
// Falls back to runtime/debug.ReadBuildInfo VCS info when Commit or
// BuildTime were not injected by goreleaser. That lets a developer
// `go build` the repo and still get useful commit/time info, while
// `go install` from outside any repo gracefully shows "unknown".
func VersionLine() string {
	commit, buildTime := Commit, BuildTime
	if commit == "unknown" || buildTime == "unknown" {
		commit, buildTime = vcsFallback(commit, buildTime)
	}
	return fmt.Sprintf("wiki-audio %s (commit %s, built %s)",
		Version, commit, buildTime)
}

// vcsFallback fills in commit and buildTime from runtime/debug
// VCS info when the ldflags-injected values are still defaults. Go
// 1.18+ embeds vcs.{revision,time,modified} for binaries built
// inside a git repo; this is the path that turns plain `go build`
// into a useful diagnostic.
//
// commit is shortened to 7 chars matching the goreleaser convention.
// A "+modified" suffix is appended when the working tree was dirty
// at build time so a stray test rebuild doesn't masquerade as the
// pristine commit.
func vcsFallback(commit, buildTime string) (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return commit, buildTime
	}
	var rev, vcsTime string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if commit == "unknown" && rev != "" {
		commit = shortCommit(rev)
		if modified {
			commit += "+modified"
		}
	}
	if buildTime == "unknown" && vcsTime != "" {
		buildTime = vcsTime
	}
	return commit, buildTime
}

func shortCommit(rev string) string {
	const shortLen = 7
	if len(rev) <= shortLen {
		return rev
	}
	// Guard against non-hex bytes — we don't trust upstream blindly.
	for i := 0; i < shortLen; i++ {
		c := rev[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return strings.ToLower(rev[:shortLen])
		}
	}
	return strings.ToLower(rev[:shortLen])
}
