package lmstudio_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/lmstudio"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

func TestDiscoveryFollowsPointerAndSettings(t *testing.T) {
	home := fixture.Home(t, "lmstudio")
	d := lmstudio.New().Discover(fixture.Env(home, nil))
	if len(d.Stores) != 1 {
		t.Fatalf("one LM Studio store, got %+v", d.Stores)
	}
	l := d.Stores[0]
	if l.Name != "lmstudio" || !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, "LMStudioModels")) || !strings.Contains(l.Rule, "downloadsFolder") {
		t.Fatalf("location %+v", l)
	}
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, lmstudio.New(), "lmstudio", ".lmstudio/models")
}

func TestTreeIsTheIndex(t *testing.T) {
	home := fixture.Home(t, "lmstudio")
	o := fixture.Observe(t, lmstudio.New(), fixture.Env(home, nil), "lmstudio")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"lmstudio:acme/Tiny-GGUF/Tiny-Q4_K_M.gguf",
		"lmstudio:acme/Tiny-GGUF/mmproj-Tiny-F16.gguf",
		"lmstudio:other/Big-GGUF/Big-Q6_K.gguf",
	})
	q4 := fixture.MustManifest(t, o, "lmstudio", "acme/Tiny-GGUF/Tiny-Q4_K_M.gguf")
	fixture.Equal(t, "holders", fixture.Holders(q4), []string{"lmstudio"})
	fixture.Equal(t, "roles", fixture.Roles(q4), []string{"weights"})
	fixture.Equal(t, "projector", fixture.Roles(fixture.MustManifest(t, o, "lmstudio", "acme/Tiny-GGUF/mmproj-Tiny-F16.gguf")), []string{"projector"})
	if len(o.Objects) != 3 || o.OtherFiles != 1 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
	if obj, _ := fixture.Object(o, "other/Big-GGUF/Big-Q6_K.gguf"); obj.Size != 6000 {
		t.Fatalf("size %+v", obj)
	}
}

// The store publishes a model descriptor per weight file: the family and the
// quantisation its own naming states, and the architecture, size label,
// fine-tune and base the file's own GGUF header states. This is the identity
// the router reads; nothing here is hashed and no tensor is opened.
func TestEachWeightFilePublishesADescriptor(t *testing.T) {
	home := fixture.Home(t, "lmstudio")
	path := filepath.Join(home, "LMStudioModels", "acme", "Tiny-GGUF", "Tiny-Q4_K_M.gguf")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture.WriteGGUF(t, path, st.Size(), map[string]string{
		"general.architecture":          "llama",
		"general.basename":              "Tiny",
		"general.size_label":            "135M",
		"general.finetune":              "Instruct",
		"general.base_model.0.repo_url": "https://huggingface.co/acme/Tiny",
		"general.file_type":             "15",
	})

	o := fixture.Observe(t, lmstudio.New(), fixture.Env(home, nil), "lmstudio")
	m := fixture.MustManifest(t, o, "lmstudio", "acme/Tiny-GGUF/Tiny-Q4_K_M.gguf")
	if m.Manifest.Kind != "abstraction.model/descriptor@1" {
		t.Fatalf("kind %q", m.Manifest.Kind)
	}
	want := map[string]string{
		"family": "tiny", "quant": "q4_k_m", "format": "gguf", "architecture": "llama",
		"size_label": "135M", "finetune": "Instruct", "base": "acme/Tiny", "source": "lmstudio",
	}
	if !reflect.DeepEqual(m.Manifest.Descriptor, want) {
		t.Fatalf("descriptor %v, want %v", m.Manifest.Descriptor, want)
	}
	// A file whose header is unreadable keeps what its name says, and says so
	// by carrying no architecture.
	big := fixture.MustManifest(t, o, "lmstudio", "other/Big-GGUF/Big-Q6_K.gguf")
	if big.Manifest.Descriptor["family"] != "big" || big.Manifest.Descriptor["quant"] != "q6_k" {
		t.Fatalf("descriptor %v", big.Manifest.Descriptor)
	}
	if big.Manifest.Descriptor["architecture"] != "" {
		t.Fatalf("a placeholder file produced an architecture: %v", big.Manifest.Descriptor)
	}
}

func TestMalformedSettings(t *testing.T) {
	home := fixture.Home(t, "lmstudio-malformed")
	o := fixture.Observe(t, lmstudio.New(), fixture.Env(home, nil), "lmstudio")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index " + filepath.Join(home, ".lmstudio", "settings.json")})
	fixture.MustManifest(t, o, "lmstudio", "acme/X-GGUF/X.gguf")
}
