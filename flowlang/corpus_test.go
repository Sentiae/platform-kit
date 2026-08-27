package flowlang

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// loadManifest is the one place a fixture manifest is read, so a corpus entry
// that stopped being publishable fails HERE rather than as a mysterious nil.
func loadManifest(t *testing.T, name string, b []byte) (*nodemanifest.Manifest, []nodemanifest.Problem) {
	t.Helper()
	man, problems := nodemanifest.Load(b)
	if man == nil {
		t.Fatalf("%s did not parse: %v", name, problems)
	}
	return man, problems
}

// corpusManifests loads the golden manifest set, keyed by the pin its file name
// encodes (`<scope>__<name>@<semver>.json` ⇒ `@<scope>/<name>@<semver>`).
func corpusManifests(t *testing.T) Manifests {
	t.Helper()
	entries, err := os.ReadDir("testdata/manifests")
	if err != nil {
		t.Fatalf("read manifests: %v", err)
	}
	out := Manifests{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("testdata/manifests", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		man, problems := loadManifest(t, e.Name(), b)
		if len(problems) > 0 {
			t.Fatalf("%s is not publishable: %v", e.Name(), problems)
		}
		out["@"+strings.Replace(strings.TrimSuffix(e.Name(), ".json"), "__", "/", 1)] = man
	}
	if len(out) == 0 {
		t.Fatal("no manifests in the corpus")
	}
	return out
}

// corpusFlows lists the golden `.flow` fixtures.
func corpusFlows(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("testdata/*.flow")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(names)
	if len(names) < 6 {
		t.Fatalf("corpus has %d .flow fixtures, want at least 6", len(names))
	}
	return names
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestCorpus_RoundTrip proves the two directions cannot drift: every golden
// file parses without a single objection, and the serializer writes the same
// bytes back. Byte-for-byte is the assertion, because "equivalent" is exactly
// the tolerance that lets a comment or a spacing rule quietly disappear.
func TestCorpus_RoundTrip(t *testing.T) {
	for _, path := range corpusFlows(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			text := readFixture(t, path)
			doc, diags := Parse(text)
			if doc == nil {
				t.Fatalf("Parse refused: %+v", diags)
			}
			if len(diags) != 0 {
				t.Fatalf("Parse reported %d findings: %+v", len(diags), diags)
			}
			got, err := Serialize(doc)
			if err != nil {
				t.Fatalf("Serialize: %v", err)
			}
			if got != text {
				t.Fatalf("round trip differs:\n--- got ---\n%s\n--- want ---\n%s", got, text)
			}
		})
	}
}

// TestCorpus_Canonical proves the fixtures ARE the canonical form: canonicalize
// then serialize is the identity on every one of them.
func TestCorpus_Canonical(t *testing.T) {
	manifests := corpusManifests(t)
	for _, path := range corpusFlows(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			text := readFixture(t, path)
			doc, diags := Parse(text)
			if doc == nil {
				t.Fatalf("Parse refused: %+v", diags)
			}
			got, err := Serialize(Canonicalize(doc, manifests))
			if err != nil {
				t.Fatalf("Serialize: %v", err)
			}
			if got != text {
				t.Fatalf("canonical form differs:\n--- got ---\n%s\n--- want ---\n%s", got, text)
			}
		})
	}
}

// TestCorpus_Diagnostics pins the FULL finding list of every fixture against
// its sidecar. A partial assertion would pass on a validator that stopped
// early, which is the failure this corpus exists to catch.
func TestCorpus_Diagnostics(t *testing.T) {
	manifests := corpusManifests(t)
	type row struct {
		Code     string   `json:"code"`
		Line     int      `json:"line"`
		Severity Severity `json:"severity"`
	}
	for _, path := range corpusFlows(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc, diags := Parse(readFixture(t, path))
			if doc == nil {
				t.Fatalf("Parse refused: %+v", diags)
			}
			var want []row
			sidecar := strings.TrimSuffix(path, ".flow") + ".diag.json"
			b, err := os.ReadFile(sidecar)
			if err != nil {
				t.Fatalf("read %s: %v", sidecar, err)
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatalf("decode %s: %v", sidecar, err)
			}
			got := []row{}
			for _, d := range Validate(doc, manifests) {
				got = append(got, row{Code: d.Code, Line: d.Line, Severity: d.Severity})
			}
			if want == nil {
				want = []row{}
			}
			if len(got) != len(want) {
				t.Fatalf("got %d findings %+v, want %d %+v", len(got), got, len(want), want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("finding %d: got %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestCorpus_Hash recomputes §2.2's corpus hash in Go. The bash guard, this
// test and the TypeScript suite must all agree, because a hash only one of
// three implementations can compute is a hash nobody checks.
func TestCorpus_Hash(t *testing.T) {
	want, err := os.ReadFile("testdata/CORPUS.sha256")
	if err != nil {
		t.Fatalf("read CORPUS.sha256: %v", err)
	}
	var files []string
	err = filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == "CORPUS.sha256" {
			return nil
		}
		rel, rerr := filepath.Rel("testdata", path)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(files) < 20 {
		t.Fatalf("corpus has %d hashed files, want at least 20", len(files))
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		h.Write([]byte(rel + "\n"))
		b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		h.Write(b)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != strings.TrimSpace(string(want)) {
		t.Fatalf("corpus hash %s, CORPUS.sha256 says %s", got, strings.TrimSpace(string(want)))
	}
}
