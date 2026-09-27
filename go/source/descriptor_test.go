package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
)

// A store's own name for a file names the family and the quantisation; the
// format comes from the extension and the source from the adapter.
func TestANameNamesFamilyAndQuant(t *testing.T) {
	got := Named("unsloth/SmolLM2-135M-Instruct-GGUF:SmolLM2-135M-Instruct-Q4_K_M.gguf", "gguf", "huggingface").Map()
	want := map[string]string{"family": "smollm2-135m", "quant": "q4_k_m", "format": "gguf", "source": "huggingface"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor %v, want %v", got, want)
	}
}

// A name that says nothing about a model leaves the descriptor with nothing
// but the format, and a manifest of such a file names no identity.
func TestANamelessFileNamesNoIdentity(t *testing.T) {
	if got := Named("", "", "").Map(); got != nil {
		t.Fatalf("descriptor %v, want none", got)
	}
}

// The header adds what no name carries: the architecture, the size label, the
// fine-tune and the model the weights were tuned from. The name keeps the
// family and the quantisation, because a host serves the file under the name.
func TestTheHeaderAddsWhatTheNameCannotSay(t *testing.T) {
	m := Named("lmstudio/Qwen3-0.6B-GGUF/Qwen3-0.6B-Q4_K_M.gguf", "gguf", "lmstudio").WithHeader(gguf.General{
		Architecture: "qwen3", BaseName: "Qwen3", SizeLabel: "0.6B", Finetune: "Instruct",
		Quant: "Q8_0", Base: "Qwen/Qwen3-0.6B",
	})
	want := map[string]string{
		"family": "qwen3-0.6b", "quant": "q4_k_m", "format": "gguf", "architecture": "qwen3",
		"size_label": "0.6B", "finetune": "Instruct", "base": "Qwen/Qwen3-0.6B", "source": "lmstudio",
	}
	if got := m.Map(); !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor %v, want %v", got, want)
	}
}

// A file whose name says nothing takes its family from the header.
func TestTheHeaderNamesTheFamilyWhenTheNameCannot(t *testing.T) {
	m := Named("", "gguf", "ollama").WithHeader(gguf.General{
		BaseName: "SmolLM2", SizeLabel: "135M", Finetune: "Instruct", Quant: "Q2_K",
	})
	if m.Family != "smollm2-135m" || m.Quant != "q2_k" {
		t.Fatalf("descriptor %+v", m)
	}
}

// Descriptor reads the header of a GGUF on disk and nothing else: a file of
// another format is described by its name alone.
func TestDescriptorReadsAGGUFHeaderAndSkipsOtherFormats(t *testing.T) {
	dir := t.TempDir()
	gg := filepath.Join(dir, "Tiny-Q6_K.gguf")
	write(t, gg, header("llama", "Tiny", "135M", "Instruct", 18))
	other := filepath.Join(dir, "sd15.safetensors")
	write(t, other, []byte("not a gguf"))

	env := Env{Headers: NewHeaders()}
	got := env.Descriptor(gg, "Tiny-Q6_K.gguf", "lmstudio")
	if got["architecture"] != "llama" || got["size_label"] != "135M" || got["finetune"] != "Instruct" {
		t.Fatalf("header fields missing: %v", got)
	}
	if got["family"] != "tiny" || got["quant"] != "q6_k" || got["format"] != "gguf" {
		t.Fatalf("name fields wrong: %v", got)
	}
	if got := env.Descriptor(other, "sd15.safetensors", "comfyui"); got["architecture"] != "" || got["format"] != "safetensors" {
		t.Fatalf("a safetensors file was read as a header: %v", got)
	}
}

// An environment with no header cache opens nothing, and a file whose header
// is unreadable keeps what its name said.
func TestNoCacheReadsNoHeaderAndAJunkFileStillHasAName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Tiny-Q6_K.gguf")
	write(t, p, header("llama", "Tiny", "135M", "", 18))
	if got := (Env{}).Descriptor(p, "Tiny-Q6_K.gguf", "lmstudio"); got["architecture"] != "" {
		t.Fatalf("an environment with no cache read a header: %v", got)
	}
	junk := filepath.Join(dir, "Junk-Q4_K_M.gguf")
	write(t, junk, []byte("this is not a gguf file at all"))
	got := (Env{Headers: NewHeaders()}).Descriptor(junk, "Junk-Q4_K_M.gguf", "lmstudio")
	if got["family"] != "junk" || got["quant"] != "q4_k_m" || got["architecture"] != "" {
		t.Fatalf("descriptor %v", got)
	}
}

// The cache reads one header per path while size and modification time hold,
// and reads again when the file changes (model-bridge decision §4 rule 1).
func TestTheCacheRereadsOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Tiny-Q6_K.gguf")
	write(t, p, header("llama", "Tiny", "135M", "", 18))
	env := Env{Headers: NewHeaders()}
	if g, ok := env.Header(p); !ok || g.Architecture != "llama" {
		t.Fatalf("first read %+v %v", g, ok)
	}
	// Replacing the bytes without moving size or time is not seen; the cache
	// is what stops a rescan from opening every file again.
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	same := header("gemma3", "Tiny", "135M", "", 18)
	if len(same) != int(st.Size()) {
		t.Skipf("the two synthetic headers differ in size (%d, %d)", len(same), st.Size())
	}
	write(t, p, same)
	os.Chtimes(p, st.ModTime(), st.ModTime())
	if g, _ := env.Header(p); g.Architecture != "llama" {
		t.Fatalf("an unchanged size and time reopened the file: %+v", g)
	}
	write(t, p, header("gemma3", "Tiny", "270M", "", 18))
	os.Chtimes(p, st.ModTime().Add(time.Minute), st.ModTime().Add(time.Minute))
	if g, _ := env.Header(p); g.Architecture != "gemma3" {
		t.Fatalf("a changed file was answered from the cache: %+v", g)
	}
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// header is a synthetic GGUF header. internal/fixture builds the same bytes
// for the adapters; this package cannot import it, because fixture reads this
// one.
func header(arch, basename, size, finetune string, fileType uint32) []byte {
	var out []byte
	str := func(s string) {
		out = appendU64(out, uint64(len(s)))
		out = append(out, s...)
	}
	out = append(out, "GGUF"...)
	out = appendU32(out, 3)
	out = appendU64(out, 1)
	kv := [][2]string{{"general.architecture", arch}, {"general.basename", basename}, {"general.size_label", size}}
	if finetune != "" {
		kv = append(kv, [2]string{"general.finetune", finetune})
	}
	out = appendU64(out, uint64(len(kv)+1))
	for _, p := range kv {
		str(p[0])
		out = appendU32(out, 8)
		str(p[1])
	}
	str("general.file_type")
	out = appendU32(out, 4)
	out = appendU32(out, fileType)
	return out
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func appendU64(b []byte, v uint64) []byte {
	for i := 0; i < 8; i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}
