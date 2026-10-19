package source

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Kinds seeded by §5.3.
const (
	KindModel    = "abstraction.model/descriptor@1"
	KindSnapshot = "abstraction.storage/snapshot@1"
	KindObject   = "abstraction.storage/object@1"
)

// Purposes seeded by §5.2.
const (
	PurposeFetched   = "fetched"
	PurposeInstalled = "installed"
	PurposePinned    = "pinned"
	PurposeCache     = "cache"
)

// Roles seeded by §5.3. RoleProjector and RoleVAE are also the closed
// vocabulary MODEL-C3 gives the descriptor's "role" key for a component: a
// part of a family, never a family of its own.
const (
	RoleWeights       = "weights"
	RoleWeightsConfig = "weights.config"
	RoleTemplate      = "template"
	RoleParams        = "params"
	RoleLicense       = "license"
	RoleProjector     = "projector"
	RoleVAE           = "vae"
	RoleDoc           = "doc"
)

// weightExts are the unambiguous weight extensions: nothing else a store
// adapter reads uses them. ".bin" is not among them — see IsWeightFile.
var weightExts = []string{".gguf", ".safetensors", ".pth", ".onnx", ".pt", ".ckpt", ".sft"}

// ambiguousWeightExt is checked for by name only after IsWeightFile finds
// other evidence: PyTorch's own checkpoint format uses ".bin", and so does
// vendored tooling a model host ships beside its store (Lemonade's cache
// carries ROCm benchmark matrices under this extension —
// research/model-bridge/EXPERIMENT-2026-09-22.md).
const ambiguousWeightExt = ".bin"

// ggmlMagic is the four bytes whisper.cpp's ggml model files start with:
// the little-endian uint32 0x67676d6c ("ggml"). A ggml file holds tensors
// and a vocabulary and no code (LS-1).
const ggmlMagic = "lmgg"

// IsWeights reports the weight-file extensions the §3 prototype counted,
// by name alone. It does not resolve ".bin", which name alone cannot: see
// IsWeightFile for the classifier a walk uses to accept or reject a file.
func IsWeights(name string, extra ...string) bool {
	lower := strings.ToLower(name)
	for _, e := range append(append([]string{}, weightExts...), extra...) {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

// IsWeightFile reports whether the file at fullPath, named rel inside its
// store, is a model weight. Every extension IsWeights recognises is enough
// evidence by itself. ".bin" is not (see ambiguousWeightExt); it counts as a
// weight only when one of:
//   - a manifest sits beside it in the same directory: config.json,
//     model.safetensors.index.json or pytorch_model.bin.index.json — the
//     files a real PyTorch or safetensors checkpoint directory carries;
//   - rel passes through a Hugging Face cache "snapshots/<rev>/" segment,
//     the layout huggingface_hub always writes a real download into;
//   - the file's own header is a format this reader recognises: GGUF's
//     magic, whisper.cpp's ggml magic, or the zip or pickle signature a
//     PyTorch checkpoint starts with.
//
// A bare ".bin" satisfying none of these — a vendored binary sitting alone
// in an operator-named root — is not listed. Every check reads at most a
// handful of bytes or sibling directory entries: no tensor is read and
// nothing is hashed.
func IsWeightFile(fullPath, rel string, extra ...string) bool {
	if IsWeights(rel, extra...) {
		return true
	}
	if !strings.HasSuffix(strings.ToLower(rel), ambiguousWeightExt) {
		return false
	}
	if hasSnapshotsSegment(rel) {
		return true
	}
	if hasSiblingManifest(fullPath) {
		return true
	}
	return hasKnownBinHeader(fullPath)
}

func hasSnapshotsSegment(rel string) bool {
	for _, seg := range strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/") {
		if seg == "snapshots" {
			return true
		}
	}
	return false
}

// binManifestNames are the files a real PyTorch or safetensors checkpoint
// directory carries beside its weights. Their presence, not the ".bin"
// file's own name, is what tells a bare pytorch_model.bin apart from
// vendored tooling that happens to share its extension.
var binManifestNames = []string{"config.json", "model.safetensors.index.json", "pytorch_model.bin.index.json"}

func hasSiblingManifest(fullPath string) bool {
	dir := filepath.Dir(fullPath)
	for _, name := range binManifestNames {
		if IsFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// hasKnownBinHeader reads the first bytes of a ".bin" file and checks them
// against the formats a weight file starts with: GGUF's own magic, the zip
// local-file-header PyTorch's checkpoint format has used since it moved off
// raw pickle, or a bare pickle stream (its legacy format).
func hasKnownBinHeader(fullPath string) bool {
	b, ok := firstBytes(fullPath)
	if !ok {
		return false
	}
	switch {
	case string(b[:]) == gguf.Magic, string(b[:]) == ggmlMagic:
		return true
	case b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04:
		return true // zip local file header: torch.save's container format
	case b[0] == 0x80 && b[1] >= 2 && b[1] <= 5:
		return true // pickle protocol 2-5: torch.save's legacy format
	}
	return false
}

// pickleExts name PyTorch's and Python's pickle files by extension alone.
var pickleExts = []string{".pth", ".pt", ".ckpt", ".pkl", ".pickle"}

// modelExts are the model formats OA loads, by extension: GGUF,
// safetensors, ONNX and FastFlowLM's q4nx.
var modelExts = []struct{ ext, format string }{
	{".gguf", "gguf"}, {".safetensors", "safetensors"}, {".sft", "safetensors"},
	{".onnx", "onnx"}, {".q4nx", "q4nx"},
}

// ModelFormatOf is the model format of the file at fullPath, named rel, or
// empty when it is in none OA loads (abstraction-provider-modelbridge
// CONTRACT.md MODELBRIDGE-L21): gguf, safetensors, onnx or q4nx by
// extension, gguf for any other name whose first four bytes are GGUF's
// magic (an Ollama blob, a GGUF .bin), or ggml for one whose first four
// bytes are whisper.cpp's ggml magic. A pickle is in none. An empty
// fullPath judges by name alone, as a container member is judged (LS-9).
func ModelFormatOf(fullPath, rel string) string {
	if IsPickle(fullPath, rel) {
		return ""
	}
	lower := strings.ToLower(rel)
	for _, m := range modelExts {
		if strings.HasSuffix(lower, m.ext) {
			return m.format
		}
	}
	if b, ok := firstBytes(fullPath); ok {
		switch string(b[:]) {
		case gguf.Magic:
			return "gguf"
		case ggmlMagic:
			return "ggml"
		}
	}
	return ""
}

// IsPickle reports a weight file OA never loads (LS-1, VISION 2026-10-01):
// a pickle, named .pth, .pt, .ckpt, .pkl or .pickle, or a weight ".bin"
// whose header is neither GGUF's magic nor whisper.cpp's ggml magic. The file is listed as an object
// and is never a manifest's weights. A caller reaches IsPickle only for a
// file IsWeightFile accepted; the check reads at most four bytes.
func IsPickle(fullPath, rel string) bool {
	lower := strings.ToLower(rel)
	for _, e := range pickleExts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	if !strings.HasSuffix(lower, ambiguousWeightExt) {
		return false
	}
	b, ok := firstBytes(fullPath)
	return !ok || (string(b[:]) != gguf.Magic && string(b[:]) != ggmlMagic)
}

// firstBytes is a regular file's first four bytes. It opens without
// blocking (O_NONBLOCK on Unix, where a FIFO's open would wait for a writer
// while the model bridge holds its lock) and checks the opened handle, so a
// path swapped for a FIFO, a device or a directory after any earlier stat is
// refused rather than read.
func firstBytes(fullPath string) ([4]byte, bool) {
	var b [4]byte
	f, err := os.OpenFile(fullPath, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return b, false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return b, false
	}
	//unchecked: io.ReadFull only errors when it reads fewer than len(b) bytes, which the n < 4 check already detects
	n, _ := io.ReadFull(f, b[:])
	return b, n == 4
}

// RoleOf classifies a file by name. It returns "" for a file with no seeded
// role. A caller reaches RoleOf only for a file IsWeightFile already accepted,
// so unlike IsWeights, a ".bin" here is weight evidence on its own.
func RoleOf(name string, extraWeights ...string) string {
	base := strings.ToLower(pathBase(name))
	weight := IsWeights(base, extraWeights...) || strings.HasSuffix(base, ambiguousWeightExt)
	switch {
	case weight && (strings.Contains(base, "mmproj") || strings.Contains(base, "vision_weight") || strings.Contains(base, "projector")):
		return RoleProjector
	case weight && hasNameToken(base, "vae"):
		return RoleVAE
	case weight:
		return RoleWeights
	case base == "config.json" || base == "generation_config.json" || strings.HasPrefix(base, "tokenizer") || base == "special_tokens_map.json" || base == "params.json":
		return RoleWeightsConfig
	case strings.HasSuffix(base, ".jinja") || strings.Contains(base, "template"):
		return RoleTemplate
	case strings.HasPrefix(base, "license") || strings.HasPrefix(base, "licence"):
		return RoleLicense
	case strings.HasPrefix(base, "readme") || strings.HasSuffix(base, ".md"):
		return RoleDoc
	}
	return ""
}

// hasNameToken reports whether word appears in base as its own token, split
// on the separators a model file name uses ("-", "_", ".", " "): "wan2.2-vae"
// has "vae", "gravel.safetensors" does not.
func hasNameToken(base, word string) bool {
	for _, t := range strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		if t == word {
			return true
		}
	}
	return false
}

// MediaTypeOf names a media type by extension. No catalogue of local model
// media types exists; these are the IANA types where one is registered and
// the file format's own name otherwise.
func MediaTypeOf(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".gguf"):
		return "application/vnd.gguf"
	case strings.HasSuffix(lower, ".safetensors"), strings.HasSuffix(lower, ".sft"):
		return "application/vnd.safetensors"
	case strings.HasSuffix(lower, ".onnx"):
		return "application/vnd.onnx"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".jinja"):
		return "text/x-jinja2"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "application/yaml"
	}
	return "application/octet-stream"
}

// FileEntry is an entry for a file known by locator and size only.
func FileEntry(role, locator string, size int64) content.Entry {
	return content.Entry{
		Role:      role,
		Size:      size,
		MediaType: MediaTypeOf(locator),
		Evidence:  content.EvidenceNone,
		Locator:   locator,
	}
}

// NewManifest builds a manifest and its id by the rule of the Manifest record.
// Every entry carries a hashed or named digest: addressing content, id over
// kind, each entry's role, digest, size and media_type, and descriptor. Any
// other entry: addressing name, id over kind, each entry's role, size and
// media_type, descriptor, and names[0], the store's own name for the manifest.
// Locators, annotations, the other names and the store never enter the id.
func NewManifest(store, kind string, entries []content.Entry, names []content.Name, descriptor map[string]string) content.Manifest {
	if entries == nil {
		entries = []content.Entry{}
	}
	if names == nil {
		names = []content.Name{}
	}
	addressing := content.AddressingContent
	for _, e := range entries {
		if e.Evidence != content.EvidenceHashed && e.Evidence != content.EvidenceNamed {
			addressing = content.AddressingName
			break
		}
	}
	sum := sha256.Sum256(canonicalIdentity(addressing, kind, entries, names, descriptor))
	return content.Manifest{
		ID:         "sha256:" + hex.EncodeToString(sum[:]),
		Addressing: addressing,
		Kind:       kind,
		Entries:    entries,
		Names:      names,
		Descriptor: descriptor,
		Store:      store,
	}
}

// canonicalIdentity is the identity document of a manifest in this
// definition's encoding with indent 0: minimal escaping, map keys in UTF-8
// byte order, integers in decimal, fields in declaration order.
//
//	{"kind":K,"entries":[{"role":R,"digest":D,"size":N,"media_type":M}],"descriptor":{...},"names":[{"scheme":S,"name":X}]}
//
// digest is absent for name addressing; names carries names[0] only and is
// absent for content addressing; descriptor is absent when empty.
func canonicalIdentity(addressing content.Addressing, kind string, entries []content.Entry, names []content.Name, descriptor map[string]string) []byte {
	out := []byte(`{"kind":`)
	out = jsonString(out, kind)
	out = append(out, `,"entries":[`...)
	for i, e := range entries {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, `{"role":`...)
		out = jsonString(out, e.Role)
		if addressing == content.AddressingContent {
			out = append(out, `,"digest":`...)
			out = jsonString(out, e.Digest)
		}
		out = append(out, `,"size":`...)
		out = strconv.AppendInt(out, e.Size, 10)
		out = append(out, `,"media_type":`...)
		out = jsonString(out, e.MediaType)
		out = append(out, '}')
	}
	out = append(out, ']')
	if len(descriptor) > 0 {
		keys := make([]string, 0, len(descriptor))
		for k := range descriptor {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Go compares strings bytewise: UTF-8 byte order
		out = append(out, `,"descriptor":{`...)
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = jsonString(out, k)
			out = append(out, ':')
			out = jsonString(out, descriptor[k])
		}
		out = append(out, '}')
	}
	if addressing == content.AddressingName && len(names) > 0 {
		out = append(out, `,"names":[{"scheme":`...)
		out = jsonString(out, names[0].Scheme)
		out = append(out, `,"name":`...)
		out = jsonString(out, names[0].Name)
		out = append(out, `}]`...)
	}
	return append(out, '}')
}

// jsonString appends s with the definition's minimal escaping: quote,
// backslash and the control characters, byte by byte.
func jsonString(out []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case 0x08:
			out = append(out, '\\', 'b')
		case 0x0c:
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if c < 0x20 {
				out = append(out, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			} else {
				out = append(out, c)
			}
		}
	}
	return append(out, '"')
}

// FormatOf is the file format word for a descriptor.
func FormatOf(name string) string {
	lower := strings.ToLower(name)
	for _, f := range []string{"gguf", "safetensors", "onnx", "q4nx", "ckpt", "pth", "pt", "bin", "sft"} {
		if strings.HasSuffix(lower, "."+f) {
			return f
		}
	}
	return ""
}

// ObservedHold is a hold a source reports: attestation observed, lifetime
// while_present, with the index entry that proves it. id, account and
// established_by are the runtime's to fill; domain is empty because the source
// reads on its own machine.
func ObservedHold(holder Holder, target, purpose string, basis content.Basis, at string) content.Hold {
	b := basis
	return content.Hold{
		Holder:      content.Holder{Program: holder.Program, Instance: holder.Instance},
		Target:      target,
		Purpose:     purpose,
		Lifetime:    content.LifetimeWhilePresent,
		Attestation: content.AttestationObserved,
		ObservedAt:  at,
		Basis:       &b,
	}
}

// Hold records that program holds manifest i through the index entry key of
// index. One hold per holder and basis; a repeated basis is recorded once.
func (o *StoreObservation) Hold(i int, program, purpose, index, key, at string) {
	o.HoldBy(i, Holder{Program: program}, purpose, content.Basis{Index: index, Key: key}, at)
}

// HoldBy records a hold with the holder's instance and the basis's role.
func (o *StoreObservation) HoldBy(i int, holder Holder, purpose string, basis content.Basis, at string) {
	mh := &o.Manifests[i]
	for _, h := range mh.Holds {
		if h.Holder.Program == holder.Program && h.Holder.Instance == holder.Instance && h.Basis != nil && *h.Basis == basis {
			return
		}
	}
	mh.Holds = append(mh.Holds, ObservedHold(holder, mh.Manifest.ID, purpose, basis, at))
}

// Want records an index entry that names content this store does not hold.
func (o *StoreObservation) Want(holder Holder, basis content.Basis, reference, expectedDigest string, expectedSize int64, at string) {
	o.Dangling = append(o.Dangling, NewDangling(holder, basis, reference, expectedDigest, expectedSize, at))
}

// NewDangling is the Dangling record for an index entry naming absent content.
func NewDangling(holder Holder, basis content.Basis, reference, expectedDigest string, expectedSize int64, at string) content.Dangling {
	return content.Dangling{
		Holder:         content.Holder{Program: holder.Program, Instance: holder.Instance},
		Basis:          basis,
		Reference:      reference,
		ExpectedDigest: expectedDigest,
		ExpectedSize:   expectedSize,
		ObservedAt:     at,
	}
}

// AddManifest appends a manifest with no holds and returns its index.
func (o *StoreObservation) AddManifest(m content.Manifest) int {
	o.Manifests = append(o.Manifests, content.ManifestHolders{Manifest: m, Holds: []content.Hold{}})
	return len(o.Manifests) - 1
}

// AddObject appends an object for a walked file.
func (o *StoreObservation) AddObject(locator string, f File, digest string, evidence content.Evidence) {
	if evidence == 0 {
		evidence = content.EvidenceNone
	}
	o.Objects = append(o.Objects, content.Object{
		Locator: locator, Size: f.Size, Modified: Stamp(f.ModTime),
		Partial: IsPartial(locator), Digest: digest, Evidence: evidence,
	})
}

// Fail records a typed error.
func (o *StoreObservation) Fail(kind content.StoreErrorKind, locator string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	o.Errors = append(o.Errors, content.StoreError{Kind: kind, Locator: locator, Detail: detail})
}

// Begin starts an observation of a store with empty, non-nil lists.
func Begin(store content.Store) StoreObservation {
	store.Errors = append([]content.StoreError{}, store.Errors...)
	return StoreObservation{
		Store:     store,
		Objects:   []content.Object{},
		Manifests: []content.ManifestHolders{},
		Dangling:  []content.Dangling{},
	}
}

// Finish sorts every list for stable output: manifests in id order as
// Snapshot pages are, objects by locator, holds by holder and basis.
func (o *StoreObservation) Finish() StoreObservation {
	sort.SliceStable(o.Objects, func(i, j int) bool { return o.Objects[i].Locator < o.Objects[j].Locator })
	sort.SliceStable(o.Manifests, func(i, j int) bool { return o.Manifests[i].Manifest.ID < o.Manifests[j].Manifest.ID })
	for i := range o.Manifests {
		hs := o.Manifests[i].Holds
		sort.SliceStable(hs, func(a, b int) bool { return HoldKey(hs[a]) < HoldKey(hs[b]) })
	}
	sort.SliceStable(o.Dangling, func(i, j int) bool { return DanglingKey(o.Dangling[i]) < DanglingKey(o.Dangling[j]) })
	return *o
}

// HoldKey orders and identifies a hold by holder, basis and target.
func HoldKey(h content.Hold) string {
	b := content.Basis{}
	if h.Basis != nil {
		b = *h.Basis
	}
	return strings.Join([]string{h.Holder.Program, h.Holder.Instance, b.Index, b.Key, b.Role, h.Target}, "\x00")
}

// DanglingKey orders and identifies a dangling reference.
func DanglingKey(d content.Dangling) string {
	return strings.Join([]string{d.Holder.Program, d.Holder.Instance, d.Basis.Index, d.Basis.Key, d.Basis.Role, d.Reference}, "\x00")
}

// EntryLocators lists the locators a manifest's entries name.
func EntryLocators(m content.Manifest) []string {
	var out []string
	for _, e := range m.Entries {
		if e.Locator != "" {
			out = append(out, e.Locator)
		}
	}
	return out
}
