package tree_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// A bare ".bin" is ambiguous: PyTorch's own checkpoint format uses it, and so
// does vendored tooling a model host ships beside its store (the ROCm
// benchmark matrices under Lemonade's cache that inflated a real inventory —
// research/model-bridge/EXPERIMENT-2026-09-22.md). Name alone cannot tell
// them apart; a table over the evidence IsWeightFile checks for can. A weight
// ".bin" is listed as an object; it carries a manifest only when its header is
// GGUF, because every other weight ".bin" is a PyTorch pickle OA never loads
// (CONTRACT.md LS-1, LS-2).
func TestBinClassification(t *testing.T) {
	cases := []struct {
		name string
		// files maps a slash-separated path, relative to the tree root, to
		// its content.
		files map[string]string
		bin   string
		// listed: an object; want: a manifest, a loadable weight.
		listed, want bool
	}{
		{
			name: "bare bin alone is not a weight",
			files: map[string]string{
				"bin/therock/clients/matrices/gemm.bin": "not a model",
			},
			bin:  "bin/therock/clients/matrices/gemm.bin",
			want: false,
		},
		{
			name: "bin beside a transformers config.json is a listed pickle",
			files: map[string]string{
				"acme/model/config.json":       `{"architectures":["AcmeForCausalLM"]}`,
				"acme/model/pytorch_model.bin": "weights",
			},
			bin:    "acme/model/pytorch_model.bin",
			listed: true,
		},
		{
			name: "bin beside a safetensors shard index is a listed pickle",
			files: map[string]string{
				"acme/model/model.safetensors.index.json": `{"weight_map":{}}`,
				"acme/model/pytorch_model.bin":            "weights",
			},
			bin:    "acme/model/pytorch_model.bin",
			listed: true,
		},
		{
			name: "bin inside a Hugging Face snapshots layout is a listed pickle",
			files: map[string]string{
				"models--acme--Weird/snapshots/" + fixture.Label("weird-rev", 40) + "/pytorch_model.bin": "weights",
			},
			bin:    "models--acme--Weird/snapshots/" + fixture.Label("weird-rev", 40) + "/pytorch_model.bin",
			listed: true,
		},
		{
			name: "bin whose header is GGUF is a loadable weight",
			files: map[string]string{
				"acme/model/config.json":    `{}`,
				"acme/model/ggml-model.bin": "GGUF\x03\x00\x00\x00",
			},
			bin:    "acme/model/ggml-model.bin",
			listed: true,
			want:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range c.files {
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			loc := source.NewStore("test", "test", root, "test")
			env := fixture.Env(root, nil)
			o := tree.Observe(loc, env, tree.Options{Scheme: "test"})
			_, got := fixture.Manifest(o, "test", c.bin)
			if got != c.want {
				t.Fatalf("%s: manifest for %s present=%v, want %v (manifests=%v)", c.name, c.bin, got, c.want, fixture.Names(o))
			}
			if _, listed := fixture.Object(o, c.bin); listed != c.listed {
				t.Fatalf("%s: object %s listed=%v, want %v (objects=%v)", c.name, c.bin, listed, c.listed, fixture.Locators(o))
			}
		})
	}
}

// OA never loads a pickle (CONTRACT.md LS-1, VISION 2026-10-01): a .pth,
// .pt or .ckpt file is listed as an object, with no manifest offering it as a
// model, beside a safetensors file that keeps its manifest.
func TestPicklesAreListedNeverLoadable(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"ESRGAN/upscale.pth", "checkpoints/old.ckpt", "enc/model.pt", "checkpoints/new.safetensors"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := tree.Observe(source.NewStore("test", "test", root, "test"), fixture.Env(root, nil), tree.Options{Scheme: "test"})
	fixture.Equal(t, "objects", fixture.Locators(o), []string{"ESRGAN/upscale.pth", "checkpoints/new.safetensors", "checkpoints/old.ckpt", "enc/model.pt"})
	fixture.Equal(t, "manifests", fixture.Names(o), []string{"test:checkpoints/new.safetensors"})
}

// A projector or VAE is a component of a family, never a family of its own
// (CONTRACT.md MODEL-C3): RoleOf classifies it, and its own file name, read
// through identity.Component, names the family it belongs to when the name
// derives one. A plain checkpoint beside them carries no role at all.
func TestComponentDescriptorNamesItsRoleAndBase(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"qwen3.6-vl-dev.gguf":            "weights",
		"mmproj-qwen3.6-vl-dev.gguf":     "weights",
		"checkpoints/wan2.2.safetensors": "weights",
		"vae/wan2.2-vae.safetensors":     "weights",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loc := source.NewStore("test", "test", root, "test")
	env := fixture.Env(root, nil)
	o := tree.Observe(loc, env, tree.Options{Scheme: "test"})

	mm := fixture.MustManifest(t, o, "test", "mmproj-qwen3.6-vl-dev.gguf")
	if mm.Manifest.Descriptor["role"] != "projector" || mm.Manifest.Descriptor["base"] != "qwen3.6-vl-dev" {
		t.Fatalf("mmproj descriptor = %v, want role projector, base qwen3.6-vl-dev", mm.Manifest.Descriptor)
	}
	vae := fixture.MustManifest(t, o, "test", "vae/wan2.2-vae.safetensors")
	if vae.Manifest.Descriptor["role"] != "vae" || vae.Manifest.Descriptor["base"] != "wan2.2" {
		t.Fatalf("vae descriptor = %v, want role vae, base wan2.2", vae.Manifest.Descriptor)
	}
	ckpt := fixture.MustManifest(t, o, "test", "checkpoints/wan2.2.safetensors")
	if ckpt.Manifest.Descriptor["role"] != "" {
		t.Fatalf("checkpoint descriptor carries a role it should not: %v", ckpt.Manifest.Descriptor)
	}
}

// LS-13: a tree names no file by content (an app's model folder, an
// operator-named root). A listing hashes nothing; a request hashes one
// weights file once, and listings then show its SHA-256 on the object and
// the entry while the manifest keeps its id and name addressing. A changed
// stamp drops the digest and keeps the id. A pickle is never hashed.
func TestTreeWeightsAreHashedOnRequest(t *testing.T) {
	root := t.TempDir()
	body := []byte("tree weights")
	file := filepath.Join(root, "pub", "model", "model-Q4_K_M.gguf")
	for path, b := range map[string][]byte{file: body, filepath.Join(root, "old", "upscale.pth"): []byte("pickle")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := fixture.Env(root, nil)
	env.Digests = source.NewDigests("")
	loc := source.NewStore("test", "test", root, "test")
	o := tree.Observe(loc, env, tree.Options{Scheme: "test"})
	before := fixture.MustManifest(t, o, "test", "pub/model/model-Q4_K_M.gguf").Manifest
	if w, _ := fixture.Object(o, "pub/model/model-Q4_K_M.gguf"); w.Digest != "" || env.Digests.Wait() {
		t.Fatalf("a listing hashed %+v", w)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Digests.Request(file, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	env.Digests.Wait()
	o = tree.Observe(loc, env, tree.Options{Scheme: "test"})
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if w, _ := fixture.Object(o, "pub/model/model-Q4_K_M.gguf"); w.Digest != want || w.Evidence != content.EvidenceHashed {
		t.Fatalf("hashed object %+v, want %s", w, want)
	}
	after := fixture.MustManifest(t, o, "test", "pub/model/model-Q4_K_M.gguf").Manifest
	if after.ID != before.ID || after.Addressing != content.AddressingName || after.Entries[0].Digest != want {
		t.Fatalf("manifest after the hash %+v, before %s", after, before.ID)
	}
	if p, _ := fixture.Object(o, "old/upscale.pth"); p.Digest != "" {
		t.Fatalf("a pickle was hashed: %+v", p)
	}
	later := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	o = tree.Observe(loc, env, tree.Options{Scheme: "test"})
	changed := fixture.MustManifest(t, o, "test", "pub/model/model-Q4_K_M.gguf").Manifest
	if changed.ID != before.ID || changed.Entries[0].Digest != "" || env.Digests.Wait() {
		t.Fatalf("after a stamp change %+v", changed)
	}
}
