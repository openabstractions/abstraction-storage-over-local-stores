// Package jan reads Jan's llama.cpp model store: <data>/llamacpp/models/<id>/
// with a model.yml naming model_path and, for vision models, mmproj_path.
package jan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
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

	if loc.Present {
		//unchecked: a missing or unreadable models directory just yields no entries below, the normal "nothing found here" case for optional discovery
		dirs, _ := os.ReadDir(loc.Locator)
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			index := filepath.Join(loc.Locator, dir.Name(), "model.yml")
			m, err := readModel(index)
			if err != nil || m == nil {
				continue
			}
			for _, p := range []struct{ key, value string }{{"model_path", m.ModelPath}, {"mmproj_path", m.MmprojPath}} {
				if p.value == "" {
					continue
				}
				abs := resolvePath(data, p.value)
				if _, inside := source.Within(loc.Locator, abs); !inside {
					d.Claims = append(d.Claims, source.Claim{Holder: source.Holder{Program: program}, Purpose: source.PurposeInstalled, Path: abs, Store: "jan-external", Basis: content.Basis{Index: index, Key: dir.Name(), Role: p.key}})
				}
			}
		}
	}
	return d
}

type model struct {
	ModelPath  string `yaml:"model_path"`
	MmprojPath string `yaml:"mmproj_path"`
	Name       string `yaml:"name"`
}

func readModel(p string) (*model, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m model
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, malformed{err}
	}
	if m.ModelPath == "" {
		return nil, malformed{fmt.Errorf("no model_path")}
	}
	return &m, nil
}

type malformed struct{ error }

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
	dirs := map[string]bool{}
	for _, f := range files {
		if dir, rest, ok := strings.Cut(f.Rel, "/"); ok && rest == "model.yml" {
			dirs[dir] = true
		}
	}
	var ids []string
	for id := range dirs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		index := id + "/model.yml"
		m, err := readModel(filepath.Join(loc.Locator, id, "model.yml"))
		if err != nil {
			var bad malformed
			if errors.As(err, &bad) {
				o.Fail(source.ErrorMalformedIndex, index, bad.error)
			} else {
				o.Fail(source.ErrorUnreadableIndex, index, err)
			}
			continue
		}
		var entries []content.Entry
		for _, p := range []struct{ role, key, value string }{
			{source.RoleWeights, "model_path", m.ModelPath},
			{source.RoleProjector, "mmproj_path", m.MmprojPath},
		} {
			if p.value == "" {
				continue
			}
			rel, inside := source.Within(loc.Locator, resolvePath(data, p.value))
			if !inside {
				continue // claimed at discovery
			}
			f, ok := objects[rel]
			if !ok {
				o.Want(source.Holder{Program: program}, content.Basis{Index: index, Key: id, Role: p.key}, p.value, "", 0, at)
				continue
			}
			named[rel] = true
			entries = append(entries, source.FileEntry(p.role, rel, f.Size))
		}
		if len(entries) == 0 {
			continue
		}
		mf := source.NewManifest(loc.Name, source.KindModel, entries, []content.Name{{Scheme: "jan", Name: id}},
			env.Descriptor(filepath.Join(loc.Locator, filepath.FromSlash(entries[0].Locator)), id, program))
		i := o.AddManifest(mf)
		o.Hold(i, program, source.PurposeInstalled, index, m.Name, at)
	}

	// Weight files no model.yml names: present, and held by nothing Jan records.
	for _, rel := range weights {
		if named[rel] {
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
