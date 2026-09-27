package source

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

func noneEntry(locator string, size int64) content.Entry {
	return FileEntry(RoleWeights, locator, size)
}

// A digest-less manifest is name-addressed over names[0]: its id follows what
// the program calls it, never where the file lies.
func TestNameAddressedIDIgnoresLocatorAndStore(t *testing.T) {
	desc := Model{Format: "gguf"}.Map()
	a := NewManifest("store-a", KindModel, []content.Entry{noneEntry("x/a.gguf", 10)}, []content.Name{{Scheme: "hf", Name: "org/repo:a.gguf"}}, desc)
	moved := NewManifest("store-b", KindModel, []content.Entry{noneEntry("elsewhere/a.gguf", 10)}, []content.Name{{Scheme: "hf", Name: "org/repo:a.gguf"}, {Scheme: "lmstudio", Name: "x"}}, desc)
	other := NewManifest("store-a", KindModel, []content.Entry{noneEntry("x/a.gguf", 10)}, []content.Name{{Scheme: "hf", Name: "org/repo:b.gguf"}}, desc)
	if a.Addressing != content.AddressingName || a.ID != moved.ID {
		t.Fatalf("locator, store or a second name changed a name-addressed id: %s %s", a.ID, moved.ID)
	}
	if a.ID == other.ID {
		t.Fatalf("two digest-less manifests with different names share id %s", a.ID)
	}
	resized := NewManifest("store-a", KindModel, []content.Entry{noneEntry("x/a.gguf", 11)}, []content.Name{{Scheme: "hf", Name: "org/repo:a.gguf"}}, desc)
	if resized.ID == a.ID {
		t.Fatalf("size is part of the id")
	}
}

// A manifest whose every entry carries a hashed or named digest is
// content-addressed: names, locators and store never enter the id.
func TestContentAddressedIDIgnoresNames(t *testing.T) {
	entry := func(locator string) content.Entry {
		return content.Entry{Role: RoleWeights, Digest: "sha256:" + hex.EncodeToString(make([]byte, 32)), Size: 5, MediaType: "application/vnd.gguf", Evidence: content.EvidenceNamed, Locator: locator}
	}
	a := NewManifest("ollama", KindModel, []content.Entry{entry("blobs/a")}, []content.Name{{Scheme: "ollama", Name: "library/a:1"}}, nil)
	b := NewManifest("elsewhere", KindModel, []content.Entry{entry("blobs/b")}, []content.Name{{Scheme: "oci", Name: "r/a@x"}}, nil)
	if a.Addressing != content.AddressingContent || a.ID != b.ID {
		t.Fatalf("content-addressed ids differ: %s %s", a.ID, b.ID)
	}
	foreign := entry("blobs/c")
	foreign.Evidence, foreign.Digest = content.EvidenceForeign, "gitsha1:"+hex.EncodeToString(make([]byte, 20))
	if m := NewManifest("hf", KindModel, []content.Entry{entry("x"), foreign}, []content.Name{{Scheme: "hf", Name: "n"}}, nil); m.Addressing != content.AddressingName {
		t.Fatalf("a foreign digest makes a manifest name-addressed, got %s", m.Addressing)
	}
}

// The identity document is spelled exactly; a change to it changes every id a
// runtime has recorded.
func TestCanonicalIdentityDocument(t *testing.T) {
	entries := []content.Entry{{Role: "weights", Size: 3, MediaType: "application/vnd.gguf", Evidence: content.EvidenceNone, Locator: "ignored"}}
	names := []content.Name{{Scheme: "comfyui", Name: "a\"b\\c\n.gguf"}}
	got := string(canonicalIdentity(content.AddressingName, KindModel, entries, names, map[string]string{"source": "x", "format": "gguf"}))
	want := `{"kind":"abstraction.model/descriptor@1","entries":[{"role":"weights","size":3,"media_type":"application/vnd.gguf"}],"descriptor":{"format":"gguf","source":"x"},"names":[{"scheme":"comfyui","name":"a\"b\\c\n.gguf"}]}`
	if got != want {
		t.Fatalf("identity document\n got  %s\n want %s", got, want)
	}
	sum := sha256.Sum256([]byte(want))
	if m := NewManifest("s", KindModel, entries, names, map[string]string{"source": "x", "format": "gguf"}); m.ID != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("id %s is not the sha256 of the identity document", m.ID)
	}
	doc := string(canonicalIdentity(content.AddressingContent, KindObject, []content.Entry{{Role: "r", Digest: "sha256:ab", Size: 0, MediaType: "m"}}, names, nil))
	if doc != `{"kind":"abstraction.storage/object@1","entries":[{"role":"r","digest":"sha256:ab","size":0,"media_type":"m"}]}` {
		t.Fatalf("content identity document %s", doc)
	}
}

// A ".bin" file needs evidence beyond its extension; every other weight
// extension IsWeights recognises does not.
func TestIsWeightFile(t *testing.T) {
	write := func(t *testing.T, dir string, files map[string][]byte) {
		t.Helper()
		for rel, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name  string
		files map[string][]byte
		rel   string
		want  bool
	}{
		{"unambiguous extension needs no evidence", map[string][]byte{"model.safetensors": []byte("w")}, "model.safetensors", true},
		{"bare bin alone", map[string][]byte{"tools/matrix.bin": []byte("not a model")}, "tools/matrix.bin", false},
		{"bin beside config.json", map[string][]byte{"m/config.json": []byte("{}"), "m/pytorch_model.bin": []byte("w")}, "m/pytorch_model.bin", true},
		{"bin beside model.safetensors.index.json", map[string][]byte{"m/model.safetensors.index.json": []byte("{}"), "m/pytorch_model.bin": []byte("w")}, "m/pytorch_model.bin", true},
		{"bin under a snapshots segment", map[string][]byte{"models--a--b/snapshots/rev/pytorch_model.bin": []byte("w")}, "models--a--b/snapshots/rev/pytorch_model.bin", true},
		{"bin with a GGUF header", map[string][]byte{"odd.bin": []byte("GGUF" + "rest of a header")}, "odd.bin", true},
		{"bin with a zip header (torch.save)", map[string][]byte{"odd.bin": {'P', 'K', 0x03, 0x04, 0, 0}}, "odd.bin", true},
		{"bin with a pickle header (legacy torch.save)", map[string][]byte{"odd.bin": {0x80, 0x02, 0, 0}}, "odd.bin", true},
		{"bin too short to sniff", map[string][]byte{"odd.bin": {'P', 'K'}}, "odd.bin", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.files)
			full := filepath.Join(dir, filepath.FromSlash(c.rel))
			if got := IsWeightFile(full, c.rel); got != c.want {
				t.Fatalf("IsWeightFile(%q) = %v, want %v", c.rel, got, c.want)
			}
		})
	}
}
