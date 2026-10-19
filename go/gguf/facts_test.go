package gguf_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

// The chat template follows the tokenizer arrays, so a facts read walks past
// the vocabulary to it; the architecture's own embedding width is kept.
func TestFactsReadTheTemplateAfterTheVocabulary(t *testing.T) {
	tokens := make([]string, 4096)
	for i := range tokens {
		tokens[i] = strings.Repeat("t", 64)
	}
	template := "{%- if tools %}<tools>{{ tools }}</tools>{%- endif %}<think>"
	b := fixture.GGUF(1, []fixture.GGUFKV{
		{Key: "general.architecture", Value: fixture.GGUFString("qwen3")},
		{Key: "general.type", Value: fixture.GGUFString("model")},
		{Key: "clip.embedding_length", Value: fixture.GGUFUint32(7)},
		{Key: "qwen3.embedding_length", Value: fixture.GGUFUint32(2048)},
		{Key: "tokenizer.ggml.tokens", Value: fixture.GGUFStrings(tokens...)},
		{Key: "tokenizer.chat_template", Value: fixture.GGUFString(template)},
	}, 1<<16)
	f, err := gguf.ReadFactsFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if f.ChatTemplate != template || f.Type != "model" || f.Architecture != "qwen3" || f.EmbeddingLength != 2048 || f.Truncated {
		t.Fatalf("facts %+v", f)
	}
	if f.HasAudioEncoder != nil || f.HasVisionEncoder != nil || f.ProjectionDim != 0 {
		t.Fatalf("a text model stated encoder facts: %+v", f)
	}
}

// A projector states its encoders and the width it hands the text model.
func TestFactsReadAProjectorsEncoders(t *testing.T) {
	b := fixture.GGUF(1, []fixture.GGUFKV{
		{Key: "general.architecture", Value: fixture.GGUFString("clip")},
		{Key: "general.type", Value: fixture.GGUFString("mmproj")},
		{Key: "clip.has_vision_encoder", Value: fixture.GGUFBool(true)},
		{Key: "clip.has_audio_encoder", Value: fixture.GGUFBool(true)},
		{Key: "clip.vision.projection_dim", Value: fixture.GGUFUint32(2048)},
	}, 0)
	f, err := gguf.ReadFactsFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != "mmproj" || f.HasVisionEncoder == nil || !*f.HasVisionEncoder || f.HasAudioEncoder == nil || !*f.HasAudioEncoder || f.ProjectionDim != 2048 {
		t.Fatalf("facts %+v", f)
	}
}

// A header of arrays nested a hundred thousand deep is refused as malformed;
// the read neither recurses through it nor takes the process down.
func TestFactsRefuseDeeplyNestedArrays(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(1))
	binary.Write(&b, binary.LittleEndian, uint64(4))
	b.WriteString("deep")
	binary.Write(&b, binary.LittleEndian, uint32(9)) // array
	for range 100000 {
		binary.Write(&b, binary.LittleEndian, uint32(9)) // of arrays
		binary.Write(&b, binary.LittleEndian, uint64(1))
	}
	if _, err := gguf.ReadFactsFrom(bytes.NewReader(b.Bytes())); err == nil || !strings.Contains(err.Error(), "nest") {
		t.Fatalf("nested arrays read: %v", err)
	}
	if _, err := gguf.ReadFrom(bytes.NewReader(b.Bytes())); err == nil {
		t.Fatal("nested arrays read as a descriptor")
	}
}
