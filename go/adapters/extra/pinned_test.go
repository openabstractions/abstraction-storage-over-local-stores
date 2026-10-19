package extra_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// A reserved Setup store reports each catalogue file at its pinned path and
// size by the pinned digest as hashed evidence, a weights file and a
// tokenizer file alike, as an object; a catalogue model all of whose files
// are present is one model manifest named by it, and no file is a model of
// its own. A file of another size is read as any tree file is: no digest,
// and its model is incomplete.
func TestSetupPictureStoreReportsPinnedDigestsWithoutHashing(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "OA", "m", "pt")
	write := func(rel, body string) source.PinnedFile {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		return source.PinnedFile{Path: rel, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	}
	weights := write("flux2-klein-4b/split_files/diffusion_models/z_image_turbo_bf16.safetensors", "transformer")
	tokenizer := write("flux2-klein-4b/tokenizer/vocab.json", "{}")
	resized := write("flux2-klein-4b/split_files/vae/ae.safetensors", "vae")
	// The catalogue names a digest this test never computes: the reader
	// takes Setup's verified digest and hashes nothing.
	weights.SHA256 = "2407613050b809ffdff18a4ac99af83ea6b95443ecebdf80e064a79c825574a6"
	resized.Size++
	key, err := identity.FileKey(root)
	if err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, nil)
	env.Digests = nil
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: source.SetupPictureStore, Path: root, ExpectedFileKey: key,
		Files: []source.PinnedFile{weights, tokenizer, resized},
		Models: []source.PinnedModel{{Name: "z-image-turbo", Files: []string{weights.Path, tokenizer.Path}},
			{Name: "needs-the-vae", Files: []string{weights.Path, resized.Path}}}}}
	adapter := extra.New()
	discovery := adapter.Discover(env)
	if len(discovery.Stores) != 1 || discovery.Stores[0].Name != source.SetupPictureStore || len(discovery.Stores[0].Errors) != 0 {
		t.Fatalf("picture store: %+v", discovery)
	}
	observed := adapter.Observe(discovery.Stores[0], env)
	objects := map[string]content.Object{}
	for _, o := range observed.Objects {
		objects[o.Locator] = o
	}
	for _, pin := range []source.PinnedFile{weights, tokenizer} {
		o, ok := objects[pin.Path]
		if !ok || o.Digest != "sha256:"+pin.SHA256 || o.Evidence != content.EvidenceHashed {
			t.Fatalf("%s: %+v", pin.Path, o)
		}
	}
	var models []string
	for _, m := range observed.Manifests {
		if len(m.Manifest.Names) == 1 && m.Manifest.Names[0].Name == resized.Path {
			continue // the resized file is read as any tree weights file
		}
		if len(m.Manifest.Names) == 0 || m.Manifest.Names[0].Name != "z-image-turbo" {
			t.Fatalf("a manifest other than the complete model: %+v", m.Manifest)
		}
		models = append(models, m.Manifest.Names[0].Name)
		if m.Manifest.Descriptor["family"] != "z-image-turbo" || m.Manifest.Addressing != content.AddressingContent || len(m.Manifest.Entries) != 2 {
			t.Fatalf("complete model manifest %+v", m.Manifest)
		}
		for i, pin := range []source.PinnedFile{weights, tokenizer} {
			if e := m.Manifest.Entries[i]; e.Locator != pin.Path || e.Digest != "sha256:"+pin.SHA256 || e.Evidence != content.EvidenceHashed {
				t.Fatalf("entry %d %+v", i, e)
			}
		}
	}
	if len(models) != 1 {
		t.Fatalf("complete models %v", models)
	}
	if o := objects[resized.Path]; o.Evidence == content.EvidenceHashed || o.Digest != "" {
		t.Fatalf("a resized pinned file kept its catalogue digest: %+v", o)
	}

	// The same catalogue on an operator's own extra root pins nothing.
	env.Config.ExtraRoots[0].Store, env.Config.ExtraRoots[0].ExpectedFileKey = "my-pictures", ""
	discovery = adapter.Discover(env)
	if len(discovery.Stores) != 1 {
		t.Fatalf("operator store: %+v", discovery)
	}
	for _, o := range adapter.Observe(discovery.Stores[0], env).Objects {
		if o.Locator == weights.Path && (o.Evidence == content.EvidenceHashed || o.Digest == "sha256:"+weights.SHA256) {
			t.Fatalf("an operator store took the catalogue digest: %+v", o)
		}
	}
}
