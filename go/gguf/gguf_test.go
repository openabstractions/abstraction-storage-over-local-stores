package gguf_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

// The header a converter writes names the architecture, the base name, the
// size label, the fine-tune, the quantisation and the model it came from.
func TestHeaderNamesWhatTheDescriptorCarries(t *testing.T) {
	b := fixture.GGUFModel(map[string]string{
		"general.architecture":          "qwen3",
		"general.type":                  "model",
		"general.name":                  "Qwen3 0.6B Instruct",
		"general.basename":              "Qwen3",
		"general.finetune":              "Instruct",
		"general.size_label":            "0.6B",
		"general.license":               "apache-2.0",
		"general.base_model.count":      "1",
		"general.base_model.0.name":     "Qwen3 0.6B",
		"general.base_model.0.repo_url": "https://huggingface.co/Qwen/Qwen3-0.6B",
		"general.file_type":             "15",
		"general.quantization_version":  "2",
		"qwen3.attention.head_count":    "16",
		"qwen3.context_length":          "40960",
	}, 64, 4096)

	g, err := gguf.ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if g.Version != 3 || g.Tensors != 1 {
		t.Fatalf("version %d, tensors %d", g.Version, g.Tensors)
	}
	if g.Architecture != "qwen3" || g.BaseName != "Qwen3" || g.SizeLabel != "0.6B" || g.Finetune != "Instruct" {
		t.Fatalf("general %+v", g)
	}
	if g.Quant != "Q4_K_M" {
		t.Fatalf("file_type 15 read as %q, want Q4_K_M", g.Quant)
	}
	if g.Base != "Qwen3 0.6B" {
		t.Fatalf("base %q", g.Base)
	}
	if g.Truncated {
		t.Fatal("a header this size was truncated")
	}
}

// A repository URL names the base when the header carries no base model name.
func TestBaseFallsBackToTheRepository(t *testing.T) {
	b := fixture.GGUFModel(map[string]string{
		"general.architecture":          "llama",
		"general.base_model.0.repo_url": "https://huggingface.co/meta-llama/Llama-3.2-1B/",
	}, 0, 0)
	g, err := gguf.ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if g.Base != "meta-llama/Llama-3.2-1B" {
		t.Fatalf("base %q", g.Base)
	}
}

// The read stops at the general metadata: a vocabulary of a hundred thousand
// tokens and a gigabyte of tensors are never touched, which is what lets an
// inventory read every GGUF on a machine.
func TestTheReadStopsBeforeTheVocabulary(t *testing.T) {
	kv := []fixture.GGUFKV{
		{Key: "general.architecture", Value: fixture.GGUFString("llama")},
		{Key: "general.name", Value: fixture.GGUFString("Tiny")},
		{Key: "general.basename", Value: fixture.GGUFString("Tiny")},
		{Key: "general.finetune", Value: fixture.GGUFString("Instruct")},
		{Key: "general.size_label", Value: fixture.GGUFString("1B")},
		{Key: "general.file_type", Value: fixture.GGUFUint32(18)},
	}
	head := fixture.GGUF(1, kv, 0)
	tokens := make([]string, 4096)
	for i := range tokens {
		tokens[i] = strings.Repeat("t", 64)
	}
	tail := fixture.GGUF(1, append(kv, fixture.GGUFKV{Key: "tokenizer.ggml.tokens", Value: fixture.GGUFStrings(tokens...)}), 0)

	counted := &counter{Reader: bytes.NewReader(tail)}
	g, err := gguf.ReadFrom(counted)
	if err != nil {
		t.Fatal(err)
	}
	if g.Quant != "Q6_K" || g.SizeLabel != "1B" {
		t.Fatalf("general %+v", g)
	}
	if counted.n > int64(len(head))+64<<10 {
		t.Fatalf("read %d bytes of a %d-byte header for a %d-byte file", counted.n, len(head), len(tail))
	}
}

// A vocabulary written before the general metadata is skipped, not held: the
// read walks past the array and still answers.
func TestAVocabularyBeforeTheMetadataIsSkipped(t *testing.T) {
	tokens := make([]string, 8192)
	for i := range tokens {
		tokens[i] = strings.Repeat("v", 32)
	}
	b := fixture.GGUF(1, []fixture.GGUFKV{
		{Key: "tokenizer.ggml.tokens", Value: fixture.GGUFStrings(tokens...)},
		{Key: "general.architecture", Value: fixture.GGUFString("gemma3")},
		{Key: "general.file_type", Value: fixture.GGUFUint32(1)},
	}, 0)
	g, err := gguf.ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if g.Architecture != "gemma3" || g.Quant != "F16" {
		t.Fatalf("general %+v", g)
	}
}

// A header longer than the budget answers with what it read and says so,
// rather than reading a file to its end.
func TestABudgetedReadSaysItWasTruncated(t *testing.T) {
	tokens := make([]string, 1)
	tokens[0] = strings.Repeat("x", 4096)
	var kv []fixture.GGUFKV
	kv = append(kv, fixture.GGUFKV{Key: "general.architecture", Value: fixture.GGUFString("llama")})
	for i := 0; i < 4096; i++ {
		kv = append(kv, fixture.GGUFKV{Key: "filler.ggml." + strings.Repeat("k", 8), Value: fixture.GGUFStrings(tokens...)})
	}
	kv = append(kv, fixture.GGUFKV{Key: "general.size_label", Value: fixture.GGUFString("70B")})
	b := fixture.GGUF(1, kv, 0)
	if len(b) <= gguf.Budget {
		t.Skipf("the synthetic header is %d bytes, inside the %d budget", len(b), gguf.Budget)
	}
	g, err := gguf.ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if !g.Truncated {
		t.Fatal("a header past the budget did not report truncation")
	}
	if g.Architecture != "llama" || g.SizeLabel != "" {
		t.Fatalf("general %+v", g)
	}
}

// Anything that is not a GGUF file is refused by its first four bytes, which
// is what a tree adapter meeting a .bin of some other format relies on.
func TestSomethingElseIsRefusedByItsMagic(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("GG"), []byte("PK\x03\x04payload"), bytes.Repeat([]byte{0}, 64)} {
		if _, err := gguf.ReadFrom(bytes.NewReader(b)); !errors.Is(err, gguf.ErrNotGGUF) {
			t.Fatalf("%q read as %v", b, err)
		}
	}
}

// Read opens a file by path.
func TestReadOpensAPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Tiny-Q8_0.gguf")
	if err := os.WriteFile(p, fixture.GGUFModel(map[string]string{
		"general.architecture": "llama", "general.basename": "Tiny", "general.size_label": "135M",
		"general.file_type": "7",
	}, 0, 128), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := gguf.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if g.Quant != "Q8_0" || g.BaseName != "Tiny" {
		t.Fatalf("general %+v", g)
	}
	if _, err := gguf.Read(filepath.Join(dir, "absent.gguf")); err == nil {
		t.Fatal("a missing file read as a header")
	}
}

type counter struct {
	*bytes.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}
