// Package buildinfo serializes the build identity of a running binary — the
// source revision it was built from, whether that source was modified, and the
// digest of the manifest naming its full source closure.
//
// It SERIALIZES; it does not discover. The values are handed to it by the
// deploy/publish authority through -ldflags -X at image build time, and that
// authority alone generates, verifies and takes custody of them
// (#fleet-build-provenance-contract). This package never shells out to git,
// never walks a source closure and never attests to anything. That is why it is
// named buildinfo and not provenance.
//
// The one exception is the checkout-build fallback: when no revision was
// injected, the Go toolchain's own vcs.revision / vcs.modified build settings
// are read via runtime/debug.ReadBuildInfo. A plain `go build` inside a git
// checkout records those settings, so a binary built by a path that passes no
// ldflags still reports the truth instead of an empty string.
//
// Precedence: injected values are AUTHORITATIVE whenever a revision is present.
package buildinfo

import (
	"runtime/debug"
	"strconv"
)

// Injected with -ldflags -X at build time. Strings, not typed values, because
// -X can only set a string variable.
var (
	// Revision is the full commit of the primary source repository the binary
	// was built from. Empty for a build whose path passes no ldflags, which is
	// what selects the ReadBuildInfo fallback.
	Revision = ""

	// Modified reports whether the built tree deviated from Revision:
	// "false" when the shipped tree is that commit by construction, "true" when
	// a dirty working tree was built. Parsed with strconv.ParseBool; a value
	// that does not parse is reported as modified, because an unreadable stamp
	// is not evidence of cleanliness.
	Modified = ""

	// SourceManifestDigest is the digest of the canonical manifest listing every
	// repository and commit in the binary's source closure (the primary repo
	// plus any sibling worktrees compiled in through local replaces). Empty when
	// the build authority attached no manifest; there is no fallback for it,
	// since a closure cannot be reconstructed after the build.
	SourceManifestDigest = ""
)

// Info is the build identity as health endpoints and boot logs report it.
type Info struct {
	PrimaryRevision      string `json:"primary_revision"`
	Modified             bool   `json:"modified"`
	SourceManifestDigest string `json:"source_manifest_digest"`
}

// Get returns the build identity of this binary: the injected values when a
// revision was injected, otherwise the toolchain's recorded vcs settings.
func Get() Info {
	return resolve(Revision, Modified, SourceManifestDigest, debug.ReadBuildInfo)
}

// resolve is Get's pure core, taking the reader so both paths are testable.
func resolve(revision, modified, digest string, read func() (*debug.BuildInfo, bool)) Info {
	if revision != "" {
		return Info{
			PrimaryRevision:      revision,
			Modified:             parseModified(modified),
			SourceManifestDigest: digest,
		}
	}

	info := Info{SourceManifestDigest: digest}
	bi, ok := read()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.PrimaryRevision = s.Value
		case "vcs.modified":
			info.Modified = parseModified(s.Value)
		}
	}
	return info
}

// parseModified reads a boolean stamp, treating absence as unmodified and an
// unparseable value as modified.
func parseModified(v string) bool {
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return true
	}
	return b
}
