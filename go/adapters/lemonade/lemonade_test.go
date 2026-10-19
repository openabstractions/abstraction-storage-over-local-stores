package lemonade_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/lemonade"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

func TestDiscovery(t *testing.T) {
	home := fixture.Home(t, "hf")
	d := lemonade.New().Discover(fixture.Env(home, nil))
	hub := fixture.Location(t, d, "lemonade-cache")
	if !hub.Present || hub.Program != "lemonade" || !fixture.SamePath(hub.Locator, filepath.Join(home, ".cache", "huggingface", "hub")) {
		t.Fatalf("shared cache %+v", hub)
	}
	extra := fixture.Location(t, d, "lemonade-extra")
	if !extra.Present || extra.Program != "lemonade" || !fixture.SamePath(extra.Locator, filepath.Join(home, "LemonadeExtra")) || !strings.Contains(extra.Rule, "extra_models_dir") {
		t.Fatalf("lemonade extra %+v", extra)
	}
	// Selections of huggingface repositories become claims on their files.
	var keys []string
	for _, c := range d.Claims {
		if c.Holder.Program != "lemonade" || c.Purpose != source.PurposeInstalled {
			t.Fatalf("claim %+v", c)
		}
		keys = append(keys, c.Basis.Key+"|"+c.Basis.Role)
	}
	fixture.Equal(t, "claims", sorted(keys), []string{"Shard-Q4_K_XL|main", "Tiny-GGUF|main", "Tiny-GGUF|mmproj"})
}

// Composed with the huggingface adapter, Lemonade's holds land on the
// manifests the cache reader publishes, with Lemonade's index as the basis.
func TestHoldsOnTheSharedCache(t *testing.T) {
	home := fixture.Home(t, "hf")
	r := snapshot.Take(fixture.Env(home, nil), []source.Adapter{huggingface.New(), lemonade.New()})
	var hf source.StoreObservation
	for _, s := range r.Stores {
		if s.Name == "huggingface" {
			hf = s
		}
	}
	q4 := fixture.MustManifest(t, hf, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	fixture.Equal(t, "q4 bases", fixture.Bases(q4), []string{
		"huggingface models--acme--Tiny-GGUF/refs/main|main|",
		"lemonade models--acme--Tiny-GGUF/.lemonade_registry.json|Tiny-GGUF|main",
	})
	mm := fixture.MustManifest(t, hf, "hf", "acme/Tiny-GGUF:mmproj-F16.gguf")
	fixture.Equal(t, "mmproj holders", fixture.Holders(mm), []string{"huggingface", "lemonade"})
	sh := fixture.MustManifest(t, hf, "hf", "acme/Shard:Q4_K_XL/S-Q4_K_XL.gguf")
	var userModels bool
	for _, h := range sh.Holds {
		if h.Holder.Program == "lemonade" && h.Basis != nil && strings.HasSuffix(filepath.ToSlash(h.Basis.Index), ".config/lemonade/user_models.json") && h.Basis.Key == "Shard-Q4_K_XL" {
			userModels = true
		}
	}
	if !userModels {
		t.Fatalf("user_models.json basis missing: %v", fixture.Bases(sh))
	}
	fixture.Equal(t, "q8 holders", fixture.Holders(fixture.MustManifest(t, hf, "hf", "acme/Tiny-GGUF:Tiny-Q8_0.gguf")), []string{"huggingface"})

	o := fixture.Observe(t, lemonade.New(), fixture.Env(home, nil), "lemonade-cache")
	if len(o.Objects) != 0 || len(o.Manifests) != 0 || len(o.Errors) != 0 {
		t.Fatalf("the shared cache store reads what the huggingface adapter reads: %v %v", fixture.Locators(o), o.Errors)
	}
	if len(o.Dangling) != 1 || o.Dangling[0].Reference != "acme/Tiny-GGUF:Q3_K_M" || o.Dangling[0].Basis.Key != "user.Tiny-GGUF-Q3_K_M" {
		t.Fatalf("dangling %+v", o.Dangling)
	}
}

func TestExtraModelsDir(t *testing.T) {
	home := fixture.Home(t, "hf")
	o := fixture.Observe(t, lemonade.New(), fixture.Env(home, nil), "lemonade-extra")
	m := fixture.MustManifest(t, o, "lemonade", "local-model.gguf")
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"lemonade"})
}

// Lemonade v2026.42 reads its state from the configuration directory first
// and the cache directory second; XDG_CONFIG_HOME, XDG_CACHE_HOME and
// LEMONADE_CACHE_DIR move them.
func TestConfigDirectoryOrder(t *testing.T) {
	for _, c := range []struct {
		name  string
		vars  func(home string) map[string]string
		files map[string]string // relative config.json path -> extra dir
		want  string
	}{
		{"config before cache", nil, map[string]string{".config/lemonade": "A", ".cache/lemonade": "B"}, "A"},
		{"cache when config has none", nil, map[string]string{".cache/lemonade": "B"}, "B"},
		{"XDG_CONFIG_HOME", func(h string) map[string]string { return map[string]string{"XDG_CONFIG_HOME": filepath.Join(h, "xc")} },
			map[string]string{"xc/lemonade": "X", ".config/lemonade": "A"}, "X"},
		{"LEMONADE_CACHE_DIR", func(h string) map[string]string {
			return map[string]string{"LEMONADE_CACHE_DIR": filepath.Join(h, "lc")}
		},
			map[string]string{"lc": "L", ".cache/lemonade": "B"}, "L"},
		{"XDG_CACHE_HOME", func(h string) map[string]string { return map[string]string{"XDG_CACHE_HOME": filepath.Join(h, "xk")} },
			map[string]string{"xk/lemonade": "K", ".cache/lemonade": "B"}, "K"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			for dir, extra := range c.files {
				write(t, filepath.Join(home, filepath.FromSlash(dir), "config.json"), `{"models_dir":"auto","extra_models_dir":"`+filepath.ToSlash(filepath.Join(home, extra))+`"}`)
			}
			var vars map[string]string
			if c.vars != nil {
				vars = c.vars(home)
			}
			d := lemonade.New().Discover(fixture.Env(home, vars))
			var got string
			for _, s := range d.Stores {
				if s.Name == "lemonade-extra" {
					got = filepath.Base(s.Locator)
				}
			}
			if got != c.want {
				t.Fatalf("extra_models_dir from %q, want %q (%+v)", got, c.want, d.Stores)
			}
		})
	}
}

// A ModelScope repository in the shared cache is Lemonade's: it reads it
// with the Hugging Face cache reader, names it in scheme modelscope, and
// holds what its index selects.
func TestModelScopeRepository(t *testing.T) {
	home := t.TempDir()
	hub := filepath.Join(home, ".cache", "huggingface", "hub")
	rev := fixture.Label("ms-rev", 40)
	repo := filepath.Join(hub, "modelscope--models--acme--MS-GGUF")
	write(t, filepath.Join(repo, "refs", "main"), rev)
	write(t, filepath.Join(repo, "snapshots", rev, "MS-Q4_K_M.gguf"), strings.Repeat("m", 1500))
	write(t, filepath.Join(home, ".config", "lemonade", "user_models.json"), `{"user.MS":{"checkpoint":"acme/MS-GGUF:Q4_K_M"}}`)

	env := fixture.Env(home, nil)
	o := fixture.Observe(t, lemonade.New(), env, "lemonade-cache")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string(nil))
	m := fixture.MustManifest(t, o, "modelscope", "acme/MS-GGUF:MS-Q4_K_M.gguf")
	fixture.Equal(t, "bases", fixture.Bases(m), []string{
		"lemonade " + filepath.Join(home, ".config", "lemonade", "user_models.json") + "|user.MS|main",
		"lemonade modelscope--models--acme--MS-GGUF/refs/main|main|",
	})
	if len(o.Dangling) != 0 {
		t.Fatalf("dangling %+v", o.Dangling)
	}
	// The huggingface adapter leaves ModelScope repositories alone.
	h := fixture.Observe(t, huggingface.New(), env, "huggingface")
	if len(h.Manifests) != 0 || len(h.Objects) != 0 {
		t.Fatalf("huggingface read a ModelScope repository: %v", fixture.Names(h))
	}
}

// A models_dir of Lemonade's own is a store Lemonade owns whole.
func TestOwnModelsDir(t *testing.T) {
	home := t.TempDir()
	own := filepath.Join(home, "LemonadeModels")
	rev := fixture.Label("own-rev", 40)
	repo := filepath.Join(own, "models--acme--Own-GGUF")
	write(t, filepath.Join(repo, "refs", "main"), rev)
	write(t, filepath.Join(repo, "snapshots", rev, "Own-Q8_0.gguf"), strings.Repeat("o", 800))
	write(t, filepath.Join(home, ".config", "lemonade", "config.json"), `{"models_dir":"`+filepath.ToSlash(own)+`"}`)
	write(t, filepath.Join(home, ".config", "lemonade", "user_models.json"), `{"user.Own":{"checkpoint":"acme/Own-GGUF:Q8_0"}}`)

	env := fixture.Env(home, nil)
	d := lemonade.New().Discover(env)
	if len(d.Claims) != 0 {
		t.Fatalf("claims on a store Lemonade owns: %+v", d.Claims)
	}
	o := fixture.Observe(t, lemonade.New(), env, "lemonade-models")
	m := fixture.MustManifest(t, o, "hf", "acme/Own-GGUF:Own-Q8_0.gguf")
	fixture.Equal(t, "bases", fixture.Bases(m), []string{
		"lemonade " + filepath.Join(home, ".config", "lemonade", "user_models.json") + "|user.Own|main",
		"lemonade models--acme--Own-GGUF/refs/main|main|",
	})
}

// Lemonade compares a full file name exactly and a quantization variant
// ignoring case (identify_gguf_models cases 1, 3 and 4): a file name spelled
// in another case names nothing.
func TestSelectionCase(t *testing.T) {
	home := t.TempDir()
	own := filepath.Join(home, "LemonadeModels")
	rev := fixture.Label("case-rev", 40)
	repo := filepath.Join(own, "models--acme--Case-GGUF")
	write(t, filepath.Join(repo, "refs", "main"), rev)
	write(t, filepath.Join(repo, "snapshots", rev, "Tiny-Q4_K_M.gguf"), strings.Repeat("c", 700))
	write(t, filepath.Join(home, ".config", "lemonade", "config.json"), `{"models_dir":"`+filepath.ToSlash(own)+`"}`)
	write(t, filepath.Join(home, ".config", "lemonade", "user_models.json"), `{
		"user.Exact":{"checkpoint":"acme/Case-GGUF:Tiny-Q4_K_M.gguf"},
		"user.Lower":{"checkpoint":"acme/Case-GGUF:tiny-q4_k_m.gguf"},
		"user.Variant":{"checkpoint":"acme/Case-GGUF:q4_k_m"}}`)

	o := fixture.Observe(t, lemonade.New(), fixture.Env(home, nil), "lemonade-models")
	index := filepath.Join(home, ".config", "lemonade", "user_models.json")
	m := fixture.MustManifest(t, o, "hf", "acme/Case-GGUF:Tiny-Q4_K_M.gguf")
	fixture.Equal(t, "bases", fixture.Bases(m), []string{
		"lemonade " + index + "|user.Exact|main",
		"lemonade " + index + "|user.Variant|main",
		"lemonade models--acme--Case-GGUF/refs/main|main|",
	})
	if len(o.Dangling) != 1 || o.Dangling[0].Reference != "acme/Case-GGUF:tiny-q4_k_m.gguf" || o.Dangling[0].Basis.Key != "user.Lower" {
		t.Fatalf("dangling %+v", o.Dangling)
	}
}

func TestMalformedIndexes(t *testing.T) {
	home := fixture.Home(t, "hf-bad")
	env := fixture.Env(home, nil)
	d := lemonade.New().Discover(env)
	if len(d.Errors) != 1 || d.Errors[0].Kind != source.ErrorMalformedIndex || filepath.Base(d.Errors[0].Locator) != "config.json" {
		t.Fatalf("discovery errors %+v", d.Errors)
	}
	o := fixture.Observe(t, lemonade.New(), env, "lemonade-cache")
	var kinds []string
	for _, e := range o.Errors {
		if e.Kind != source.ErrorMalformedIndex {
			t.Fatalf("error kind %+v", e)
		}
		kinds = append(kinds, filepath.Base(e.Locator))
	}
	fixture.Equal(t, "malformed indexes", sorted(kinds), []string{".lemonade_registry.json", "user_models.json"})
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

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
