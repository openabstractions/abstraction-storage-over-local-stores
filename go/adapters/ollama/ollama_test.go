package ollama_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/ollama"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

func TestDiscovery(t *testing.T) {
	home := fixture.Home(t, "ollama")
	d := ollama.New().Discover(fixture.Env(home, nil))
	loc := fixture.Location(t, d, "ollama")
	if !loc.Present || !fixture.SamePath(loc.Locator, filepath.Join(home, ".ollama", "models")) || !strings.Contains(loc.Rule, "default") {
		t.Fatalf("default location %+v", loc)
	}

	elsewhere := filepath.Join(home, "elsewhere")
	d = ollama.New().Discover(fixture.Env(home, map[string]string{"OLLAMA_MODELS": elsewhere}))
	if len(d.Stores) != 1 {
		t.Fatalf("OLLAMA_MODELS replaces the default; got %d locations", len(d.Stores))
	}
	if l := d.Stores[0]; !fixture.SamePath(l.Locator, elsewhere) || l.Present || !strings.Contains(l.Rule, "OLLAMA_MODELS") {
		t.Fatalf("env location %+v", l)
	}
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, ollama.New(), "ollama", ".ollama/models", "manifests", "blobs")
}

func TestManifestsBlobsAndPartials(t *testing.T) {
	home := fixture.Home(t, "ollama")
	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")

	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"ollama:hf.co/acme/remote-gguf:q4", "ollama:library/tiny:1b", "ollama:library/tiny:latest",
	})

	model := fixture.Label("ollama-model", 64)
	tiny := fixture.MustManifest(t, o, "ollama", "library/tiny:1b")
	fixture.Equal(t, "roles", fixture.Roles(tiny), []string{"weights.config", "weights", "template", "license", "params"})
	fixture.Equal(t, "holders", fixture.Holders(tiny), []string{"ollama"})
	w := tiny.Manifest.Entries[1]
	if w.Digest != "sha256:"+model || w.Evidence != content.EvidenceNamed || w.Size != 4096 || w.MediaType != "application/vnd.ollama.image.model" {
		t.Fatalf("weights entry %+v", w)
	}
	if tiny.Manifest.Kind != source.KindModel || tiny.Manifest.Store != "ollama" || !strings.HasPrefix(tiny.Manifest.ID, "sha256:") {
		t.Fatalf("manifest %+v", tiny.Manifest)
	}
	h := tiny.Holds[0]
	if h.Target != tiny.Manifest.ID || h.Attestation != content.AttestationObserved || h.Lifetime != content.LifetimeWhilePresent || h.Purpose != source.PurposeInstalled || h.ObservedAt == "" {
		t.Fatalf("hold %+v", h)
	}

	latest := fixture.MustManifest(t, o, "ollama", "library/tiny:latest")
	if latest.Manifest.ID == tiny.Manifest.ID {
		t.Fatalf("manifests with different params share id %s", tiny.Manifest.ID)
	}
	if latest.Manifest.Entries[1].Digest != w.Digest {
		t.Fatalf("the two tags share one model blob")
	}

	// 7 named blobs, 2 partial blobs.
	if len(o.Objects) != 9 {
		t.Fatalf("objects %v", fixture.Locators(o))
	}
	blob, ok := fixture.Object(o, "blobs/sha256-"+model)
	if !ok || blob.Evidence != content.EvidenceNamed || blob.Digest != "sha256:"+model || blob.Size != 4096 || blob.Partial || blob.Modified == "" {
		t.Fatalf("model blob %+v", blob)
	}
	partial := fixture.Label("ollama-partial", 64)
	for _, name := range []string{"blobs/sha256-" + partial + "-partial", "blobs/sha256-" + partial + "-partial-0"} {
		p, ok := fixture.Object(o, name)
		if !ok || !p.Partial || p.Evidence != content.EvidenceNone || p.Digest != "" {
			t.Fatalf("partial %s: %+v", name, p)
		}
	}
	orphan := fixture.Label("ollama-orphan", 64)
	if _, ok := fixture.Object(o, "blobs/sha256-"+orphan); !ok {
		t.Fatalf("orphan blob not listed")
	}
	for _, m := range o.Manifests {
		for _, e := range m.Manifest.Entries {
			if strings.Contains(e.Digest, orphan) {
				t.Fatalf("orphan blob is named by %v", m.Manifest.Names)
			}
		}
	}

	missing := fixture.Label("ollama-missing", 64)
	if len(o.Dangling) != 1 || o.Dangling[0].Reference != "sha256:"+missing || o.Dangling[0].Holder.Program != "ollama" ||
		o.Dangling[0].ExpectedDigest != "sha256:"+missing || o.Dangling[0].Basis.Key != "hf.co/acme/remote-gguf:q4" || o.Dangling[0].ExpectedSize != 9999 || o.Dangling[0].Basis.Index == "" {
		t.Fatalf("dangling %+v", o.Dangling)
	}
}

// A tag's manifest publishes a model descriptor: the family and the
// quantisation Ollama's own name states, and the architecture, size label,
// fine-tune and base the weights blob's GGUF header states.
func TestATagPublishesADescriptorFromItsNameAndItsBlob(t *testing.T) {
	home := fixture.Home(t, "ollama")
	blob := filepath.Join(home, ".ollama", "models", "blobs", "sha256-"+fixture.Label("ollama-model", 64))
	st, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	fixture.WriteGGUF(t, blob, st.Size(), map[string]string{
		"general.architecture":      "llama",
		"general.basename":          "Tiny",
		"general.size_label":        "1B",
		"general.base_model.0.name": "acme/Tiny",
		"general.file_type":         "10",
	})

	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	m := fixture.MustManifest(t, o, "ollama", "library/tiny:1b")
	want := map[string]string{
		"family": "tiny-1b", "quant": "q2_k", "format": "gguf", "architecture": "llama",
		"size_label": "1B", "base": "acme/Tiny", "source": "ollama",
	}
	if !reflect.DeepEqual(m.Manifest.Descriptor, want) {
		t.Fatalf("descriptor %v, want %v", m.Manifest.Descriptor, want)
	}
	// A tag whose blob Ollama does not hold has no header to read, and its
	// descriptor is what the tag name says.
	remote := fixture.MustManifest(t, o, "ollama", "hf.co/acme/remote-gguf:q4")
	if remote.Manifest.Descriptor["architecture"] != "" || remote.Manifest.Descriptor["family"] != "acme-remote" {
		t.Fatalf("descriptor of an unheld tag %v", remote.Manifest.Descriptor)
	}
}

func TestMalformedManifest(t *testing.T) {
	home := fixture.Home(t, "ollama-malformed")
	o := fixture.Observe(t, ollama.New(), fixture.Env(home, nil), "ollama")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{
		"malformed_index manifests/registry.ollama.ai/library/bad/1b",
		"malformed_index manifests/registry.ollama.ai/library/bad/nolayers",
	})
	good := fixture.MustManifest(t, o, "ollama", "library/good:1b")
	fixture.Equal(t, "holders", fixture.Holders(good), []string{"ollama"})
	if len(o.Objects) != 1 {
		t.Fatalf("objects %v", fixture.Locators(o))
	}
}
