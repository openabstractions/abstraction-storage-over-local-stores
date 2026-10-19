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

// FactsBudget bounds an engine-facts read. The chat template follows the
// tokenizer arrays, and a 250k-token vocabulary with its merges is some tens
// of MiB of header, so this read walks the whole key-value section; the
// budget stops a malformed file from turning it into a full-file read.
const FactsBudget = 64 << 20

// maxTemplate bounds the chat template kept. A template longer than this is
// left unread, which a caller treats as an unknown template.
const maxTemplate = maxString

// Facts is what an engine reads from a GGUF file's header beyond its
// descriptor: the chat template a text model formats a conversation with,
// and the encoders a projector (mmproj) carries.
type Facts struct {
	// Type is general.type, Architecture general.architecture.
	Type, Architecture string
	// ChatTemplate is tokenizer.chat_template, empty when the file states
	// none or it is longer than this reader keeps.
	ChatTemplate string
	// EmbeddingLength is <architecture>.embedding_length, the width of the
	// text model's hidden state; zero when unstated.
	EmbeddingLength uint64
	// ProjectionDim is clip.vision.projection_dim, the width a projector
	// hands the text model; zero when unstated.
	ProjectionDim uint64
	// HasVisionEncoder and HasAudioEncoder are clip.has_vision_encoder and
	// clip.has_audio_encoder; nil when the file states neither value.
	HasVisionEncoder, HasAudioEncoder *bool
	// Truncated says the read reached FactsBudget before the key-value
	// section ended, so an absent field may still be in the file.
	Truncated bool
}

// ReadFacts opens path and reads its engine facts.
func ReadFacts(path string) (Facts, error) {
	f, err := os.Open(path)
	if err != nil {
		return Facts{}, err
	}
	defer f.Close()
	return ReadFactsFrom(f)
}

// ReadFactsFrom reads engine facts from r, read forward to the end of the
// key-value section at most. Array values are skipped without being held.
func ReadFactsFrom(r io.Reader) (Facts, error) {
	d := &decoder{r: bufio.NewReaderSize(r, 64<<10), left: FactsBudget}
	var magic [4]byte
	if err := d.full(magic[:]); err != nil || string(magic[:]) != Magic {
		return Facts{}, ErrNotGGUF
	}
	version, err := d.u32()
	if err != nil {
		return Facts{}, err
	}
	if version < 2 || version > 3 {
		return Facts{}, fmt.Errorf("gguf version %d is not readable here", version)
	}
	if _, err := d.u64(); err != nil {
		return Facts{}, err
	}
	count, err := d.u64()
	if err != nil {
		return Facts{}, err
	}
	var f Facts
	widths := map[string]uint64{}
	finish := func() Facts {
		if f.Architecture != "" {
			f.EmbeddingLength = widths[f.Architecture+".embedding_length"]
		}
		return f
	}
	for i := uint64(0); i < count; i++ {
		key, err := d.str()
		if err == nil {
			err = d.fact(&f, widths, key)
		}
		if errors.Is(err, errBudget) {
			f.Truncated = true
			return finish(), nil
		}
		if err != nil {
			return finish(), err
		}
	}
	return finish(), nil
}

// fact reads one value and keeps it when its key is an engine fact.
func (d *decoder) fact(f *Facts, widths map[string]uint64, key string) error {
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
		case "general.type":
			f.Type = s
		case "general.architecture":
			f.Architecture = s
		case "tokenizer.chat_template":
			if len(s) <= maxTemplate {
				f.ChatTemplate = s
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
	switch {
	case kind == tBool && key == "clip.has_vision_encoder":
		v := b[0] != 0
		f.HasVisionEncoder = &v
	case kind == tBool && key == "clip.has_audio_encoder":
		v := b[0] != 0
		f.HasAudioEncoder = &v
	case key == "clip.vision.projection_dim":
		f.ProjectionDim = unsigned(kind, b)
	case strings.HasSuffix(key, ".embedding_length") && !strings.HasPrefix(key, "general.") && !strings.HasPrefix(key, "clip."):
		if n := unsigned(kind, b); n > 0 {
			widths[key] = n
		}
	}
	return nil
}

// unsigned is a positive integer value of kind, zero for any other.
func unsigned(kind uint32, b []byte) uint64 {
	var n uint64
	switch kind {
	case tUint32:
		n = uint64(binary.LittleEndian.Uint32(b))
	case tInt32:
		if v := int32(binary.LittleEndian.Uint32(b)); v > 0 {
			n = uint64(v)
		}
	case tUint64:
		n = binary.LittleEndian.Uint64(b)
	case tInt64:
		if v := int64(binary.LittleEndian.Uint64(b)); v > 0 {
			n = uint64(v)
		}
	}
	if n > math.MaxInt32 {
		return 0
	}
	return n
}
