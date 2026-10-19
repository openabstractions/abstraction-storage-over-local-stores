package service_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// LS-13 on demand: a listing asks for no hash. Verify on one model
// manifest's id asks for its weights file only, and a same-size file in the
// same store that nobody asked about is never hashed.
func TestVerifyHashesOnlyTheManifestAskedFor(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "models")
	for _, name := range []string{"asked-Q4_K_M.gguf", "other-Q4_K_M.gguf"} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte("same size"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := fixture.Env(home, nil)
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "mine", Path: root}}
	env.Digests = source.NewDigests("")
	src := service.New(func() snapshot.Result { return snapshot.Take(env, []source.Adapter{extra.New()}) })
	var asked []string
	src.Hash = func(path string, size int64, modified time.Time) error {
		asked = append(asked, path)
		return env.Digests.Request(path, size, modified)
	}
	if _, err := src.Snapshot("", 256); err != nil || len(asked) != 0 {
		t.Fatalf("a listing asked for %v, %v", asked, err)
	}
	var id string
	for _, m := range snapshot.Take(env, []source.Adapter{extra.New()}).Page.Manifests {
		for _, n := range m.Manifest.Names {
			if filepath.Base(n.Name) == "asked-Q4_K_M.gguf" {
				id = m.Manifest.ID
			}
		}
	}
	if id == "" {
		t.Fatal("no manifest for the asked file")
	}
	if page, err := src.Verify(id); err != nil || len(page.Manifests) != 1 {
		t.Fatalf("verify = %+v, %v", page, err)
	}
	env.Digests.Wait()
	if len(asked) != 1 || filepath.Base(asked[0]) != "asked-Q4_K_M.gguf" {
		t.Fatalf("asked %v", asked)
	}
	page, _ := src.Verify(id)
	if len(page.Manifests) != 1 || page.Manifests[0].Manifest.ID != id || page.Manifests[0].Manifest.Entries[0].Digest == "" {
		t.Fatalf("after the hash %+v", page.Manifests)
	}
	for _, m := range snapshot.Take(env, []source.Adapter{extra.New()}).Page.Manifests {
		if m.Manifest.ID != id && m.Manifest.Entries[0].Digest != "" {
			t.Fatalf("an unasked file was hashed: %+v", m.Manifest)
		}
	}
}
