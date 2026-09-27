package fixture

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"
)

// GGUFValue is one header value of a synthetic GGUF file. Str, U32 and Arr
// cover what a descriptor reads and what it has to skip past.
type GGUFValue struct {
	Str string
	U32 *uint32
	// Arr is a string array, the shape of the tokenizer vocabulary a header
	// read has to walk past without holding it.
	Arr []string
}

// GGUFString is a string value.
func GGUFString(s string) GGUFValue { return GGUFValue{Str: s} }

// GGUFUint32 is a uint32 value.
func GGUFUint32(v uint32) GGUFValue { u := v; return GGUFValue{U32: &u} }

// GGUFStrings is a string-array value.
func GGUFStrings(s ...string) GGUFValue { return GGUFValue{Arr: s} }

// GGUFKV is one key and its value, in the order written.
type GGUFKV struct {
	Key   string
	Value GGUFValue
}

// GGUF builds the bytes of a synthetic GGUF version 3 file: the magic, the
// version, a tensor count, the key-value pairs in the order given, and the
// trailing bytes that stand in for tensor data a header read must never open.
func GGUF(tensors uint64, kv []GGUFKV, trailing int) []byte {
	out := append([]byte(nil), "GGUF"...)
	out = binary.LittleEndian.AppendUint32(out, 3)
	out = binary.LittleEndian.AppendUint64(out, tensors)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(kv)))
	for _, p := range kv {
		out = ggufString(out, p.Key)
		switch {
		case p.Value.U32 != nil:
			out = binary.LittleEndian.AppendUint32(out, 4) // uint32
			out = binary.LittleEndian.AppendUint32(out, *p.Value.U32)
		case p.Value.Arr != nil:
			out = binary.LittleEndian.AppendUint32(out, 9) // array
			out = binary.LittleEndian.AppendUint32(out, 8) // of string
			out = binary.LittleEndian.AppendUint64(out, uint64(len(p.Value.Arr)))
			for _, s := range p.Value.Arr {
				out = ggufString(out, s)
			}
		default:
			out = binary.LittleEndian.AppendUint32(out, 8) // string
			out = ggufString(out, p.Value.Str)
		}
	}
	return append(out, make([]byte, trailing)...)
}

// GGUFModel is the header a converter writes for one small model: the general
// metadata first, then an architecture parameter, then a tokenizer array.
func GGUFModel(d map[string]string, vocab int, trailing int) []byte {
	order := []string{"general.architecture", "general.type", "general.name", "general.basename",
		"general.finetune", "general.size_label", "general.license", "general.base_model.count",
		"general.base_model.0.name", "general.base_model.0.repo_url", "general.file_type",
		"general.quantization_version"}
	var kv []GGUFKV
	written := map[string]bool{}
	add := func(k string) {
		v, ok := d[k]
		if !ok || written[k] {
			return
		}
		written[k] = true
		if k == "general.file_type" || k == "general.quantization_version" || k == "general.base_model.count" {
			kv = append(kv, GGUFKV{k, GGUFUint32(uint32(atoi(v)))})
			return
		}
		kv = append(kv, GGUFKV{k, GGUFString(v)})
	}
	for _, k := range order {
		add(k)
	}
	var rest []string
	for k := range d {
		if !written[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	if vocab > 0 {
		tokens := make([]string, vocab)
		for i := range tokens {
			tokens[i] = "tok" + itoa(i)
		}
		kv = append(kv, GGUFKV{"tokenizer.ggml.tokens", GGUFStrings(tokens...)})
	}
	return GGUF(1, kv, trailing)
}

// WriteGGUF replaces the file at path with a synthetic GGUF of exactly size
// bytes carrying d as its general metadata, so a corpus file keeps the size
// the rest of a test asserts while gaining a header a descriptor can read.
func WriteGGUF(t testing.TB, path string, size int64, d map[string]string) {
	t.Helper()
	b := GGUFModel(d, 0, 0)
	if int64(len(b)) > size {
		t.Fatalf("a %d-byte header does not fit in %d bytes", len(b), size)
	}
	if err := os.WriteFile(path, append(b, make([]byte, size-int64(len(b)))...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ggufString(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint64(out, uint64(len(s)))
	return append(out, s...)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
