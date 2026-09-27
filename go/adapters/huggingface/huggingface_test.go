package huggingface_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

func TestDiscovery(t *testing.T) {
	home := fixture.Home(t, "hf")
	a := huggingface.New()

	d := a.Discover(fixture.Env(home, nil))
	hub := fixture.Location(t, d, "huggingface")
	if !hub.Present || !fixture.SamePath(hub.Locator, filepath.Join(home, ".cache", "huggingface", "hub")) || !strings.Contains(hub.Rule, "default") {
		t.Fatalf("default hub %+v", hub)
	}
	extra := fixture.Location(t, d, "lemonade-extra")
	if !extra.Present || extra.Program != "lemonade" || !fixture.SamePath(extra.Locator, filepath.Join(home, "LemonadeExtra")) || !strings.Contains(extra.Rule, "extra_models_dir") {
		t.Fatalf("lemonade extra %+v", extra)
	}

	for _, c := range []struct {
		vars map[string]string
		root string
		rule string
	}{
		{map[string]string{"HF_HUB_CACHE": filepath.Join(home, "a")}, filepath.Join(home, "a"), "HF_HUB_CACHE"},
		{map[string]string{"HUGGINGFACE_HUB_CACHE": filepath.Join(home, "b")}, filepath.Join(home, "b"), "HUGGINGFACE_HUB_CACHE"},
		{map[string]string{"HF_HOME": filepath.Join(home, "c")}, filepath.Join(home, "c", "hub"), "HF_HOME"},
		{map[string]string{"HF_HUB_CACHE": filepath.Join(home, "a"), "HF_HOME": filepath.Join(home, "c")}, filepath.Join(home, "a"), "HF_HUB_CACHE"},
	} {
		d := a.Discover(fixture.Env(home, c.vars))
		var found bool
		for _, l := range d.Stores {
			if l.Name == "huggingface" && fixture.SamePath(l.Locator, c.root) && strings.Contains(l.Rule, c.rule) {
				found = true
			}
		}
		if !found {
			t.Errorf("vars %v: no hub at %s by %s in %+v", c.vars, c.root, c.rule, d.Stores)
		}
	}
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, huggingface.New(), "huggingface", ".cache/huggingface/hub")
}

func TestSnapshotsRefsAndLemonade(t *testing.T) {
	home := fixture.Home(t, "hf")
	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	partialRev := fixture.Label("partial-rev", 40)
	// A revision no ref names carries its commit in the name, so a name-addressed
	// id never collides with the same file in the revision refs/main names.
	oldRev := fixture.Label("sold", 40)
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"hf:acme/Partial-GGUF@" + partialRev[:12],
		"hf:acme/Shard:Q4_K_XL/S-Q4_K_XL.gguf",
		"hf:acme/Shard@" + oldRev[:12] + ":Shard-old.gguf",
		"hf:acme/Tiny-GGUF:Tiny-Q4_K_M.gguf",
		"hf:acme/Tiny-GGUF:Tiny-Q8_0.gguf",
		"hf:acme/Tiny-GGUF:mmproj-F16.gguf",
	})

	// One unit held by two programs: the hub's ref and Lemonade's registry.
	q4 := fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	fixture.Equal(t, "q4 holders", fixture.Holders(q4), []string{"huggingface", "lemonade"})
	fixture.Equal(t, "q4 roles", fixture.Roles(q4), []string{"weights", "weights.config"})
	if e := q4.Manifest.Entries[0]; e.Size != 3000 || e.Evidence != content.EvidenceNone || e.Digest != "" || e.MediaType != "application/vnd.gguf" {
		t.Fatalf("q4 entry %+v", e)
	}
	fixture.Equal(t, "q8 holders", fixture.Holders(fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q8_0.gguf")), []string{"huggingface"})
	mm := fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:mmproj-F16.gguf")
	fixture.Equal(t, "mmproj holders", fixture.Holders(mm), []string{"huggingface", "lemonade"})
	fixture.Equal(t, "mmproj roles", fixture.Roles(mm), []string{"projector", "weights.config"})

	// A multi-entry manifest from shards; held by user_models.json.
	sh := fixture.MustManifest(t, o, "hf", "acme/Shard:Q4_K_XL/S-Q4_K_XL.gguf")
	fixture.Equal(t, "shard roles", fixture.Roles(sh), []string{"weights", "weights"})
	fixture.Equal(t, "shard holders", fixture.Holders(sh), []string{"huggingface", "lemonade"})
	// A detached revision: no ref names it, nobody holds it.
	fixture.Equal(t, "detached holders", fixture.Holders(fixture.MustManifest(t, o, "hf", "acme/Shard@"+oldRev[:12]+":Shard-old.gguf")), []string(nil))
	snap := fixture.MustManifest(t, o, "hf", "acme/Partial-GGUF@"+partialRev[:12])
	if snap.Manifest.Kind != source.KindSnapshot {
		t.Fatalf("weightless revision kind %s", snap.Manifest.Kind)
	}

	// Lemonade's evidence is recorded per hold: the index, the model key and
	// the role inside it.
	fixture.Equal(t, "q4 bases", fixture.Bases(q4), []string{
		"huggingface models--acme--Tiny-GGUF/refs/main|main|",
		"lemonade models--acme--Tiny-GGUF/.lemonade_registry.json|Tiny-GGUF|main",
	})
	fixture.Equal(t, "mmproj bases", fixture.Bases(mm), []string{
		"huggingface models--acme--Tiny-GGUF/refs/main|main|",
		"lemonade models--acme--Tiny-GGUF/.lemonade_registry.json|Tiny-GGUF|mmproj",
	})
	var userModels bool
	for _, h := range sh.Holds {
		if h.Holder.Program == "lemonade" && h.Target == sh.Manifest.ID && h.Basis != nil && strings.HasSuffix(filepath.ToSlash(h.Basis.Index), ".config/lemonade/user_models.json") && h.Basis.Key == "Shard-Q4_K_XL" && h.Basis.Role == "main" {
			userModels = true
		}
	}
	if !userModels {
		t.Fatalf("user_models.json basis missing: %v", fixture.Bases(sh))
	}
	if len(o.Dangling) != 1 || o.Dangling[0].Holder.Program != "lemonade" || o.Dangling[0].Reference != "acme/Tiny-GGUF:Q3_K_M" ||
		o.Dangling[0].Basis.Key != "user.Tiny-GGUF-Q3_K_M" || o.Dangling[0].Basis.Role != "main" {
		t.Fatalf("dangling %+v", o.Dangling)
	}

	// 4 Tiny files, 3 Sharded files, README and the .incomplete blob.
	if len(o.Objects) != 9 {
		t.Fatalf("objects %v", fixture.Locators(o))
	}
	etag := fixture.Label("partial-etag", 64)
	inc, ok := fixture.Object(o, "models--acme--Partial-GGUF/blobs/"+etag+".incomplete")
	if !ok || !inc.Partial || inc.Size != 1234 || inc.Evidence != content.EvidenceNone {
		t.Fatalf("incomplete blob %+v", inc)
	}
	for _, m := range o.Manifests {
		for _, l := range source.EntryLocators(m.Manifest) {
			if strings.HasSuffix(l, ".incomplete") {
				t.Fatalf("a manifest names the partial blob: %v", m.Manifest.Names)
			}
		}
	}
}

func TestLemonadeExtraModelsDir(t *testing.T) {
	home := fixture.Home(t, "hf")
	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "lemonade-extra")
	m := fixture.MustManifest(t, o, "lemonade", "local-model.gguf")
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"lemonade"})
}

// An artifact in the cache publishes a model descriptor: the family and the
// quantisation its repository and file name state, and what the file's own
// GGUF header states.
func TestAnArtifactPublishesADescriptor(t *testing.T) {
	home := fixture.Home(t, "hf")
	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	q4 := fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	if q4.Manifest.Descriptor["family"] != "tiny" || q4.Manifest.Descriptor["quant"] != "q4_k_m" ||
		q4.Manifest.Descriptor["source"] != "huggingface" || q4.Manifest.Descriptor["architecture"] != "" {
		t.Fatalf("descriptor from the name alone %v", q4.Manifest.Descriptor)
	}

	root := filepath.Join(home, ".cache", "huggingface", "hub")
	weights := filepath.Join(root, filepath.FromSlash(q4.Manifest.Entries[0].Locator))
	st, err := os.Stat(weights)
	if err != nil {
		t.Fatal(err)
	}
	fixture.WriteGGUF(t, weights, st.Size(), map[string]string{
		"general.architecture": "llama",
		"general.basename":     "Tiny",
		"general.size_label":   "135M",
		"general.finetune":     "Instruct",
		"general.file_type":    "15",
	})
	o = fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	q4 = fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	want := map[string]string{
		"family": "tiny", "quant": "q4_k_m", "format": "gguf", "architecture": "llama",
		"size_label": "135M", "finetune": "Instruct", "source": "huggingface",
	}
	if !reflect.DeepEqual(q4.Manifest.Descriptor, want) {
		t.Fatalf("descriptor %v, want %v", q4.Manifest.Descriptor, want)
	}
}

func TestMalformedIndexes(t *testing.T) {
	home := fixture.Home(t, "hf-bad")
	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	var kinds []string
	for _, e := range o.Errors {
		if e.Kind != source.ErrorMalformedIndex {
			t.Fatalf("error kind %+v", e)
		}
		kinds = append(kinds, filepath.Base(e.Locator))
	}
	fixture.Equal(t, "malformed indexes", sorted(kinds), []string{".lemonade_registry.json", "config.json", "main", "user_models.json"})
	bad := fixture.MustManifest(t, o, "hf", "acme/Bad-GGUF:Bad-Q4_K_M.gguf")
	fixture.Equal(t, "holders", fixture.Holders(bad), []string{"huggingface"})
}

// A repository with blobs/ and snapshot links, as huggingface_hub writes where
// symlinks are allowed. Built at test time because links do not survive every
// checkout; skipped where this account cannot create them.
func TestBlobsAndLinks(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--Linked-GGUF")
	rev := fixture.Label("linked-rev", 40)
	weights := fixture.Label("linked-weights", 64)
	config := fixture.Label("linked-config", 40)
	mustWrite(t, filepath.Join(repo, "refs", "main"), rev)
	mustWrite(t, filepath.Join(repo, "blobs", weights), strings.Repeat("w", 2048))
	mustWrite(t, filepath.Join(repo, "blobs", config), "{}")
	mustWrite(t, filepath.Join(repo, "blobs", fixture.Label("linked-orphan", 64)), "orphan")
	snap := filepath.Join(repo, "snapshots", rev)
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", weights), filepath.Join(snap, "Linked-Q4_K_M.gguf")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", config), filepath.Join(snap, "config.json")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", fixture.Label("linked-gone", 64)), filepath.Join(snap, "gone.gguf")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	// Objects are the blobs; the links are the cache's index.
	fixture.Equal(t, "objects", fixture.Locators(o), sorted([]string{
		"models--acme--Linked-GGUF/blobs/" + weights,
		"models--acme--Linked-GGUF/blobs/" + config,
		"models--acme--Linked-GGUF/blobs/" + fixture.Label("linked-orphan", 64),
	}))
	w, _ := fixture.Object(o, "models--acme--Linked-GGUF/blobs/"+weights)
	if w.Evidence != content.EvidenceNamed || w.Digest != "sha256:"+weights || w.Size != 2048 {
		t.Fatalf("weights blob %+v", w)
	}
	c, _ := fixture.Object(o, "models--acme--Linked-GGUF/blobs/"+config)
	if c.Evidence != content.EvidenceForeign || c.Digest != "gitsha1:"+config {
		t.Fatalf("config blob %+v", c)
	}
	m := fixture.MustManifest(t, o, "hf", "acme/Linked-GGUF:Linked-Q4_K_M.gguf")
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"huggingface"})
	if e := m.Manifest.Entries[0]; e.Digest != "sha256:"+weights || e.Evidence != content.EvidenceNamed || e.Size != 2048 {
		t.Fatalf("linked weights entry %+v", e)
	}
	if len(o.Dangling) != 1 || o.Dangling[0].Holder.Program != "huggingface" || !strings.HasSuffix(o.Dangling[0].Basis.Key, "gone.gguf") ||
		o.Dangling[0].ExpectedDigest != "sha256:"+fixture.Label("linked-gone", 64) {
		t.Fatalf("dangling %+v", o.Dangling)
	}
}

// A repository with blobs/ and a snapshot huggingface_hub wrote as plain
// copies, not links: what it falls back to when the process lacks the
// symlink privilege, measured on the owner's own Windows machine without
// Developer Mode (research/model-bridge/EXPERIMENT-2026-09-22.md — every
// digest there read "(none)"). The copy carries no name or metadata pointing
// back at its blob; the fix matches it to the blob by content, so it reads
// the same digest a symlink to that blob would.
func TestBlobsWithoutLinks(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--Copied-GGUF")
	rev := fixture.Label("copied-rev", 40)
	weights := fixture.Label("copied-weights", 64)
	config := fixture.Label("copied-config", 40)
	weightsBody := strings.Repeat("w", 2048)
	configBody := "{}"
	mustWrite(t, filepath.Join(repo, "refs", "main"), rev)
	mustWrite(t, filepath.Join(repo, "blobs", weights), weightsBody)
	mustWrite(t, filepath.Join(repo, "blobs", config), configBody)
	snap := filepath.Join(repo, "snapshots", rev)
	// Plain copies, not links: what a snapshot holds without the symlink
	// privilege. Reproduces the miss: before the fix, these carried
	// Evidence: EvidenceNone, Digest: "", the same as TestSnapshotsRefsAndLemonade's
	// q4 entry over the "hf" fixture, which has never had a blobs/ directory
	// at all.
	mustWrite(t, filepath.Join(snap, "Copied-Q4_K_M.gguf"), weightsBody)
	mustWrite(t, filepath.Join(snap, "config.json"), configBody)

	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	w, ok := fixture.Object(o, "models--acme--Copied-GGUF/snapshots/"+rev+"/Copied-Q4_K_M.gguf")
	if !ok || w.Evidence != content.EvidenceNamed || w.Digest != "sha256:"+weights || w.Size != 2048 {
		t.Fatalf("copied weights object %+v, want the matching blob's digest sha256:%s", w, weights)
	}
	c, ok := fixture.Object(o, "models--acme--Copied-GGUF/snapshots/"+rev+"/config.json")
	if !ok || c.Evidence != content.EvidenceForeign || c.Digest != "gitsha1:"+config {
		t.Fatalf("copied config object %+v, want the matching blob's digest gitsha1:%s", c, config)
	}
	m := fixture.MustManifest(t, o, "hf", "acme/Copied-GGUF:Copied-Q4_K_M.gguf")
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"huggingface"})
	if e := m.Manifest.Entries[0]; e.Digest != "sha256:"+weights || e.Evidence != content.EvidenceNamed || e.Size != 2048 {
		t.Fatalf("copied weights entry %+v, want the matching blob's digest sha256:%s", e, weights)
	}
}

// A repository with refs/ and snapshots/ and no blobs/ directory at all:
// Lemonade's own downloader writes a repository's files straight into
// snapshots/<rev>/ without ever creating a blob to link or copy from
// (lemonade-sdk/lemonade, src/cpp/server/model_manager.cpp). Every
// repository in the owner's Hugging Face cache is this layout
// (research/model-bridge/EXPERIMENT-2026-09-22.md). There is no blob for
// matchBlobBySize to find: the entry keeps an empty digest and EvidenceNone,
// and the manifest still holds a complete, name-addressed identity and its
// holds.
func TestNoBlobsDirectory(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--Bare-GGUF")
	rev := fixture.Label("bare-rev", 40)
	mustWrite(t, filepath.Join(repo, "refs", "main"), rev)
	snap := filepath.Join(repo, "snapshots", rev)
	mustWrite(t, filepath.Join(snap, "Bare-Q4_K_M.gguf"), strings.Repeat("w", 4096))
	mustWrite(t, filepath.Join(snap, "config.json"), "{}")

	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	if _, err := os.Stat(filepath.Join(repo, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("fixture grew a blobs/ directory: %v", err)
	}
	w, ok := fixture.Object(o, "models--acme--Bare-GGUF/snapshots/"+rev+"/Bare-Q4_K_M.gguf")
	if !ok || w.Evidence != content.EvidenceNone || w.Digest != "" || w.Size != 4096 {
		t.Fatalf("bare weights object %+v, want an empty digest with no blob to match", w)
	}
	m := fixture.MustManifest(t, o, "hf", "acme/Bare-GGUF:Bare-Q4_K_M.gguf")
	if m.Manifest.Addressing != content.AddressingName {
		t.Fatalf("manifest addressing %q, want name addressing with no digest to hash by", m.Manifest.Addressing)
	}
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"huggingface"})
	if e := m.Manifest.Entries[0]; e.Digest != "" || e.Evidence != content.EvidenceNone || e.Size != 4096 {
		t.Fatalf("bare weights entry %+v, want the manifest complete with an empty digest", e)
	}
}

func mustWrite(t *testing.T, p, body string) {
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
