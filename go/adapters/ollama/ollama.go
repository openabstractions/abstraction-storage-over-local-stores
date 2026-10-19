// Package ollama reads Ollama's model store: manifests-v2/<host>/<namespace>/
// <model>/<tag> (Ollama v0.40.0 and later: a manifest, or a manifest list of
// runner-specific children, each a reference to a blob or a manifest in
// place; the named manifest is itself a blob) and the legacy manifests/
// tree, over blobs/sha256-<hex>. Shapes follow Ollama v0.40.6's
// manifest/manifest.go and manifest/paths.go.
package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstraction-storage/go/digest"
)

const program = "ollama"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover: OLLAMA_MODELS when set, which is where the server reads and
// writes; otherwise ~/.ollama/models, plus the Linux service account's
// /usr/share/ollama/.ollama/models when that exists.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	if v := env.Var("OLLAMA_MODELS"); v != "" {
		d.Stores = append(d.Stores, source.NewStore("ollama", program, env.Expand(v), "environment OLLAMA_MODELS"))
		return d
	}
	d.Stores = append(d.Stores, source.NewStore("ollama", program, filepath.Join(env.Home, ".ollama", "models"), "default location ~/.ollama/models"))
	if env.GOOS == "linux" {
		svc := "/usr/share/ollama/.ollama/models"
		if source.IsDir(svc) {
			d.Stores = append(d.Stores, source.NewStore("ollama-service", program, svc, "default location of the Linux service account"))
		}
	}
	return d
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// manifest is Ollama v0.40.6's Manifest as its MarshalJSON writes it
// (manifest/manifest.go): a plain manifest has config, layers and, when set,
// runner and format; a manifest list has manifests.
type manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        json.RawMessage `json:"config"`
	Layers        json.RawMessage `json:"layers"`
	Runner        string          `json:"runner"`
	Format        string          `json:"format"`
	Manifests     []child         `json:"manifests"`
}

// child is one runner-specific manifest a manifest list names. A reference
// child (NewManifestReference) is {mediaType, digest, runner, format} and
// names a blob holding the child manifest; it states no size. An inline
// child is a full manifest with config and layers and no digest. Raw keeps
// the child's own JSON for the inline case.
type child struct {
	MediaType string          `json:"mediaType"`
	Digest    string          `json:"digest"`
	Size      int64           `json:"size"`
	Runner    string          `json:"runner"`
	Format    string          `json:"format"`
	Config    json.RawMessage `json:"config"`
	Layers    json.RawMessage `json:"layers"`
	Raw       json.RawMessage `json:"-"`
}

func (c *child) UnmarshalJSON(b []byte) error {
	type plainChild child
	var p plainChild
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = child(p)
	c.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// reference is a child read through its blob. It mirrors Ollama v0.40.6's
// isReference: the digest is set, the config states no digest and there are
// no layers. Every other child is a manifest carried in place, whatever
// digest it states.
func (c child) reference() bool {
	if c.Digest == "" {
		return false
	}
	var config struct {
		Digest string `json:"digest"`
	}
	if len(c.Config) > 0 && json.Unmarshal(c.Config, &config) != nil {
		return false
	}
	var layers []json.RawMessage
	if len(c.Layers) > 0 && json.Unmarshal(c.Layers, &layers) != nil {
		return false
	}
	return config.Digest == "" && len(layers) == 0
}

// listMediaType marks a manifest list (Ollama v0.40.0 and later).
const listMediaType = "application/vnd.ollama.manifest.list.v2+json"

// roleManifest is the role of a manifest's own blob: the named manifest or
// manifest list a manifests-v2 name copies or links, and a reference child's
// blob. Ollama counts each as referenced (referencedBlobDigestsForData).
const roleManifest = "ollama/manifest"

// maxManifestBytes bounds the bytes compared to match a named manifest's
// copy to its blob; a manifest is a few hundred bytes.
const maxManifestBytes = 1 << 20

// model is one manifest a named manifest yields, before its own blob is
// placed.
type model struct {
	index, name string
	// own is the named file whose blob the manifest's first entry names;
	// empty for the legacy tree, which Ollama does not store as a blob.
	own     source.File
	ownBody []byte
	format  string
	runner  string
	list    bool
	entries []content.Entry
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())

	blobs, errs := source.Walk(filepath.Join(loc.Locator, "blobs"))
	o.Errors = append(o.Errors, prefix(errs, "blobs")...)
	b := blobSet{size: map[string]int64{}, bySize: map[int64][]string{}}
	for _, f := range blobs {
		loc := "blobs/" + f.Rel
		if f.Link {
			o.OtherFiles++
			continue
		}
		digest, evidence := "", content.EvidenceNone
		if h, ok := blobDigest(f.Rel); ok && !source.IsPartial(f.Rel) {
			digest, evidence = "sha256:"+h, content.EvidenceNamed
			b.size[digest] = f.Size
			b.bySize[f.Size] = append(b.bySize[f.Size], digest)
		}
		o.AddObject(loc, f, digest, evidence)
	}

	// Ollama v0.40 and later resolve a name in manifests-v2/ first and fall
	// back to the legacy manifests/ tree; its lazy migration leaves both
	// names for one model, and the v2 one is the one Ollama reads.
	var models []model
	seen := map[string]bool{}
	for _, tree := range []string{"manifests-v2", "manifests"} {
		files, errs := source.Walk(filepath.Join(loc.Locator, tree))
		o.Errors = append(o.Errors, prefix(errs, tree)...)
		for _, f := range files {
			index := tree + "/" + f.Rel
			name := nameOf(tree, f.Rel)
			if seen[name] {
				continue
			}
			if f.Link && tree == "manifests" {
				o.OtherFiles++
				continue
			}
			// A v2 named manifest is a link into blobs/ (Unix) or a
			// regular-file copy of that blob (Windows); either reads as the
			// manifest's bytes.
			body, err := os.ReadFile(filepath.Join(loc.Locator, filepath.FromSlash(index)))
			if err != nil {
				o.Fail(source.ErrorUnreadableIndex, index, err)
				continue
			}
			seen[name] = true
			m := model{index: index, name: name}
			if tree == "manifests-v2" {
				m.own, m.ownBody = f, body
			}
			models = append(models, readNamed(&o, loc.Locator, m, body, b, at)...)
		}
	}

	// A named manifest's own blob is matched only among blobs no manifest
	// names as a config or layer, so no tensor is ever read to compare.
	named := map[string]bool{}
	for _, m := range models {
		for _, e := range m.entries {
			if e.Role != roleManifest {
				named[e.Digest] = true
			}
		}
	}
	own := map[string]content.Entry{}
	for _, m := range models {
		if m.own.Rel != "" {
			head, done := own[m.index]
			if !done {
				head, _ = ownBlob(loc.Locator, m, b, named)
				own[m.index] = head
			}
			if head.Digest != "" {
				m.entries = append([]content.Entry{head}, m.entries...)
			}
		}
		addModel(&o, env, loc.Locator, m, b, at)
	}
	return o.Finish()
}

// blobSet is the blobs present by digest, with their sizes.
type blobSet struct {
	size   map[string]int64
	bySize map[int64][]string
}

func (b blobSet) present(digest string) bool {
	_, ok := b.size[digest]
	return ok
}

// ownBlob is the entry for the blob a manifests-v2 name stands for: the blob
// a link points at, by its name (LS-3), or the one blob of the copy's size
// and bytes that no manifest names as an entry (LS-4). None when neither
// finds one.
func ownBlob(root string, m model, b blobSet, named map[string]bool) (content.Entry, bool) {
	mediaType := "application/vnd.docker.distribution.manifest.v2+json"
	if m.list {
		mediaType = listMediaType
	}
	p := filepath.Join(root, filepath.FromSlash(m.index))
	if m.own.Link {
		target, err := os.Readlink(p)
		if err != nil {
			return content.Entry{}, false
		}
		h, ok := blobDigest(filepath.Base(target))
		if !ok || !b.present("sha256:"+h) {
			return content.Entry{}, false
		}
		e, err := entry(descriptor{MediaType: mediaType, Digest: "sha256:" + h, Size: b.size["sha256:"+h]}, roleManifest)
		return e, err == nil
	}
	n := int64(len(m.ownBody))
	if n == 0 || n > maxManifestBytes {
		return content.Entry{}, false
	}
	for _, d := range b.bySize[n] {
		if named[d] {
			continue
		}
		e, err := entry(descriptor{MediaType: mediaType, Digest: d, Size: n}, roleManifest)
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.Locator)))
		if err == nil && bytes.Equal(body, m.ownBody) {
			return e, true
		}
	}
	return content.Entry{}, false
}

// readNamed reads one named manifest: a plain manifest becomes one model; a
// manifest list becomes one model per child, each entry annotated with the
// child's runner and format. A reference child's own blob is its first
// entry; an inline child is no blob and has none.
func readNamed(o *source.StoreObservation, root string, m model, body []byte, b blobSet, at string) []model {
	var top manifest
	if err := json.Unmarshal(body, &top); err != nil {
		o.Fail(source.ErrorMalformedIndex, m.index, err)
		return nil
	}
	if top.MediaType != listMediaType && len(top.Manifests) == 0 {
		entries, err := parse(body)
		if err != nil {
			o.Fail(source.ErrorMalformedIndex, m.index, err)
			return nil
		}
		m.entries, m.format = entries, top.Format
		if m.format == "" {
			m.format = formatOf(entries)
		}
		return []model{m}
	}
	if len(top.Manifests) == 0 {
		o.Fail(source.ErrorMalformedIndex, m.index, errors.New("manifest list names no manifests"))
		return nil
	}
	m.list = true
	var out []model
	for i, c := range top.Manifests {
		if c.MediaType == listMediaType {
			// Ollama refuses a nested list (parseManifest).
			o.Fail(source.ErrorMalformedIndex, m.index, fmt.Errorf("manifest %d: nested manifest lists are not supported", i))
			continue
		}
		var entries []content.Entry
		if !c.reference() {
			e, err := parse(c.Raw)
			if err != nil {
				o.Fail(source.ErrorMalformedIndex, m.index, fmt.Errorf("manifest %d: %w", i, err))
				continue
			}
			entries = e
		} else {
			size := c.Size
			if size == 0 {
				// A reference child states no size; its blob's is the one.
				size = b.size[c.Digest]
			}
			head, err := entry(descriptor{MediaType: c.MediaType, Digest: c.Digest, Size: size}, roleManifest)
			if err != nil {
				o.Fail(source.ErrorMalformedIndex, m.index, fmt.Errorf("manifest %d: %w", i, err))
				continue
			}
			if !b.present(c.Digest) {
				o.Want(source.Holder{Program: program}, content.Basis{Index: m.index, Key: m.name, Role: roleManifest}, c.Digest, c.Digest, c.Size, at)
				continue
			}
			cb, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(head.Locator)))
			if err != nil {
				o.Fail(source.ErrorUnreadableIndex, head.Locator, err)
				continue
			}
			e, err := parse(cb)
			if err != nil {
				o.Fail(source.ErrorMalformedIndex, head.Locator, err)
				continue
			}
			entries = append([]content.Entry{head}, e...)
		}
		cm := m
		cm.entries, cm.runner, cm.format = entries, c.Runner, c.Format
		if cm.format == "" {
			cm.format = formatOf(entries)
		}
		out = append(out, cm)
	}
	return out
}

// addModel adds one model manifest and Ollama's hold on it, with a
// descriptor from the name and, for a held GGUF, its header. A list child's
// entries carry its runner and format as annotations.
func addModel(o *source.StoreObservation, env source.Env, root string, m model, b blobSet, at string) {
	if m.list {
		ann := map[string]string{"format": m.format}
		if m.runner != "" {
			ann["runner"] = m.runner
		}
		for j := range m.entries {
			m.entries[j].Annotations = ann
		}
	}
	index, name, entries, format := m.index, m.name, m.entries, m.format
	descriptor := source.Model{}
	for _, e := range entries {
		if !b.present(e.Digest) {
			o.Want(source.Holder{Program: program}, content.Basis{Index: index, Key: name, Role: e.Role}, e.Digest, e.Digest, e.Size, at)
		}
		if e.Role != source.RoleWeights {
			continue
		}
		descriptor = source.Named(name, format, program)
		// Ollama's model layer is a GGUF whatever its media type says, and
		// the blob it names is where the header is.
		if format == "gguf" && b.present(e.Digest) {
			if g, ok := env.Header(filepath.Join(root, filepath.FromSlash(e.Locator))); ok {
				descriptor = descriptor.WithHeader(g)
			}
		}
	}
	mf := source.NewManifest(o.Name, source.KindModel, entries, []content.Name{{Scheme: program, Name: name}}, descriptor.Map())
	i := o.AddManifest(mf)
	o.Hold(i, program, source.PurposeInstalled, index, name, at)
}

// formatOf is the weights format a manifest's layers state: safetensors for
// image.tensor layers, gguf for an image.model layer.
func formatOf(entries []content.Entry) string {
	for _, e := range entries {
		if e.Role != source.RoleWeights {
			continue
		}
		if strings.HasSuffix(e.MediaType, ".tensor") {
			return "safetensors"
		}
		return "gguf"
	}
	return ""
}

func prefix(errs []content.StoreError, dir string) []content.StoreError {
	for i := range errs {
		if errs[i].Locator == "." {
			errs[i].Locator = dir
		} else {
			errs[i].Locator = dir + "/" + errs[i].Locator
		}
	}
	return errs
}

// blobDigest reads sha256-<64 hex> from a blob name.
func blobDigest(name string) (string, bool) {
	if !strings.HasPrefix(name, "sha256-") || len(name) < 71 {
		return "", false
	}
	h := name[7:71]
	if !digest.Valid(digest.Prefix + h) {
		return "", false
	}
	return h, true
}

// nameOf turns manifests[-v2]/<host>/<namespace>/<model>/<tag> into the name
// Ollama shows: registry.ollama.ai (v1) and ollama.com (v2) are implicit, so
// library/tiny:1b. manifests-v2 spells a host's port colon as %3A
// (paths.go encodeV2ManifestHost; decodeV2ManifestHost also reads %3a), the
// legacy tree as the colon itself, so both trees give one name.
func nameOf(tree, rel string) string {
	if tree == "manifests-v2" {
		host, rest, ok := strings.Cut(rel, "/")
		if ok {
			rel = strings.NewReplacer("%3A", ":", "%3a", ":").Replace(host) + "/" + rest
		}
	}
	dir, tag := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	dir = strings.TrimPrefix(dir, "registry.ollama.ai/")
	dir = strings.TrimPrefix(dir, "ollama.com/")
	if dir == "" {
		return tag
	}
	return dir + ":" + tag
}

func parse(b []byte) ([]content.Entry, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	var layers []descriptor
	if len(m.Layers) == 0 {
		return nil, errors.New("no layers")
	}
	if err := json.Unmarshal(m.Layers, &layers); err != nil {
		return nil, fmt.Errorf("layers: %w", err)
	}
	var out []content.Entry
	if len(m.Config) > 0 && string(m.Config) != "null" {
		var c descriptor
		if err := json.Unmarshal(m.Config, &c); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if c.Digest != "" {
			e, err := entry(c, source.RoleWeightsConfig)
			if err != nil {
				return nil, fmt.Errorf("config: %w", err)
			}
			out = append(out, e)
		}
	}
	for i, l := range layers {
		e, err := entry(l, roleOf(l.MediaType))
		if err != nil {
			return nil, fmt.Errorf("layer %d: %w", i, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func entry(d descriptor, role string) (content.Entry, error) {
	h := strings.TrimPrefix(d.Digest, "sha256:")
	if !digest.Valid(d.Digest) {
		return content.Entry{}, fmt.Errorf("digest %q is not sha256:<64 hex>", d.Digest)
	}
	if d.Size < 0 {
		return content.Entry{}, fmt.Errorf("size %d", d.Size)
	}
	return content.Entry{
		Role:      role,
		Digest:    d.Digest,
		Size:      d.Size,
		MediaType: d.MediaType,
		Evidence:  content.EvidenceNamed,
		Locator:   "blobs/sha256-" + h,
	}, nil
}

// roleOf maps application/vnd.ollama.image.<kind> onto the seeded roles; kinds
// with no seed keep Ollama's word under its owner.
func roleOf(mediaType string) string {
	kind := mediaType
	if i := strings.LastIndexByte(mediaType, '.'); i >= 0 {
		kind = mediaType[i+1:]
	}
	switch kind {
	case "model", "tensor":
		return source.RoleWeights
	case "projector":
		return source.RoleProjector
	case "template":
		return source.RoleTemplate
	case "params":
		return source.RoleParams
	case "license":
		return source.RoleLicense
	}
	if kind == "" {
		return "ollama/layer"
	}
	return "ollama/" + kind
}
