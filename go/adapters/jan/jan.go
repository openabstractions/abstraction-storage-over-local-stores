// Package jan reads Jan's model stores: <data>/llamacpp/models and
// <data>/mlx/models, each holding model.yml files at any depth (Jan v0.8.6
// writes llamacpp/models/<org>/<repo>/model.yml) naming model_path, for
// vision models mmproj_path, and model_sha256, Jan's own digest of the
// weights file.
package jan

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	digestfmt "github.com/openabstractions/abstraction-storage/go/digest"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/indexyaml"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

const program = "jan"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover: Jan's data folder is data_folder in jan.ai.app/settings.json under
// the per-user configuration directory when set; otherwise Jan/data under the
// application data directory (%APPDATA% on Windows, ~/Library/Application
// Support on macOS, ~/.local/share on Linux). The store is
// <data>/llamacpp/models. model.yml files naming weights outside the store
// become claims.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	var data, rule string
	switch env.GOOS {
	case "windows", "darwin":
		data = filepath.Join(env.RoamingConfig(), "Jan", "data")
	default:
		data = filepath.Join(env.LocalData(), "Jan", "data")
	}
	rule = "default data folder Jan/data"
	var errs []content.StoreError
	settings := filepath.Join(env.RoamingConfig(), "jan.ai.app", "settings.json")
	b, err := os.ReadFile(settings)
	switch {
	case err == nil:
		var s struct {
			DataFolder string `json:"data_folder"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: settings, Detail: err.Error()})
		} else if s.DataFolder != "" {
			data, rule = env.Expand(s.DataFolder), "jan.ai.app settings.json data_folder"
		}
	case !errors.Is(err, fs.ErrNotExist):
		errs = append(errs, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: settings, Detail: err.Error()})
	}
	loc := source.NewStore("jan", program, filepath.Join(data, "llamacpp", "models"), rule)
	loc.Errors = errs
	d.Stores = append(d.Stores, loc)
	// The MLX store exists on Apple silicon installs only; it is reported
	// where Jan made it.
	if mlx := filepath.Join(data, "mlx", "models"); source.IsDir(mlx) {
		d.Stores = append(d.Stores, source.NewStore("jan-mlx", program, mlx, rule+" mlx/models"))
	}

	for _, s := range d.Stores {
		if !s.Present {
			continue
		}
		//unchecked: an unreadable directory under the models folder just yields no claims; Observe reports it
		files, _ := source.Walk(s.Locator)
		for _, id := range modelDirs(files) {
			index := filepath.Join(s.Locator, filepath.FromSlash(id), "model.yml")
			m, err := readModel(index)
			if err != nil || m == nil {
				continue
			}
			for _, p := range []struct{ key, value string }{{"model_path", m.ModelPath}, {"mmproj_path", m.MmprojPath}} {
				if p.value == "" {
					continue
				}
				abs := resolvePath(data, p.value)
				// A path identity could not place makes no claim.
				if _, inside, err := source.Within(s.Locator, abs); err == nil && !inside {
					d.Claims = append(d.Claims, source.Claim{Holder: source.Holder{Program: program}, Purpose: source.PurposeInstalled, Path: abs, Store: "jan-external", Basis: content.Basis{Index: index, Key: id, Role: p.key}})
				}
			}
		}
	}
	return d
}

// modelDirs are the directories, relative and slash-separated, that hold a
// model.yml at any depth, sorted.
func modelDirs(files []source.File) []string {
	var ids []string
	for _, f := range files {
		if f.Link || path.Base(f.Rel) != "model.yml" {
			continue
		}
		if dir := path.Dir(f.Rel); dir != "." {
			ids = append(ids, dir)
		}
	}
	sort.Strings(ids)
	return ids
}

type model struct {
	ModelPath   string
	MmprojPath  string
	Name        string
	ModelSHA256 string
	// BadDigest is set when model_sha256 is not 64 hex; the model keeps its
	// place and has no digest.
	BadDigest error
}

func readModel(p string) (*model, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := indexyaml.Parse(b)
	if err != nil {
		return nil, malformed{err}
	}
	if doc.Kind != indexyaml.Map {
		return nil, malformed{fmt.Errorf("top level is not a mapping")}
	}
	var m model
	for _, f := range []struct {
		key string
		dst *string
	}{{"model_path", &m.ModelPath}, {"mmproj_path", &m.MmprojPath}, {"name", &m.Name}, {"model_sha256", &m.ModelSHA256}} {
		v := doc.Get(f.key)
		if v == nil || v.Kind == indexyaml.Null {
			continue
		}
		s, ok := v.String()
		if !ok {
			return nil, malformed{fmt.Errorf("%s is not a string", f.key)}
		}
		*f.dst = s
	}
	if m.ModelPath == "" {
		return nil, malformed{fmt.Errorf("no model_path")}
	}
	if m.ModelSHA256 != "" {
		// Decoding and re-encoding spells the digest in lowercase hex.
		raw, err := hex.DecodeString(strings.TrimPrefix(m.ModelSHA256, "sha256:"))
		if err != nil || len(raw) != 32 {
			m.BadDigest = fmt.Errorf("model_sha256 %q is not 64 hex", m.ModelSHA256)
			m.ModelSHA256 = ""
		} else {
			m.ModelSHA256 = digestfmt.Hex(raw)
		}
	}
	return &m, nil
}

type malformed struct{ error }

// wantDigest is the digest a dangling reference expects: model_sha256 for
// the model file, none for any other.
func wantDigest(key string, m *model) string {
	if key == "model_path" {
		return m.ModelSHA256
	}
	return ""
}

// resolvePath applies Jan's rule: model_path is relative to the data folder
// unless absolute.
func resolvePath(data, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(data, filepath.FromSlash(p))
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	if strings.HasPrefix(loc.Name, "jan-external") {
		// A directory a model.yml names outside the store: Jan holds only
		// what that model.yml names, through the claim.
		return tree.Observe(loc, env, tree.Options{Scheme: "jan"})
	}
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())
	data := filepath.Dir(filepath.Dir(loc.Locator))
	files, errs := source.Walk(loc.Locator)
	o.Errors = append(o.Errors, errs...)

	objects := map[string]source.File{}
	var weights []string
	for _, f := range files {
		switch {
		case f.Link:
			o.OtherFiles++
		case source.IsPartial(f.Rel):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
		case source.IsWeightFile(filepath.Join(loc.Locator, filepath.FromSlash(f.Rel)), f.Rel):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
			objects[f.Rel] = f
			weights = append(weights, f.Rel)
		default:
			o.OtherFiles++
			o.OtherBytes += f.Size
		}
	}

	named := map[string]bool{}
	for _, id := range modelDirs(files) {
		index := id + "/model.yml"
		m, err := readModel(filepath.Join(loc.Locator, filepath.FromSlash(id), "model.yml"))
		if err != nil {
			var bad malformed
			if errors.As(err, &bad) {
				o.Fail(source.ErrorMalformedIndex, index, bad.error)
			} else {
				o.Fail(source.ErrorUnreadableIndex, index, err)
			}
			continue
		}
		if m.BadDigest != nil {
			o.Fail(source.ErrorMalformedIndex, index, m.BadDigest)
		}
		var entries []content.Entry
		pickled := false
		for _, p := range []struct{ role, key, value string }{
			{source.RoleWeights, "model_path", m.ModelPath},
			{source.RoleProjector, "mmproj_path", m.MmprojPath},
		} {
			if p.value == "" {
				continue
			}
			rel, inside, err := source.Within(loc.Locator, resolvePath(data, p.value))
			if err != nil || !inside {
				continue // claimed at discovery
			}
			rels := []string{rel}
			_, file := objects[rel]
			if !file {
				// An MLX model_path names the model's folder: every weight
				// file under it is the model's.
				rels = nil
				for _, w := range weights {
					if strings.HasPrefix(w, rel+"/") {
						rels = append(rels, w)
					}
				}
			}
			if len(rels) == 0 {
				o.Want(source.Holder{Program: program}, content.Basis{Index: index, Key: id, Role: p.key}, p.value, wantDigest(p.key, m), 0, at)
				continue
			}
			for _, rel := range rels {
				named[rel] = true
				if source.IsPickle(filepath.Join(loc.Locator, filepath.FromSlash(rel)), rel) {
					pickled = true
					continue
				}
				e := source.FileEntry(p.role, rel, objects[rel].Size)
				// model_sha256 is the digest Jan's own index states for the
				// file model_path names (LS-3, index-stated digest); a
				// folder-named MLX model has no per-file digest.
				if p.key == "model_path" && file && m.ModelSHA256 != "" {
					e.Digest, e.Evidence = m.ModelSHA256, content.EvidenceNamed
				}
				entries = append(entries, e)
			}
		}
		// A model whose file is a pickle is listed and never offered (LS-1).
		if len(entries) == 0 || pickled {
			continue
		}
		mf := source.NewManifest(loc.Name, source.KindModel, entries, []content.Name{{Scheme: "jan", Name: id}},
			env.Descriptor(filepath.Join(loc.Locator, filepath.FromSlash(entries[0].Locator)), id, program))
		i := o.AddManifest(mf)
		o.Hold(i, program, source.PurposeInstalled, index, m.Name, at)
	}

	// Weight files no model.yml names: present, and held by nothing Jan records.
	for _, rel := range weights {
		if named[rel] || source.IsPickle(filepath.Join(loc.Locator, filepath.FromSlash(rel)), rel) {
			continue
		}
		f := objects[rel]
		mf := source.NewManifest(loc.Name, source.KindModel, []content.Entry{source.FileEntry(source.RoleOf(rel), rel, f.Size)},
			[]content.Name{{Scheme: "jan", Name: rel}},
			env.Descriptor(filepath.Join(loc.Locator, filepath.FromSlash(rel)), rel, program))
		o.AddManifest(mf)
	}
	return o.Finish()
}
