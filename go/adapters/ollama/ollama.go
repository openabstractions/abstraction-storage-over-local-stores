// Package ollama reads Ollama's model store: manifests/<host>/<namespace>/
// <model>/<tag> (an OCI-shaped manifest per tag) over blobs/sha256-<hex>.
package ollama

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
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

type manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Config        json.RawMessage `json:"config"`
	Layers        json.RawMessage `json:"layers"`
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())

	blobs, errs := source.Walk(filepath.Join(loc.Locator, "blobs"))
	o.Errors = append(o.Errors, prefix(errs, "blobs")...)
	present := map[string]bool{}
	for _, f := range blobs {
		loc := "blobs/" + f.Rel
		if f.Link {
			o.OtherFiles++
			continue
		}
		digest, evidence := "", content.EvidenceNone
		if h, ok := blobDigest(f.Rel); ok && !source.IsPartial(f.Rel) {
			digest, evidence = "sha256:"+h, content.EvidenceNamed
			present[digest] = true
		}
		o.AddObject(loc, f, digest, evidence)
	}

	files, errs := source.Walk(filepath.Join(loc.Locator, "manifests"))
	o.Errors = append(o.Errors, prefix(errs, "manifests")...)
	for _, f := range files {
		index := "manifests/" + f.Rel
		if f.Link {
			o.OtherFiles++
			continue
		}
		b, err := os.ReadFile(filepath.Join(loc.Locator, filepath.FromSlash(index)))
		if err != nil {
			o.Fail(source.ErrorUnreadableIndex, index, err)
			continue
		}
		entries, err := parse(b)
		if err != nil {
			o.Fail(source.ErrorMalformedIndex, index, err)
			continue
		}
		name := nameOf(f.Rel)
		descriptor := source.Model{}
		for _, e := range entries {
			if !present[e.Digest] {
				o.Want(source.Holder{Program: program}, content.Basis{Index: index, Key: name, Role: e.Role}, e.Digest, e.Digest, e.Size, at)
			}
			if e.Role != source.RoleWeights {
				continue
			}
			// Ollama's weights layer is a GGUF whatever its media type says,
			// and the blob it names is where the header is.
			descriptor = source.Named(name, "gguf", program)
			if present[e.Digest] {
				if g, ok := env.Header(filepath.Join(loc.Locator, filepath.FromSlash(e.Locator))); ok {
					descriptor = descriptor.WithHeader(g)
				}
			}
		}
		m := source.NewManifest(loc.Name, source.KindModel, entries, []content.Name{{Scheme: "ollama", Name: name}}, descriptor.Map())
		i := o.AddManifest(m)
		o.Hold(i, program, source.PurposeInstalled, index, name, at)
	}
	return o.Finish()
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
	if !isHex(h) {
		return "", false
	}
	return h, true
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

// nameOf turns manifests/<host>/<namespace>/<model>/<tag> into the name Ollama
// shows: registry.ollama.ai is implicit, so library/tiny:1b.
func nameOf(rel string) string {
	dir, tag := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	dir = strings.TrimPrefix(dir, "registry.ollama.ai/")
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
	if len(h) != 64 || !isHex(h) || !strings.HasPrefix(d.Digest, "sha256:") {
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
	case "model":
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
