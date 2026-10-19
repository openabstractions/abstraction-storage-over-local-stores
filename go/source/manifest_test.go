package source

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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

// LS-1: OA never loads a pickle. Every weight .pth, .pt and .ckpt is one, and
// so is every weight .bin whose header is neither GGUF's nor ggml's.
func TestIsPickle(t *testing.T) {
	for _, c := range []struct {
		rel  string
		body []byte
		want bool
	}{
		{"upscale.pth", []byte("w"), true},
		{"enc.PT", []byte("w"), true},
		{"old.ckpt", []byte("w"), true},
		{"checkpoint.pkl", []byte("w"), true},
		{"state.pickle", []byte("w"), true},
		{"pytorch_model.bin", []byte{'P', 'K', 0x03, 0x04}, true},
		{"legacy.bin", []byte{0x80, 0x02, 0, 0}, true},
		{"short.bin", []byte{'P'}, true},
		{"ggml-model.bin", []byte("GGUF\x03\x00\x00\x00"), false},
		{"ggml-medium.bin", []byte("lmgg\x80\xb1\x00\x00"), false},
		{"ggml-reversed.bin", []byte("ggml"), true},
		{"model.safetensors", []byte("w"), false},
		{"model.gguf", []byte("GGUF"), false},
	} {
		full := filepath.Join(t.TempDir(), c.rel)
		write(t, full, c.body)
		if got := IsPickle(full, c.rel); got != c.want {
			t.Fatalf("IsPickle(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// MODELBRIDGE-L21: the model formats OA loads, by extension or by GGUF's
// magic; a pickle and every other file are in none. An empty path judges a
// container member by name, so a GGUF .bin member is in none (LS-9).
func TestModelFormatOf(t *testing.T) {
	for _, c := range []struct {
		rel  string
		body []byte
		want string
	}{
		{"m.gguf", []byte("w"), "gguf"},
		{"m.safetensors", []byte("w"), "safetensors"},
		{"m.sft", []byte("w"), "safetensors"},
		{"m.onnx", []byte("w"), "onnx"},
		{"model.q4nx", []byte("w"), "q4nx"},
		{"blobs/sha256-" + strings.Repeat("a", 64), []byte("GGUF\x03\x00\x00\x00"), "gguf"},
		{"ggml.bin", []byte("GGUF\x03\x00\x00\x00"), "gguf"},
		{"ggml-medium.bin", []byte("lmgg\x80\xb1\x00\x00"), "ggml"},
		{"emb_2_3", []byte("lmgg"), "ggml"},
		{"pytorch_model.bin", []byte{'P', 'K', 0x03, 0x04}, ""},
		{"upscale.pth", []byte("GGUF"), ""},
		{"checkpoint.pkl", []byte("w"), ""},
		{"config.json", []byte("{}"), ""},
		{"torch.zip", []byte{'P', 'K', 0x03, 0x04}, ""},
	} {
		full := filepath.Join(t.TempDir(), filepath.Base(c.rel))
		write(t, full, c.body)
		if got := ModelFormatOf(full, c.rel); got != c.want {
			t.Fatalf("ModelFormatOf(%q) = %q, want %q", c.rel, got, c.want)
		}
	}
	// A directory where a file should be is no regular file and in no format.
	dir := filepath.Join(t.TempDir(), "sha256-"+strings.Repeat("b", 64))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ModelFormatOf(dir, filepath.Base(dir)); got != "" {
		t.Fatalf("a directory: %q", got)
	}
	if !IsPickle(dir, "weights.bin") {
		t.Fatal("a .bin directory is not refused")
	}
	if got := ModelFormatOf("", "member.bin"); got != "" {
		t.Fatalf("a .bin member by name: %q", got)
	}
	if got := ModelFormatOf("", "member.gguf"); got != "gguf" {
		t.Fatalf("a .gguf member by name: %q", got)
	}
}
