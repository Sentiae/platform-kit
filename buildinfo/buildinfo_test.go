package buildinfo

import (
	"encoding/json"
	"runtime/debug"
	"testing"
)

func readerFor(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: settings}, true
	}
}

// TestResolve covers both paths: injected values win outright, and an empty
// injected revision falls back to the toolchain's recorded vcs settings.
func TestResolve(t *testing.T) {
	const (
		injected = "1a14c2b39d0be6e7707b5e094786a5c5ff2a35c0"
		stamped  = "d205a919e0f1c2b3a4d5e6f708192a3b4c5d6e7f"
		digest   = "sha256:0f1e2d3c"
	)

	tests := []struct {
		name     string
		revision string
		modified string
		digest   string
		read     func() (*debug.BuildInfo, bool)
		want     Info
	}{
		{
			name:     "injected revision wins over the vcs settings",
			revision: injected, modified: "false", digest: digest,
			read: readerFor(
				debug.BuildSetting{Key: "vcs.revision", Value: stamped},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"},
			),
			want: Info{PrimaryRevision: injected, Modified: false, SourceManifestDigest: digest},
		},
		{
			name:     "injected dirty build reports modified",
			revision: injected, modified: "true",
			read: readerFor(),
			want: Info{PrimaryRevision: injected, Modified: true},
		},
		{
			name:     "injected revision with an unparseable modified stamp reports modified",
			revision: injected, modified: "yes-ish",
			read: readerFor(),
			want: Info{PrimaryRevision: injected, Modified: true},
		},
		{
			name: "empty injected revision falls back to the vcs settings",
			read: readerFor(
				debug.BuildSetting{Key: "vcs.revision", Value: stamped},
				debug.BuildSetting{Key: "vcs.modified", Value: "false"},
			),
			want: Info{PrimaryRevision: stamped, Modified: false},
		},
		{
			name: "fallback reports a dirty checkout build",
			read: readerFor(
				debug.BuildSetting{Key: "vcs.revision", Value: stamped},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"},
			),
			want: Info{PrimaryRevision: stamped, Modified: true},
		},
		{
			name:   "fallback keeps the injected closure digest",
			digest: digest,
			read: readerFor(
				debug.BuildSetting{Key: "vcs.revision", Value: stamped},
			),
			want: Info{PrimaryRevision: stamped, SourceManifestDigest: digest},
		},
		{
			name: "no injection and no build info yields the zero identity",
			read: func() (*debug.BuildInfo, bool) { return nil, false },
			want: Info{},
		},
		{
			name: "no injection and no vcs settings yields the zero identity",
			read: readerFor(debug.BuildSetting{Key: "-trimpath", Value: "true"}),
			want: Info{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(tt.revision, tt.modified, tt.digest, tt.read)
			if got != tt.want {
				t.Fatalf("resolve() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGetReadsPackageVars confirms Get serializes the injectable package
// variables (the seam -ldflags -X writes to).
func TestGetReadsPackageVars(t *testing.T) {
	origRev, origMod, origDigest := Revision, Modified, SourceManifestDigest
	t.Cleanup(func() { Revision, Modified, SourceManifestDigest = origRev, origMod, origDigest })

	Revision = "1a14c2b39d0be6e7707b5e094786a5c5ff2a35c0"
	Modified = "false"
	SourceManifestDigest = "sha256:0f1e2d3c"

	want := Info{PrimaryRevision: Revision, Modified: false, SourceManifestDigest: SourceManifestDigest}
	if got := Get(); got != want {
		t.Fatalf("Get() = %+v, want %+v", got, want)
	}
}

// TestInfoJSONShape pins the wire shape health endpoints and boot logs report.
func TestInfoJSONShape(t *testing.T) {
	b, err := json.Marshal(Info{
		PrimaryRevision:      "1a14c2b39d0be6e7707b5e094786a5c5ff2a35c0",
		Modified:             false,
		SourceManifestDigest: "sha256:0f1e2d3c",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"primary_revision":"1a14c2b39d0be6e7707b5e094786a5c5ff2a35c0","modified":false,"source_manifest_digest":"sha256:0f1e2d3c"}`
	if string(b) != want {
		t.Fatalf("Info JSON = %s, want %s", b, want)
	}
}
