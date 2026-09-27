package tree_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// A bare ".bin" is ambiguous: PyTorch's own checkpoint format uses it, and so
// does vendored tooling a model host ships beside its store (the ROCm
// benchmark matrices under Lemonade's cache that inflated a real inventory —
// research/model-bridge/EXPERIMENT-2026-09-22.md). Name alone cannot tell
// them apart; a table over the evidence IsWeightFile checks for can.
func TestBinClassification(t *testing.T) {
	cases := []struct {
		name string
		// files maps a slash-separated path, relative to the tree root, to
		// its content.
		files map[string]string
		bin   string
		want  bool
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
			name: "bin beside a transformers config.json is a weight",
			files: map[string]string{
				"acme/model/config.json":      `{"architectures":["AcmeForCausalLM"]}`,
				"acme/model/pytorch_model.bin": "weights",
			},
			bin:  "acme/model/pytorch_model.bin",
			want: true,
		},
		{
			name: "bin beside a safetensors shard index is a weight",
			files: map[string]string{
				"acme/model/model.safetensors.index.json": `{"weight_map":{}}`,
				"acme/model/pytorch_model.bin":             "weights",
			},
			bin:  "acme/model/pytorch_model.bin",
			want: true,
		},
		{
			name: "bin inside a Hugging Face snapshots layout is a weight",
			files: map[string]string{
				"models--acme--Weird/snapshots/" + fixture.Label("weird-rev", 40) + "/pytorch_model.bin": "weights",
			},
			bin:  "models--acme--Weird/snapshots/" + fixture.Label("weird-rev", 40) + "/pytorch_model.bin",
			want: true,
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
		})
	}
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
