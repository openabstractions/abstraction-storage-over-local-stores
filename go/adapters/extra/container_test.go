package extra_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// zipOf stores each entry uncompressed, except names in deflate.
func zipOf(t *testing.T, entries map[string][]byte, deflate ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range []string{"w-Q4_K_M.gguf", "enc.pth", "inner.zip", "mmproj-f16.gguf", "readme.txt"} {
		data, ok := entries[name]
		if !ok {
			continue
		}
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		for _, d := range deflate {
			if d == name {
				h.Method = zip.Deflate
			}
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOperatorRootReadsContainers(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Packages")
	if err := os.MkdirAll(filepath.Join(root, "asus"), 0o700); err != nil {
		t.Fatal(err)
	}
	inner := zipOf(t, map[string][]byte{"w-Q4_K_M.gguf": []byte("inner weights")})
	pkg := zipOf(t, map[string][]byte{
		"w-Q4_K_M.gguf":   bytes.Repeat([]byte{1}, 4096),
		"mmproj-f16.gguf": bytes.Repeat([]byte{2}, 64),
		"enc.pth":         bytes.Repeat([]byte{3}, 64),
		"inner.zip":       inner,
		"readme.txt":      []byte("hello"),
	}, "enc.pth", "inner.zip")
	for name, b := range map[string][]byte{
		"asus/model.zip":  pkg,
		"broken.zip":      []byte("PK not really a zip"),
		"loose-f16.gguf":  []byte("loose"),
		"notes/other.zip": zipOf(t, map[string][]byte{"readme.txt": []byte("x")}),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := fixture.Env(home, nil)
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "packages", Path: root}}
	o := fixture.Observe(t, extra.New(), env, "packages")

	fixture.Equal(t, "objects", fixture.Locators(o), []string{
		"asus/model.zip!/enc.pth",
		"asus/model.zip!/inner.zip",
		"asus/model.zip!/mmproj-f16.gguf",
		"asus/model.zip!/w-Q4_K_M.gguf",
		"loose-f16.gguf",
	})
	fixture.Equal(t, "manifests", fixture.Names(o), []string{
		"packages:asus/model.zip!/mmproj-f16.gguf",
		"packages:asus/model.zip!/w-Q4_K_M.gguf",
		"packages:loose-f16.gguf",
	})
	m := fixture.MustManifest(t, o, "packages", "asus/model.zip!/w-Q4_K_M.gguf")
	e := m.Manifest.Entries[0]
	if e.Locator != "asus/model.zip!/w-Q4_K_M.gguf" || e.Size != 4096 || e.Evidence != content.EvidenceNone || e.Digest != "" {
		t.Fatalf("entry %+v", e)
	}
	if m.Manifest.Descriptor["family"] == "" || m.Manifest.Descriptor["quant"] != "q4_k_m" || m.Manifest.Descriptor["format"] != "gguf" {
		t.Fatalf("descriptor %v", m.Manifest.Descriptor)
	}
	fixture.Equal(t, "component role", fixture.Roles(fixture.MustManifest(t, o, "packages", "asus/model.zip!/mmproj-f16.gguf")), []string{source.RoleProjector})
	if obj, _ := fixture.Object(o, "asus/model.zip!/enc.pth"); obj.Size != 64 || obj.Digest != "" {
		t.Fatalf("pickle object %+v", obj)
	}
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{string(content.StoreErrorKindOther) + " broken.zip"})
}
