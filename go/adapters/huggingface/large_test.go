package huggingface_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

// A store holding a model the size of the shipped one (22.9 GB) is listed in
// the time a header read takes: the listing reads the GGUF header and no
// weights, and hashes nothing (LS-13 hashes only on a caller's request). The
// file is sparse, so the test writes a header and no 22.9 GB.
func TestALargeWeightsFileIsListedWithoutReadingIt(t *testing.T) {
	home := fixture.Home(t, "hf")
	o := fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	q4 := fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	weights := filepath.Join(home, ".cache", "huggingface", "hub", filepath.FromSlash(q4.Manifest.Entries[0].Locator))
	st, err := os.Stat(weights)
	if err != nil {
		t.Fatal(err)
	}
	fixture.WriteGGUF(t, weights, st.Size(), map[string]string{
		"general.architecture":    "qwen3moe",
		"general.basename":        "Tiny",
		"general.size_label":      "35B",
		"general.file_type":       "15",
		"qwen3moe.context_length": "262144",
	})
	const large = 22853663008
	makeSparse(t, weights, large)

	start := time.Now()
	o = fixture.Observe(t, huggingface.New(), fixture.Env(home, nil), "huggingface")
	took := time.Since(start)
	q4 = fixture.MustManifest(t, o, "hf", "acme/Tiny-GGUF:Tiny-Q4_K_M.gguf")
	if took > 5*time.Second {
		t.Fatalf("listing a %d-byte file took %v", int64(large), took)
	}
	if q4.Manifest.Entries[0].Size != large {
		t.Fatalf("listed size %d, want %d", q4.Manifest.Entries[0].Size, int64(large))
	}
	if got := q4.Manifest.Descriptor["context_length"]; got != "262144" {
		t.Fatalf("context_length %q, want the header's 262144: %v", got, q4.Manifest.Descriptor)
	}
}
