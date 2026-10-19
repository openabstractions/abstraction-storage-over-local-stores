package asusai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

const asus = "ProgramData/ASUS"

// windowsNames skips a test whose fixture names differ from the pins only
// in case: the pins match as the file system compares names
// (identity.PathNameKey), ignoring case on Windows, the one platform with
// ASUS's folders (Discover reads nothing elsewhere).
func windowsNames(t *testing.T) {
	t.Helper()
	if !identity.SamePathName("ASUS", "asus") {
		t.Skip("ASUS's model folders exist only on Windows, where names match ignoring case")
	}
}

// Fixture files: small stand-ins at ASUS's own paths, relative to
// %ProgramData%\ASUS. The pins below are computed from these bytes, as the
// real pins are the public releases'.
var (
	transformerRel = "AICreator/flux_2/checkpoints/FLUX.2-klein-4B/transformer/diffusion_pytorch_model.safetensors"
	turboRel       = "AICreator/hummingbird/vae_decoder/wan22_v1_tiling_16_12/diffusion_pytorch_model.safetensors"
	resizedRel     = "AICreator/hummingbird/vae_decoder/wan22_v3_tiling_16_12/diffusion_pytorch_model.safetensors"
	i2vRel         = "AICreator/hummingbird/HM_2.2_5B/transformer_20251218/diffusion_pytorch_model_i2v.safetensors"
	t2vRel         = "AICreator/hummingbird/HM_2.2_5B/transformer_20251218/diffusion_pytorch_model_t2v.safetensors"
	nf4Rel         = "AICreator/flux_2/checkpoints/FLUX.2-klein-4B/text_encoder_4bit/model.safetensors"
	vaePthRel      = "AICreator/hummingbird/HM_2.2_5B/Wan2.2_VAE.pth"
	blipRel        = "AICreator/clip/blip-image-captioning-large/pytorch_model.bin"
	unknownRel     = "AICreator/flux_2/prompt_config.json"
	embRel         = "LLMServer/models/emb_2_3"
	llmRel         = "LLMServer/models/llm_2_16"
	batRel         = "LLMServer/models/uninstall-emb.bat"
	whisperRel     = "WhisperRecorder/models/ggml-medium.bin"

	files = map[string]string{
		transformerRel: "public FLUX.2 klein transformer bytes",
		turboRel:       "public TurboVAED decoder bytes",
		resizedRel:     "a TurboVAED path with another size",
		i2vRel:         "bytes that are not the public i2v transformer",
		t2vRel:         "ASUS-made t2v transformer, same size as i2v!",
		nf4Rel:         "ASUS NF4 text encoder",
		vaePthRel:      "PK\x03\x04 public Wan VAE pickle",
		blipRel:        "PK\x03\x04 public BLIP pickle",
		unknownRel:     `{"templates": []}`,
		embRel:         "GGUF public bge-m3 Q4_K_M",
		llmRel:         "GGUF ASUS Llama 3.1 8B fine-tune",
		batRel:         "@echo off",
		whisperRel:     "lmgg public whisper medium",
	}
)

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func size(rel string) int64 { return int64(len(files[rel])) }

// within is rel without its folder: the path the store's walk reports.
func within(rel string) string {
	for _, f := range folders {
		if strings.HasPrefix(rel, f.sub+"/") {
			return strings.TrimPrefix(rel, f.sub+"/")
		}
	}
	return rel
}

// testPins pin the transformer, TurboVAED, bge-m3 and whisper by their
// fixture bytes, the i2v file by bytes it does not hold (a substituted
// file), and the pickle by its own bytes, to show a pickle is never listed
// even when pinned. The LLM fine-tune is pinned to the wrong store, to show
// a pin applies to its own folder only.
func testPins() []pin {
	return []pin{
		{store: "asus-aicreator", pattern: "flux_2/checkpoints/*/transformer/diffusion_pytorch_model.safetensors", size: size(transformerRel),
			sha256: digestOf(files[transformerRel]), format: "safetensors", model: "FLUX.2-klein-4B", repository: "black-forest-labs/FLUX.2-klein-4B",
			revision: "e7b7dc27f91deacad38e78976d1f2b499d76a294", file: "transformer/diffusion_pytorch_model.safetensors", license: "Apache-2.0", role: source.RoleWeights},
		{store: "asus-aicreator", pattern: "hummingbird/vae_decoder/*/diffusion_pytorch_model.safetensors", size: size(turboRel),
			sha256: digestOf(files[turboRel]), format: "safetensors", model: "HummingbirdXT", repository: "amd/HummingbirdXT", revision: "4d3bd2e3a8c96a189ed158ab74b575ec6a424d2c",
			file: "vae/wan22_v1_tiling_16_12/diffusion_pytorch_model.safetensors", license: "Apache-2.0", usePolicy: hummingbirdPolicy, role: source.RoleVAE},
		{store: "asus-aicreator", pattern: "hummingbird/*/transformer_*/diffusion_pytorch_model_i2v.safetensors", size: size(i2vRel),
			sha256: digestOf("the public i2v transformer, which this fixture does not hold"), format: "safetensors", model: "HummingbirdXT", repository: "amd/HummingbirdXT",
			revision: "4d3bd2e3a8c96a189ed158ab74b575ec6a424d2c", file: "hm_2.2_5B/diffusion_pytorch_model.safetensors", license: "Apache-2.0", role: source.RoleWeights},
		{store: "asus-aicreator", pattern: "hummingbird/*/wan2.2_vae.pth", size: size(vaePthRel),
			sha256: digestOf(files[vaePthRel]), format: "safetensors", model: "Wan2.2-TI2V-5B", repository: "Wan-AI/Wan2.2-TI2V-5B", revision: "921dbaf3f1674a56f47e83fb80a34bac8a8f203e",
			file: "Wan2.2_VAE.pth", license: "Apache-2.0", role: source.RoleVAE},
		{store: "asus-llmserver", pattern: "emb_*", size: size(embRel),
			sha256: digestOf(files[embRel]), format: "gguf", model: "bge-m3-Q4_K_M", repository: "doof-ferb/bge-m3-gguf", revision: "7a0937bc7478e150e8a33f96bb09647c365e9ef1",
			file: "model-q4_k_m.gguf", license: "MIT", role: source.RoleWeights},
		{store: "asus-whisperrecorder", pattern: "llm_*", size: size(llmRel),
			sha256: digestOf(files[llmRel]), format: "gguf", model: "llm", repository: "example/not-this-folder", revision: "0000000000000000000000000000000000000000",
			file: "llm.gguf", license: "MIT", role: source.RoleWeights},
		{store: "asus-whisperrecorder", pattern: "ggml-medium.bin", size: size(whisperRel),
			sha256: digestOf(files[whisperRel]), format: "ggml", model: "whisper-medium", repository: "ggerganov/whisper.cpp", revision: "5359861c739e955e79d9a303bcbc70fb988958b1",
			file: "ggml-medium.bin", license: "MIT", role: source.RoleWeights},
	}
}

// trustAll accepts every fixture file, which this account owns, and
// restores the real rule when the test ends.
func trustAll(t *testing.T) {
	t.Helper()
	trust = func(string) error { return nil }
	t.Cleanup(func() { trust = trustedFile })
}

// machine writes the fixture folders under a fresh home and returns the
// home, an environment naming its ProgramData, and the digest record's file,
// which sits in a private folder outside the home so NoWrite sees the
// stores untouched.
func machine(t *testing.T) (string, source.Env, string) {
	t.Helper()
	trustAll(t)
	home := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(home, filepath.FromSlash(asus), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := fixture.Env(home, map[string]string{"ProgramData": filepath.Join(home, "ProgramData")})
	record := filepath.Join(t.TempDir(), "inventoryd", "digests.json")
	env.Digests = source.NewDigests(record)
	return home, env, record
}

func full(env source.Env, rel string) string {
	return filepath.Join(env.Var("ProgramData"), "ASUS", filepath.FromSlash(rel))
}

func observe(t *testing.T, env source.Env, store string) source.StoreObservation {
	t.Helper()
	return fixture.Observe(t, Adapter{pins: testPins()}, env, store)
}

// settled observes, lets the background hashes the observation queued end,
// and observes again.
func settled(t *testing.T, env source.Env, store string) source.StoreObservation {
	t.Helper()
	o := observe(t, env, store)
	if env.Digests.Wait() {
		o = observe(t, env, store)
	}
	return o
}

// hashed is the paths, relative to %ProgramData%\ASUS, the record holds.
func hashed(t *testing.T, env source.Env, record string) []string {
	t.Helper()
	b, err := os.ReadFile(record)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Files []struct{ Path string } `json:"files"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(env.Var("ProgramData"), "ASUS")
	var out []string
	for _, f := range doc.Files {
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func TestDiscovery(t *testing.T) {
	home := t.TempDir()
	if d := New().Discover(fixture.Env(home, nil)); len(d.Stores) != 0 {
		t.Fatalf("no ProgramData discovered %+v", d.Stores)
	}
	linux := fixture.Env(home, map[string]string{"ProgramData": home})
	linux.GOOS = "linux"
	if d := New().Discover(linux); len(d.Stores) != 0 {
		t.Fatalf("linux discovered %+v", d.Stores)
	}
	pd := filepath.Join(home, "ProgramData")
	d := New().Discover(fixture.Env(home, map[string]string{"ProgramData": pd}))
	for store, sub := range map[string]string{
		"asus-aicreator": "AICreator", "asus-llmserver": "LLMServer/models", "asus-whisperrecorder": "WhisperRecorder/models",
	} {
		l := fixture.Location(t, d, store)
		if l.Present || !fixture.SamePath(l.Locator, filepath.Join(pd, "ASUS", filepath.FromSlash(sub))) || l.Program != "asus" {
			t.Fatalf("location %+v", l)
		}
	}
}

func TestEmptyAndMissing(t *testing.T) {
	for _, present := range []bool{false, true} {
		home := t.TempDir()
		if present {
			for _, f := range folders {
				if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(asus), filepath.FromSlash(f.sub)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		env := fixture.Env(home, map[string]string{"ProgramData": filepath.Join(home, "ProgramData")})
		env.Digests = source.NewDigests("")
		for _, o := range fixture.NoWrite(t, New(), env) {
			if o.Present != present || len(o.Objects) != 0 || len(o.Manifests) != 0 || len(o.Errors) != 0 {
				t.Fatalf("present %v: %+v", present, o)
			}
		}
	}
}

// TestPendingUntilHashed: a listing never waits on a hash. Trusted
// candidates are objects with no digest and no manifest until the
// background hash ends; the next listing names them.
func TestPendingUntilHashed(t *testing.T) {
	windowsNames(t)
	_, env, record := machine(t)
	first := observe(t, env, "asus-aicreator")
	if len(first.Manifests) != 0 {
		t.Fatalf("first listing: %d manifests", len(first.Manifests))
	}
	// The pinned pickle is refused by its name at once, unread.
	fixture.Equal(t, "first errors", fixture.ErrorKinds(first), []string{"other " + within(vaePthRel)})
	fixture.Equal(t, "pending", fixture.Locators(first), []string{within(transformerRel), within(i2vRel), within(turboRel)})
	for _, obj := range first.Objects {
		if obj.Digest != "" || obj.Evidence != content.EvidenceNone {
			t.Fatalf("pending object %+v", obj)
		}
	}
	if !env.Digests.Wait() {
		t.Fatal("no hash ran")
	}
	// fixture.Observe lists every folder the adapter discovers.
	fixture.Equal(t, "hashed", hashed(t, env, record), []string{transformerRel, i2vRel, turboRel, embRel, whisperRel})
	fixture.Equal(t, "listed", fixture.Locators(observe(t, env, "asus-aicreator")), []string{within(transformerRel), within(turboRel)})
}

// TestListsOnlyPinnedPublicFiles: the transformer, TurboVAED, bge-m3 and
// whisper files are listed with their public identity; the ASUS-made files,
// the unknown files, both pickles (one of them pinned), the substituted i2v
// file, a pinned path of another size and a file pinned for another folder
// are not.
func TestListsOnlyPinnedPublicFiles(t *testing.T) {
	windowsNames(t)
	_, env, record := machine(t)
	creator := settled(t, env, "asus-aicreator")
	llm := settled(t, env, "asus-llmserver")
	whisper := settled(t, env, "asus-whisperrecorder")

	fixture.Equal(t, "AICreator listed", fixture.Locators(creator), []string{within(transformerRel), within(turboRel)})
	fixture.Equal(t, "LLMServer listed", fixture.Locators(llm), []string{within(embRel)})
	fixture.Equal(t, "WhisperRecorder listed", fixture.Locators(whisper), []string{within(whisperRel)})
	fixture.Equal(t, "AICreator names", fixture.Names(creator), []string{
		"asus:" + transformerRel,
		"asus:" + turboRel,
		"hf:amd/HummingbirdXT@4d3bd2e3a8c96a189ed158ab74b575ec6a424d2c:vae/wan22_v1_tiling_16_12/diffusion_pytorch_model.safetensors",
		"hf:black-forest-labs/FLUX.2-klein-4B@e7b7dc27f91deacad38e78976d1f2b499d76a294:transformer/diffusion_pytorch_model.safetensors",
	})
	// The pinned pickle is refused by its name before a byte is read; the
	// substituted i2v file by its digest.
	fixture.Equal(t, "AICreator errors", fixture.ErrorKinds(creator), []string{"other " + within(vaePthRel), "other " + within(i2vRel)})
	for _, e := range creator.Errors {
		if !strings.Contains(e.Detail, "not listed") {
			t.Fatalf("detail %q", e.Detail)
		}
	}
	if creator.OtherFiles != 7 || llm.OtherFiles != 2 || whisper.OtherFiles != 0 || len(llm.Errors)+len(whisper.Errors) != 0 {
		t.Fatalf("other files %d %d %d, errors %v %v", creator.OtherFiles, llm.OtherFiles, whisper.OtherFiles, llm.Errors, whisper.Errors)
	}

	m := fixture.MustManifest(t, creator, "asus", transformerRel)
	if m.Manifest.Addressing != content.AddressingContent {
		t.Fatalf("addressing %v", m.Manifest.Addressing)
	}
	e := m.Manifest.Entries[0]
	want := "sha256:" + digestOf(files[transformerRel])
	if e.Digest != want || e.Evidence != content.EvidenceHashed || e.Role != source.RoleWeights || e.Locator != within(transformerRel) {
		t.Fatalf("entry %+v", e)
	}
	fixture.Equal(t, "annotations", e.Annotations, map[string]string{
		"release.repository": "black-forest-labs/FLUX.2-klein-4B",
		"release.revision":   "e7b7dc27f91deacad38e78976d1f2b499d76a294",
		"release.path":       "transformer/diffusion_pytorch_model.safetensors",
		"release.license":    "Apache-2.0",
	})
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"musetree"})
	obj, _ := fixture.Object(creator, within(transformerRel))
	if obj.Digest != want || obj.Evidence != content.EvidenceHashed {
		t.Fatalf("object %+v", obj)
	}

	// A VAE is a component whose base is its transformer's family.
	turbo := fixture.MustManifest(t, creator, "asus", turboRel)
	family := descriptor(testPins()[2]).Family
	if turbo.Manifest.Entries[0].Role != source.RoleVAE || turbo.Manifest.Descriptor["role"] != source.RoleVAE ||
		turbo.Manifest.Descriptor["base"] != family || family == "" ||
		turbo.Manifest.Entries[0].Annotations["release.use_policy"] != hummingbirdPolicy {
		t.Fatalf("TurboVAED %+v, transformer family %q", turbo.Manifest, family)
	}
	if got := m.Manifest.Descriptor["family"]; got != descriptor(testPins()[0]).Family || got == "" {
		t.Fatalf("transformer family %q", got)
	}

	// The extension-less bge-m3 file and the whisper ggml file take their
	// media type from the release's file name and their format from their
	// first bytes.
	emb := fixture.MustManifest(t, llm, "asus", embRel)
	if emb.Manifest.Entries[0].MediaType != "application/vnd.gguf" || emb.Manifest.Descriptor["format"] != "gguf" {
		t.Fatalf("bge-m3 %+v", emb.Manifest)
	}
	fixture.Equal(t, "bge-m3 holders", fixture.Holders(emb), []string{"asus-llm-server"})
	ggml := fixture.MustManifest(t, whisper, "asus", whisperRel)
	if ggml.Manifest.Descriptor["format"] != "ggml" || ggml.Manifest.Names[1].Name != "ggerganov/whisper.cpp@5359861c739e955e79d9a303bcbc70fb988958b1:ggml-medium.bin" {
		t.Fatalf("whisper %+v", ggml.Manifest)
	}

	// Only candidates were read: the record names the transformer,
	// TurboVAED, the substituted i2v file, bge-m3 and whisper, never the
	// ASUS-made files, the pickles, the resized file or the unknown files.
	fixture.Equal(t, "hashed", hashed(t, env, record), []string{transformerRel, i2vRel, turboRel, embRel, whisperRel})
}

// TestSizeGuard: a file at a pinned path with another size is another file,
// never opened and never an error.
func TestSizeGuard(t *testing.T) {
	_, env, record := machine(t)
	opened := map[string]bool{}
	trust = func(p string) error { opened[p] = true; return nil }
	o := settled(t, env, "asus-aicreator")
	if _, ok := fixture.Object(o, within(resizedRel)); ok || opened[full(env, resizedRel)] {
		t.Fatalf("the resized file was a candidate: %v", fixture.Locators(o))
	}
	for _, e := range o.Errors {
		if e.Locator == within(resizedRel) {
			t.Fatalf("the resized file is an error: %+v", e)
		}
	}
	for _, p := range hashed(t, env, record) {
		if p == resizedRel {
			t.Fatal("the resized file was hashed")
		}
	}
}

// TestUntrustedNeverOpened: a candidate whose owner or access the rule
// refuses is reported and never read, so a file another account planted at
// a pinned path cannot borrow a recorded digest.
func TestUntrustedNeverOpened(t *testing.T) {
	_, env, record := machine(t)
	trust = func(p string) error {
		if p == full(env, transformerRel) {
			return errors.New("it is owned by S-1-5-21-planted")
		}
		return nil
	}
	o := settled(t, env, "asus-aicreator")
	fixture.Equal(t, "listed", fixture.Locators(o), []string{within(turboRel)})
	var detail string
	for _, e := range o.Errors {
		if e.Locator == within(transformerRel) {
			detail = e.Detail
		}
	}
	if !strings.Contains(detail, "not opened") || !strings.Contains(detail, "planted") {
		t.Fatalf("errors %v", o.Errors)
	}
	for _, p := range hashed(t, env, record) {
		if p == transformerRel {
			t.Fatal("the untrusted file was hashed")
		}
	}
}

// TestSameDigestAsOtherStores: a listed object carries the digest the
// Hugging Face adapter names the same bytes by ("sha256:" and the LFS
// object id), so the composed inventory sees one object in both stores.
func TestSameDigestAsOtherStores(t *testing.T) {
	_, env, _ := machine(t)
	o := settled(t, env, "asus-aicreator")
	obj, ok := fixture.Object(o, within(turboRel))
	if !ok || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(obj.Digest) || obj.Digest != "sha256:"+testPins()[1].sha256 {
		t.Fatalf("object %+v", obj)
	}
}

// TestHashOnceAndReverify: an unchanged file is not read again, in this
// process or after a restart; a file whose time changes, or that a
// same-size file renamed in with the old time replaces, is hashed again and
// dropped when its bytes no longer match the pin; a vanished file leaves
// the record.
func TestHashOnceAndReverify(t *testing.T) {
	windowsNames(t)
	_, env, record := machine(t)
	path := full(env, transformerRel)
	if got := fixture.Locators(settled(t, env, "asus-aicreator")); len(got) != 2 {
		t.Fatalf("first listing %v", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Same file, same size, same time, different bytes (an in-place write,
	// which the owner rule keeps from other accounts): the record answers,
	// so the file is not read.
	tampered := strings.Replace(files[transformerRel], "public", "PUBLIC", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := fixture.Locators(settled(t, env, "asus-aicreator")); len(got) != 2 {
		t.Fatalf("unchanged stamp re-read the file: %v", got)
	}
	restarted := env
	restarted.Digests = source.NewDigests(record)
	if got := fixture.Locators(settled(t, restarted, "asus-aicreator")); len(got) != 2 {
		t.Fatalf("restart lost the record: %v", got)
	}

	// A same-size file renamed over it with the old time is another file:
	// it is hashed again and dropped.
	if err := os.WriteFile(path, []byte(files[transformerRel]), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	restarted.Digests = source.NewDigests(record)
	settled(t, restarted, "asus-aicreator")
	planted := path + ".new"
	if err := os.WriteFile(planted, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(planted, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(planted, path); err != nil {
		t.Fatal(err)
	}
	o := settled(t, restarted, "asus-aicreator")
	fixture.Equal(t, "listed after rename", fixture.Locators(o), []string{within(turboRel)})
	fixture.Equal(t, "errors after rename", fixture.ErrorKinds(o), []string{"other " + within(transformerRel), "other " + within(vaePthRel), "other " + within(i2vRel)})

	// A new modification time re-verifies too; restoring the public bytes
	// under it lists the file again.
	if err := os.WriteFile(path, []byte(files[transformerRel]), 0o644); err != nil {
		t.Fatal(err)
	}
	later := st.ModTime().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	fixture.Equal(t, "listed after restore", fixture.Locators(settled(t, restarted, "asus-aicreator")), []string{within(transformerRel), within(turboRel)})

	// A file that vanished leaves the record at the next listing.
	if err := os.Remove(full(env, turboRel)); err != nil {
		t.Fatal(err)
	}
	settled(t, restarted, "asus-aicreator")
	for _, p := range hashed(t, env, record) {
		if p == turboRel {
			t.Fatal("a vanished file stayed in the record")
		}
	}
}

// TestNoRecordListsNothing: with no digest record nothing is hashed, and
// the candidates are other files with no error.
func TestNoRecordListsNothing(t *testing.T) {
	_, env, _ := machine(t)
	env.Digests = nil
	o := observe(t, env, "asus-aicreator")
	if len(o.Manifests) != 0 || len(o.Objects) != 0 || len(o.Errors) != 0 || o.OtherFiles != 9 {
		t.Fatalf("without a record: %d manifests, %d objects, %d other, errors %v", len(o.Manifests), len(o.Objects), o.OtherFiles, o.Errors)
	}
}

// TestPins checks the shipped table against ASUS-INVENTORY.md: every pin
// names a loadable format with a full digest and revision, each public file
// at its ASUS path matches exactly one pin, and the ASUS-made files and
// pickles at their real paths and sizes match none.
func TestPins(t *testing.T) {
	windowsNames(t)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	pickle := regexp.MustCompile(`(?i)\.(pth|pt|ckpt|pkl|pickle)$`)
	stores := map[string]bool{}
	for _, f := range folders {
		stores[f.store] = true
	}
	for _, p := range pins {
		formatOK := map[string]bool{"safetensors": true, "gguf": true, "ggml": true}[p.format]
		if !formatOK || pickle.MatchString(p.pattern) || pickle.MatchString(p.file) || !stores[p.store] || p.model == "" ||
			!hex64.MatchString(p.sha256) || !hex40.MatchString(p.revision) || p.size <= 0 || p.license == "" || p.role == "" {
			t.Fatalf("pin %+v", p)
		}
		if strings.HasSuffix(p.file, ".bin") && p.format == "safetensors" {
			t.Fatalf("a .bin pin must name ggml or gguf: %+v", p)
		}
		if _, err := path.Match(p.pattern, ""); err != nil {
			t.Fatalf("pattern %q: %v", p.pattern, err)
		}
	}
	a := New()
	real := []struct {
		store, rel string
		size       int64
		listed     bool
	}{
		{"asus-aicreator", "flux_2/checkpoints/FLUX.2-klein-4B/transformer/diffusion_pytorch_model.safetensors", 7751109744, true},
		{"asus-aicreator", "flux_2/checkpoints/FLUX.2-klein-4B/vae/diffusion_pytorch_model.safetensors", 168120878, true},
		{"asus-aicreator", "hummingbird/HM_2.2_5B/transformer_20251218/diffusion_pytorch_model_i2v.safetensors", 9999659672, true},
		{"asus-aicreator", "hummingbird/vae_decoder/wan22_v1_tiling_16_12/diffusion_pytorch_model.safetensors", 152049060, true},
		{"asus-llmserver", "emb_2_3", 437778496, true},
		{"asus-whisperrecorder", "ggml-medium.bin", 1533763059, true},
		{"asus-aicreator", "hummingbird/HM_2.2_5B/transformer_20251218/diffusion_pytorch_model_t2v.safetensors", 9999658880, false},
		{"asus-aicreator", "flux_2/checkpoints/FLUX.2-klein-4B/text_encoder_4bit/model.safetensors", 2708678012, false},
		{"asus-aicreator", "hummingbird/HM_2.2_5B/text_encoder_4bit/quantized_text_encoder.pth", 4453450493, false},
		{"asus-aicreator", "hummingbird/HM_2.2_5B/Wan2.2_VAE.pth", 2818839170, false},
		{"asus-aicreator", "hummingbird/HM_2.2_5B/models_t5_umt5-xxl-enc-bf16.pth", 11361920418, false},
		{"asus-aicreator", "clip/blip-image-captioning-large/pytorch_model.bin", 1879143921, false},
		{"asus-aicreator", "processor/sam_vit_l_0b3195.pth", 1249524607, false},
		{"asus-aicreator", "upscale/RealESRGAN_x4plus.pth", 67040989, false},
		{"asus-aicreator", "upscale/RealESRGAN_x4plus_anime_6B.pth", 17938799, false},
		{"asus-llmserver", "llm_2_16", 4920740000, false},
		{"asus-llmserver", "uninstall-emb.bat", 1238, false},
		// A pinned file in another folder is not a candidate.
		{"asus-aicreator", "emb_2_3", 437778496, false},
	}
	seen := map[string]bool{}
	for _, r := range real {
		p, ok := a.match(r.store, source.File{Rel: r.rel, Size: r.size})
		if ok != r.listed {
			t.Fatalf("%s %s: matched %v, want %v", r.store, r.rel, ok, r.listed)
		}
		if ok {
			if seen[p.sha256] {
				t.Fatalf("%s matched a pin twice", r.rel)
			}
			seen[p.sha256] = true
		}
	}
	if len(seen) != len(pins) {
		t.Fatalf("matched %d of %d pins", len(seen), len(pins))
	}
	// The case ASUS's folders are spelled in does not decide a match.
	if _, ok := a.match(real[0].store, source.File{Rel: strings.ToUpper(real[0].rel[:6]) + real[0].rel[6:], Size: real[0].size}); !ok {
		t.Fatal("case decided a match")
	}
}
