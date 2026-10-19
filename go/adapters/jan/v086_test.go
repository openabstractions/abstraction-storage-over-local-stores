package jan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/jan"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// TestDataFolderV086 builds a Jan v0.8.6 data folder in a temporary home, in
// the shape Jan's data-folder page documents: llamacpp/models/<org>/<repo>/
// model.yml two levels deep with model_sha256, and mlx/models/<model_id>/
// model.yml naming a folder of safetensors.
func TestDataFolderV086(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "AppData", "Roaming", "Jan", "data")
	sha := fixture.Label("jan-tiny", 64)
	write(t, filepath.Join(data, "llamacpp", "models", "acme", "Tiny-GGUF", "Tiny-Q4_K_M.gguf"), strings.Repeat("g", 3000))
	write(t, filepath.Join(data, "llamacpp", "models", "acme", "Tiny-GGUF", "model.yml"),
		"model_path: llamacpp/models/acme/Tiny-GGUF/Tiny-Q4_K_M.gguf\nname: Tiny\nmodel_sha256: "+sha+"\nsize_bytes: 3000\n")
	write(t, filepath.Join(data, "llamacpp", "models", "acme", "Gone-GGUF", "model.yml"),
		"model_path: llamacpp/models/acme/Gone-GGUF/Gone.gguf\nname: Gone\nmodel_sha256: "+fixture.Label("jan-gone", 64)+"\n")
	write(t, filepath.Join(data, "mlx", "models", "Small-4bit", "weights", "model.safetensors"), strings.Repeat("s", 2000))
	write(t, filepath.Join(data, "mlx", "models", "Small-4bit", "weights", "config.json"), "{}")
	write(t, filepath.Join(data, "mlx", "models", "Small-4bit", "model.yml"),
		"model_path: mlx/models/Small-4bit/weights\nname: Small\n")

	env := fixture.Env(home, nil)
	o := fixture.Observe(t, jan.New(), env, "jan")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	fixture.Equal(t, "names", fixture.Names(o), []string{"jan:acme/Tiny-GGUF"})
	tiny := fixture.MustManifest(t, o, "jan", "acme/Tiny-GGUF")
	fixture.Equal(t, "holders", fixture.Holders(tiny), []string{"jan"})
	if e := tiny.Manifest.Entries[0]; e.Digest != "sha256:"+sha || e.Evidence != content.EvidenceNamed || e.Size != 3000 {
		t.Fatalf("model_sha256 entry %+v", e)
	}
	if tiny.Manifest.Addressing != content.AddressingContent {
		t.Fatalf("addressing %q, want content from the index-stated digest", tiny.Manifest.Addressing)
	}
	if len(o.Dangling) != 1 || o.Dangling[0].Basis.Key != "acme/Gone-GGUF" || o.Dangling[0].ExpectedDigest != "sha256:"+fixture.Label("jan-gone", 64) {
		t.Fatalf("dangling %+v", o.Dangling)
	}

	mlx := fixture.Observe(t, jan.New(), env, "jan-mlx")
	fixture.Equal(t, "mlx errors", fixture.ErrorKinds(mlx), []string(nil))
	small := fixture.MustManifest(t, mlx, "jan", "Small-4bit")
	fixture.Equal(t, "mlx roles", fixture.Roles(small), []string{"weights"})
	fixture.Equal(t, "mlx holders", fixture.Holders(small), []string{"jan"})
}

// A model_sha256 that is not a sha256 is a malformed index; the model stays
// listed and held by Jan and loses only its digest.
func TestModelSHA256Malformed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "AppData", "Roaming", "Jan", "data", "llamacpp", "models", "acme", "Bad")
	write(t, filepath.Join(dir, "m.gguf"), "g")
	write(t, filepath.Join(dir, "model.yml"), "model_path: llamacpp/models/acme/Bad/m.gguf\nname: Bad\nmodel_sha256: nope\n")
	o := fixture.Observe(t, jan.New(), fixture.Env(home, nil), "jan")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index acme/Bad/model.yml"})
	fixture.Equal(t, "names", fixture.Names(o), []string{"jan:acme/Bad"})
	bad := fixture.MustManifest(t, o, "jan", "acme/Bad")
	fixture.Equal(t, "holders", fixture.Holders(bad), []string{"jan"})
	if e := bad.Manifest.Entries[0]; e.Digest != "" || e.Evidence != content.EvidenceNone {
		t.Fatalf("entry %+v, want no digest and evidence none", e)
	}
	if bad.Manifest.Addressing != content.AddressingName {
		t.Fatalf("addressing %q, want name", bad.Manifest.Addressing)
	}
}

// An MLX model_path naming a folder that holds one weight file gets no
// digest from model_sha256: Jan states that digest for a file model_path
// names.
func TestModelSHA256FolderOfOne(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "AppData", "Roaming", "Jan", "data")
	write(t, filepath.Join(data, "mlx", "models", "One-4bit", "weights", "model.safetensors"), strings.Repeat("s", 100))
	write(t, filepath.Join(data, "mlx", "models", "One-4bit", "model.yml"),
		"model_path: mlx/models/One-4bit/weights\nname: One\nmodel_sha256: "+fixture.Label("jan-one", 64)+"\n")
	o := fixture.Observe(t, jan.New(), fixture.Env(home, nil), "jan-mlx")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	one := fixture.MustManifest(t, o, "jan", "One-4bit")
	if e := one.Manifest.Entries[0]; e.Digest != "" || e.Evidence != content.EvidenceNone {
		t.Fatalf("entry %+v, want no digest for a folder-named model", e)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
