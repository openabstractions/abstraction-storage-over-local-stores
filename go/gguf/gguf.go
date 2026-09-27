// Package gguf reads the key-value header a GGUF file carries before its
// tensors: the architecture, the quantisation, the base name and size label
// the converter recorded, and the model this one was tuned from.
//
// The header is the only part read. Tensor data is never opened, nothing is
// hashed, and array values — the tokenizer vocabulary and its merges, which
// are the bulk of a header — are skipped without being held in memory. The
// read stops as soon as every field a descriptor needs has been seen, and in
// any case at Budget bytes.
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Magic is the four bytes every GGUF file starts with.
const Magic = "GGUF"

// Budget bounds a header read. A converter writes the general metadata before
// the tokenizer arrays, so the wanted keys are normally all seen in the first
// few kilobytes; the budget is what stops a malformed or unusual file from
// turning one stat into a full-file read.
const Budget = 8 << 20

// maxString bounds one string value. A header string is a name, a template or
// a chat format, never a tensor.
const maxString = 1 << 20

// ErrNotGGUF says the first four bytes are not the GGUF magic.
var ErrNotGGUF = errors.New("not a gguf file")

// General is what a descriptor is built from: the general.* metadata, and the
// two counts of the file header itself.
type General struct {
	Version      uint32
	Tensors      uint64
	Architecture string
	Name         string
	BaseName     string
	SizeLabel    string
	Finetune     string
	// Quant is the quantisation word for general.file_type, empty for a file
	// type this package has no word for.
	Quant string
	// Base names the model this one was derived from: the first base model's
	// name, or its repository when it has no name.
	Base string
	// Truncated says the read reached Budget before the header ended, so an
	// absent field may still be in the file.
	Truncated bool
}

// wanted is the key set that ends the read once every one of them is seen.
// general.base_model.0.name is left out: most files have no base model, and
// waiting for one would read every header to the end.
var wanted = []string{
	"general.architecture",
	"general.name",
	"general.basename",
	"general.size_label",
	"general.finetune",
	"general.file_type",
}

// Read opens path and reads its header.
func Read(path string) (General, error) {
	f, err := os.Open(path)
	if err != nil {
		return General{}, err
	}
	defer f.Close()
	return ReadFrom(f)
}

// ReadFrom reads a header from r, which is read forward and never seeked.
func ReadFrom(r io.Reader) (General, error) {
	d := &decoder{r: bufio.NewReaderSize(r, 64<<10), left: Budget}
	var magic [4]byte
	if err := d.full(magic[:]); err != nil {
		return General{}, ErrNotGGUF
	}
	if string(magic[:]) != Magic {
		return General{}, ErrNotGGUF
	}
	var g General
	version, err := d.u32()
	if err != nil {
		return General{}, err
	}
	g.Version = version
	if version < 2 || version > 3 {
		return g, fmt.Errorf("gguf version %d is not readable here", version)
	}
	if g.Tensors, err = d.u64(); err != nil {
		return g, err
	}
	count, err := d.u64()
	if err != nil {
		return g, err
	}
	seen := map[string]bool{}
	for i := uint64(0); i < count; i++ {
		key, err := d.str()
		if err != nil {
			if errors.Is(err, errBudget) {
				g.Truncated = true
				return g, nil
			}
			return g, err
		}
		if err := d.value(&g, key); err != nil {
			if errors.Is(err, errBudget) {
				g.Truncated = true
				return g, nil
			}
			return g, err
		}
		if len(key) > 8 && key[:8] == "general." {
			seen[key] = true
		}
		if complete(seen) {
			return g, nil
		}
	}
	return g, nil
}

func complete(seen map[string]bool) bool {
	for _, k := range wanted {
		if !seen[k] {
			return false
		}
	}
	return true
}

// Descriptor kinds of a GGUF value, as the format numbers them.
const (
	tUint8 = iota
	tInt8
	tUint16
	tInt16
	tUint32
	tInt32
	tFloat32
	tBool
	tString
	tArray
	tUint64
	tInt64
	tFloat64
)

var fixedWidth = map[uint32]int64{
	tUint8: 1, tInt8: 1, tBool: 1,
	tUint16: 2, tInt16: 2,
	tUint32: 4, tInt32: 4, tFloat32: 4,
	tUint64: 8, tInt64: 8, tFloat64: 8,
}

// value reads one value and keeps it when its key is one a descriptor uses.
func (d *decoder) value(g *General, key string) error {
	kind, err := d.u32()
	if err != nil {
		return err
	}
	switch kind {
	case tString:
		s, err := d.str()
		if err != nil {
			return err
		}
		switch key {
		case "general.architecture":
			g.Architecture = s
		case "general.name":
			g.Name = s
		case "general.basename":
			g.BaseName = s
		case "general.size_label":
			g.SizeLabel = s
		case "general.finetune":
			g.Finetune = s
		case "general.base_model.0.name":
			g.Base = s
		case "general.base_model.0.repo_url":
			if g.Base == "" {
				g.Base = repoOf(s)
			}
		}
		return nil
	case tArray:
		return d.skipArray()
	}
	width, ok := fixedWidth[kind]
	if !ok {
		return fmt.Errorf("value type %d at key %q", kind, key)
	}
	b := make([]byte, width)
	if err := d.full(b); err != nil {
		return err
	}
	if key == "general.file_type" && kind == tUint32 {
		g.Quant = QuantOf(binary.LittleEndian.Uint32(b))
	}
	return nil
}

// skipArray reads past an array value. A fixed-width element array is one
// discard; a string array is a length and a discard per element; a nested
// array is read the same way, recursively.
func (d *decoder) skipArray() error {
	kind, err := d.u32()
	if err != nil {
		return err
	}
	n, err := d.u64()
	if err != nil {
		return err
	}
	if width, ok := fixedWidth[kind]; ok {
		if n > math.MaxInt64/uint64(width) {
			return errors.New("array length overflows")
		}
		return d.skip(int64(n) * width)
	}
	for i := uint64(0); i < n; i++ {
		switch kind {
		case tString:
			size, err := d.u64()
			if err != nil {
				return err
			}
			if err := d.skip(int64(size)); err != nil {
				return err
			}
		case tArray:
			if err := d.skipArray(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("array element type %d", kind)
		}
	}
	return nil
}

// repoOf is the org/repo of a Hugging Face repository URL, or the URL when it
// is not one.
func repoOf(u string) string {
	s := strings.TrimSuffix(strings.TrimSpace(u), "/")
	for _, prefix := range []string{"https://huggingface.co/", "http://huggingface.co/", "https://hf.co/"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			return rest
		}
	}
	return s
}

// quants is ggml's file type numbering. A number with no entry leaves the
// quantisation unnamed rather than guessed.
var quants = map[uint32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 4: "Q4_1_SOME_F16",
	7: "Q8_0", 8: "Q5_0", 9: "Q5_1", 10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M",
	13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S", 17: "Q5_K_M",
	18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S", 22: "IQ3_XS",
	23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M",
	28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M", 32: "BF16",
	36: "TQ1_0", 37: "TQ2_0", 38: "MXFP4_MOE",
}

// QuantOf is the quantisation word for a general.file_type number.
func QuantOf(fileType uint32) string { return quants[fileType] }

var errBudget = errors.New("header budget reached")

type decoder struct {
	r    *bufio.Reader
	left int64
}

func (d *decoder) take(n int64) error {
	if n < 0 {
		return errors.New("negative length")
	}
	if n > d.left {
		d.left = 0
		return errBudget
	}
	d.left -= n
	return nil
}

func (d *decoder) full(b []byte) error {
	if err := d.take(int64(len(b))); err != nil {
		return err
	}
	_, err := io.ReadFull(d.r, b)
	return err
}

func (d *decoder) skip(n int64) error {
	if err := d.take(n); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, d.r, n)
	return err
}

func (d *decoder) u32() (uint32, error) {
	var b [4]byte
	if err := d.full(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func (d *decoder) u64() (uint64, error) {
	var b [8]byte
	if err := d.full(b[:]); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(b[:])
	if v > math.MaxInt64 {
		return 0, errors.New("length overflows")
	}
	return v, nil
}

func (d *decoder) str() (string, error) {
	n, err := d.u64()
	if err != nil {
		return "", err
	}
	if n > maxString {
		if err := d.skip(int64(n)); err != nil {
			return "", err
		}
		return "", nil
	}
	b := make([]byte, n)
	if err := d.full(b); err != nil {
		return "", err
	}
	return string(b), nil
}
