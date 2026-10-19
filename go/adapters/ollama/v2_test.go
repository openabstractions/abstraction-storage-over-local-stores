package ollama_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/ollama"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Media types and field values as Ollama v0.40.6 spells them
// (manifest/manifest.go: MediaTypeManifest, MediaTypeManifestList, RunnerMLX,
// RunnerGGML, RunnerLlamaCPP, FormatGGUF, FormatSafetensors).
const (
	mediaManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaList     = "application/vnd.ollama.manifest.list.v2+json"
	mediaConfig   = "application/vnd.docker.container.image.v1+json"
)

// v2Store builds an Ollama v0.40 store in a temporary home: blobs named
// sha256-<hex of their bytes>, and manifests-v2/<host>/<namespace>/<model>/
// <tag> written as regular-file copies of a blob, as Ollama writes them on
// Windows since v0.40.6. Every JSON shape follows Ollama v0.40.6's
// Manifest.MarshalJSON: a plain manifest carries schemaVersion, mediaType,
// config, layers and, when set, runner and format; a manifest list carries
// schemaVersion, mediaType and manifests; a reference child carries
// mediaType, digest, runner and format, and no size; an inline child is a
// plain manifest with no digest (createManifestForTest's shape).
type v2Store struct {
	t    *testing.T
	root string
}

func newV2Store(t *testing.T) (string, v2Store) {
	home := t.TempDir()
	root := filepath.Join(home, ".ollama", "models")
	return home, v2Store{t: t, root: root}
}

func (s v2Store) blob(body []byte) (string, int64) {
	s.t.Helper()
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	s.write(filepath.Join(s.root, "blobs", "sha256-"+h), body)
	return "sha256:" + h, int64(len(body))
}

func (s v2Store) write(p string, body []byte) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		s.t.Fatal(err)
	}
}

type layer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func (s v2Store) layer(mediaType, body string) layer {
	d, n := s.blob([]byte(body))
	return layer{MediaType: mediaType, Digest: d, Size: n}
}

// plain is a manifest in the full branch of Ollama's MarshalJSON.
type plain struct {
	SchemaVersion int     `json:"schemaVersion"`
	MediaType     string  `json:"mediaType"`
	Config        layer   `json:"config"`
	Layers        []layer `json:"layers"`
	Runner        string  `json:"runner,omitempty"`
	Format        string  `json:"format,omitempty"`
}

// reference is a list child in the reference branch of Ollama's
// MarshalJSON (NewManifestReference): no size, runner and format top-level.
type reference struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Runner    string `json:"runner,omitempty"`
	Format    string `json:"format,omitempty"`
}

func newPlain(config layer, layers ...layer) plain {
	return plain{SchemaVersion: 2, MediaType: mediaManifest, Config: config, Layers: layers}
}

func (s v2Store) marshal(v any) []byte {
	s.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	return b
}

func (s v2Store) manifest(config layer, layers ...layer) []byte {
	return s.marshal(newPlain(config, layers...))
}

func (s v2Store) list(children ...any) []byte {
	return s.marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaList, "manifests": children})
}

// named writes a named manifest. Under manifests-v2 the named manifest is
// also a blob, as Ollama v0.40 stores it; the legacy manifests/ tree is not.
func (s v2Store) named(tree, rel string, body []byte) {
	if tree == "manifests-v2" {
		s.blob(body)
	}
	s.write(filepath.Join(s.root, tree, filepath.FromSlash(rel)), body)
}

// held is every locator a manifest of o names.
func held(o source.StoreObservation) map[string]bool {
	h := map[string]bool{}
	for _, m := range o.Manifests {
		for _, l := range source.EntryLocators(m.Manifest) {
			h[l] = true
		}
	}
	return h
}

// unheld is every blob no manifest names, sorted.
func unheld(o source.StoreObservation) []string {
	h := held(o)
	var out []string
	for _, obj := range o.Objects {
		if strings.HasPrefix(obj.Locator, "blobs/") && !h[obj.Locator] {
			out = append(out, obj.Locator)
		}
	}
	sort.Strings(out)
	return out
}

func blobLocator(digest string) string {
	return "blobs/sha256-" + strings.TrimPrefix(digest, "sha256:")
}

func TestManifestsV2(t *testing.T) {
	home, s := newV2Store(t)
	cfg := s.layer(mediaConfig, `{"model_format":"gguf"}`)
	gguf := s.layer("application/vnd.ollama.image.model", strings.Repeat("g", 4096))
	tmpl := s.layer("application/vnd.ollama.image.template", "{{ .Prompt }}")

	// A plain v2 manifest under the new default host.
	small := newPlain(cfg, gguf, tmpl)
	small.Format = "gguf"
	s.named("manifests-v2", "ollama.com/library/small/1b", s.marshal(small))

	// A manifest list with runner-specific reference children.
	tensors := s.manifest(s.layer(mediaConfig, `{"model_format":"safetensors"}`),
		s.layer("application/vnd.ollama.image.tensor", strings.Repeat("a", 2048)),
		s.layer("application/vnd.ollama.image.tensor", strings.Repeat("b", 2048)),
		s.layer("application/vnd.ollama.image.draft", strings.Repeat("d", 512)),
	)
	mlxDigest, _ := s.blob(tensors)
	ggml := s.manifest(cfg, gguf)
	ggmlDigest, ggmlSize := s.blob(ggml)
	missingChild := "sha256:" + fixture.Label("ollama-child-missing", 64)
	list := s.list(
		reference{MediaType: mediaManifest, Digest: mlxDigest, Runner: "mlx", Format: "safetensors"},
		reference{MediaType: mediaManifest, Digest: ggmlDigest, Runner: "llamacpp", Format: "gguf"},
		reference{MediaType: mediaManifest, Digest: missingChild, Runner: "ggml", Format: "gguf"},
	)
	listDigest, _ := s.blob(list)
	s.named("manifests-v2", "ollama.com/library/multi/latest", list)

	// One model under both trees: the lazy migration leaves the legacy name
	// in manifests/ beside the v2 name; Ollama reads manifests-v2 first.
	s.named("manifests-v2", "ollama.com/library/both/1b", s.manifest(cfg, gguf))
	s.named("manifests", "registry.ollama.ai/library/both/1b", s.manifest(cfg, gguf, tmpl))

	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"ollama:library/both:1b", "ollama:library/multi:latest", "ollama:library/multi:latest", "ollama:library/small:1b",
	})

	sm := fixture.MustManifest(t, o, "ollama", "library/small:1b")
	fixture.Equal(t, "small roles", fixture.Roles(sm), []string{"ollama/manifest", "weights.config", "weights", "template"})
	fixture.Equal(t, "small holders", fixture.Holders(sm), []string{"ollama"})
	if sm.Manifest.Descriptor["format"] != "gguf" {
		t.Fatalf("small descriptor %v", sm.Manifest.Descriptor)
	}

	both := fixture.MustManifest(t, o, "ollama", "library/both:1b")
	fixture.Equal(t, "both roles: the v2 manifest wins", fixture.Roles(both), []string{"ollama/manifest", "weights.config", "weights"})
	if len(both.Holds) != 1 || !strings.HasPrefix(both.Holds[0].Basis.Index, "manifests-v2/") {
		t.Fatalf("both holds %+v", both.Holds)
	}

	var runners []string
	for _, m := range o.Manifests {
		if m.Manifest.Names[0].Name != "library/multi:latest" {
			continue
		}
		e := m.Manifest.Entries
		runner := e[0].Annotations["runner"]
		runners = append(runners, runner)
		fixture.Equal(t, "multi holders", fixture.Holders(m), []string{"ollama"})
		if e[0].Digest != listDigest || e[0].MediaType != mediaList {
			t.Fatalf("%s: first entry is not the list's own blob: %+v", runner, e[0])
		}
		switch runner {
		case "mlx":
			fixture.Equal(t, "mlx roles", fixture.Roles(m), []string{"ollama/manifest", "ollama/manifest", "weights.config", "weights", "weights", "ollama/draft"})
			if m.Manifest.Descriptor["format"] != "safetensors" || e[1].Annotations["format"] != "safetensors" {
				t.Fatalf("mlx descriptor %v annotations %v", m.Manifest.Descriptor, e[1].Annotations)
			}
			w := e[3]
			if w.Evidence != content.EvidenceNamed || w.Size != 2048 || w.MediaType != "application/vnd.ollama.image.tensor" {
				t.Fatalf("tensor entry %+v", w)
			}
		case "llamacpp":
			fixture.Equal(t, "llamacpp roles", fixture.Roles(m), []string{"ollama/manifest", "ollama/manifest", "weights.config", "weights"})
			if m.Manifest.Descriptor["format"] != "gguf" {
				t.Fatalf("llamacpp descriptor %v", m.Manifest.Descriptor)
			}
			// The reference child states no size; the head entry carries the
			// size of the blob it names.
			if e[1].Digest != ggmlDigest || e[1].Size != ggmlSize {
				t.Fatalf("child head entry %+v, want %s size %d", e[1], ggmlDigest, ggmlSize)
			}
		}
	}
	sort.Strings(runners)
	fixture.Equal(t, "runners", runners, []string{"llamacpp", "mlx"})
	if len(o.Dangling) != 1 || o.Dangling[0].ExpectedDigest != missingChild || o.Dangling[0].Basis.Key != "library/multi:latest" {
		t.Fatalf("dangling %+v", o.Dangling)
	}

	// Ollama counts every named manifest's and every manifest list's own
	// blob as referenced (referencedBlobDigestsForData), so every blob in
	// this store is held.
	fixture.Equal(t, "unheld blobs", unheld(o), []string(nil))
}

// An inline child is a full manifest inside the list with config and layers
// and no digest; Ollama v0.40.6's parser accepts it beside reference children.
func TestManifestListInlineChild(t *testing.T) {
	home, s := newV2Store(t)
	cfg := s.layer(mediaConfig, `{"model_format":"gguf"}`)
	gguf := s.layer("application/vnd.ollama.image.model", strings.Repeat("g", 1024))
	ref, _ := s.blob(s.manifest(cfg, gguf))

	mlxCfg := s.layer(mediaConfig, `{"model_format":"safetensors"}`)
	tensor := s.layer("application/vnd.ollama.image.tensor", strings.Repeat("t", 2048))
	inline := newPlain(mlxCfg, tensor)
	inline.Runner, inline.Format = "mlx", "safetensors"

	s.named("manifests-v2", "ollama.com/library/real/latest", s.list(
		reference{MediaType: mediaManifest, Digest: ref, Runner: "llamacpp", Format: "gguf"},
		inline,
	))

	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	fixture.Equal(t, "names", fixture.Names(o), []string{"ollama:library/real:latest", "ollama:library/real:latest"})
	var found bool
	for _, m := range o.Manifests {
		if m.Manifest.Entries[0].Annotations["runner"] != "mlx" {
			continue
		}
		found = true
		// The list's own blob, then the inline child's config and layer: an
		// inline child is no blob of its own.
		fixture.Equal(t, "inline roles", fixture.Roles(m), []string{"ollama/manifest", "weights.config", "weights"})
		fixture.Equal(t, "inline holders", fixture.Holders(m), []string{"ollama"})
		if m.Manifest.Descriptor["format"] != "safetensors" {
			t.Fatalf("inline descriptor %v", m.Manifest.Descriptor)
		}
	}
	if !found {
		t.Fatal("no manifest for the inline mlx child")
	}
	h := held(o)
	for _, l := range []string{blobLocator(tensor.Digest), blobLocator(mlxCfg.Digest)} {
		if !h[l] {
			t.Fatalf("%s not held: %v", l, h)
		}
	}
	fixture.Equal(t, "unheld blobs", unheld(o), []string(nil))
}

// A child that states a digest beside its config and layers is no reference
// for Ollama v0.40.6's isReference; parseManifest reads it in place. Ollama's
// writer never produces this shape; a hand-edited manifest can.
func TestManifestListChildWithDigestAndLayers(t *testing.T) {
	home, s := newV2Store(t)
	cfg := s.layer(mediaConfig, `{"model_format":"gguf"}`)
	gguf := s.layer("application/vnd.ollama.image.model", strings.Repeat("h", 1024))
	stated := "sha256:" + fixture.Label("ollama-stated-child", 64)
	s.named("manifests-v2", "ollama.com/library/edited/latest", s.list(map[string]any{
		"schemaVersion": 2, "mediaType": mediaManifest, "digest": stated,
		"config": cfg, "layers": []layer{gguf}, "runner": "llamacpp", "format": "gguf",
	}))

	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	if len(o.Dangling) != 0 {
		t.Fatalf("a child with layers read as a reference: %+v", o.Dangling)
	}
	m := fixture.MustManifest(t, o, "ollama", "library/edited:latest")
	fixture.Equal(t, "roles", fixture.Roles(m), []string{"ollama/manifest", "weights.config", "weights"})
	fixture.Equal(t, "unheld blobs", unheld(o), []string(nil))
}

// A registry named host:port is written into manifests-v2 with its colon as
// %3A (paths.go encodeV2ManifestHost) and into the legacy tree with the
// colon itself; both name one model.
func TestManifestsV2HostPort(t *testing.T) {
	home, s := newV2Store(t)
	cfg := s.layer(mediaConfig, `{}`)
	gguf := s.layer("application/vnd.ollama.image.model", strings.Repeat("p", 512))
	s.named("manifests-v2", "localhost%3A5000/library/local/1b", s.manifest(cfg, gguf))
	// Windows cannot hold a colon in a file name; Ollama skips the legacy
	// candidate there.
	if runtime.GOOS != "windows" {
		s.named("manifests", "localhost:5000/library/local/1b", s.manifest(cfg, gguf))
	}
	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	fixture.Equal(t, "names", fixture.Names(o), []string{"ollama:localhost:5000/library/local:1b"})
	m := fixture.MustManifest(t, o, "ollama", "localhost:5000/library/local:1b")
	if len(m.Holds) != 1 || !strings.HasPrefix(m.Holds[0].Basis.Index, "manifests-v2/") {
		t.Fatalf("holds %+v", m.Holds)
	}
}

// A named manifest that is a symbolic link into blobs, as Ollama v0.40 writes
// it on Linux and macOS, is read through its link and holds the blob it
// links to.
func TestManifestsV2Link(t *testing.T) {
	home, s := newV2Store(t)
	cfg := s.layer(mediaConfig, `{}`)
	gguf := s.layer("application/vnd.ollama.image.model", strings.Repeat("l", 1024))
	body := s.manifest(cfg, gguf)
	d, _ := s.blob(body)
	link := filepath.Join(s.root, "manifests-v2", "ollama.com", "library", "linked", "1b")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.root, "blobs", "sha256-"+strings.TrimPrefix(d, "sha256:"))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	m := fixture.MustManifest(t, o, "ollama", "library/linked:1b")
	fixture.Equal(t, "roles", fixture.Roles(m), []string{"ollama/manifest", "weights.config", "weights"})
	if m.Manifest.Entries[0].Digest != d {
		t.Fatalf("head entry %+v, want %s", m.Manifest.Entries[0], d)
	}
	fixture.Equal(t, "unheld blobs", unheld(o), []string(nil))
}
