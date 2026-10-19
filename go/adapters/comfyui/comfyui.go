// Package comfyui reads ComfyUI's models tree and the extra_model_paths.yaml
// that points ComfyUI at folders outside it.
package comfyui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"

	identity "github.com/openabstractions/abstraction-identity"
)

const program = "comfyui"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover: ComfyUI has no fixed install location. Installations come from the
// operator's configuration and from the AMD AI bundle's default path
// (%LOCALAPPDATA%\AMD\AI_Bundle\ComfyUI\ComfyUI, where §3 measured it). Each
// installation's store is <install>/models. Its extra_model_paths.yaml, and
// ComfyUI Desktop's %APPDATA%\ComfyUI\extra_models_config.yaml, become claims:
// ComfyUI uses the files under every folder they list.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	type install struct{ dir, rule string }
	var installs []install
	for _, dir := range env.Config.ComfyUI {
		installs = append(installs, install{env.Expand(dir), "operator configuration comfyui"})
	}
	if env.GOOS == "windows" {
		installs = append(installs, install{filepath.Join(env.LocalData(), "AMD", "AI_Bundle", "ComfyUI", "ComfyUI"), "default location of the AMD AI bundle"})
	}
	seen := map[string]bool{}
	n := 0
	for _, in := range installs {
		// One folder named twice: keyed by its final path when it exists
		// (identity.PlaceKey), else by its cleaned spelling.
		key, err := identity.PlaceKey(in.dir)
		if err != nil {
			key = filepath.Clean(in.dir)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		store := "comfyui"
		if n > 0 {
			store = fmt.Sprintf("comfyui#%d", n+1)
		}
		n++
		loc := source.NewStore(store, program, filepath.Join(in.dir, "models"), in.rule)
		claims, errs := readExtraPaths(env, filepath.Join(in.dir, "extra_model_paths.yaml"))
		loc.Errors = errs
		d.Claims = append(d.Claims, claims...)
		d.Stores = append(d.Stores, loc)
	}
	desktop := filepath.Join(env.RoamingConfig(), "ComfyUI", "extra_models_config.yaml")
	claims, errs := readExtraPaths(env, desktop)
	d.Claims = append(d.Claims, claims...)
	d.Errors = append(d.Errors, errs...)
	return d
}

// readExtraPaths applies ComfyUI's utils/extra_config.py: each top-level
// section may set base_path (expanded, relative to the yaml's directory) and
// is_default; every other key is a model folder type whose value is one path
// or several separated by newlines, relative to base_path.
func readExtraPaths(env source.Env, file string) ([]source.Claim, []content.StoreError) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []content.StoreError{{Kind: source.ErrorUnreadableIndex, Locator: file, Detail: err.Error()}}
	}
	var doc map[string]map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, []content.StoreError{{Kind: source.ErrorMalformedIndex, Locator: file, Detail: err.Error()}}
	}
	dir := filepath.Dir(file)
	var sections []string
	for s := range doc {
		sections = append(sections, s)
	}
	sort.Strings(sections)
	var claims []source.Claim
	var errs []content.StoreError
	for _, section := range sections {
		conf := doc[section]
		if conf == nil {
			continue
		}
		base := ""
		if v, ok := conf["base_path"]; ok {
			s, ok := v.(string)
			if !ok {
				errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: file, Detail: section + ".base_path is not a string"})
				continue
			}
			base = env.Expand(s)
			if !filepath.IsAbs(base) {
				base = filepath.Join(dir, base)
			}
			base = filepath.Clean(base)
		}
		var types []string
		for k := range conf {
			if k != "base_path" && k != "is_default" {
				types = append(types, k)
			}
		}
		sort.Strings(types)
		for _, typ := range types {
			if typ == "custom_nodes" {
				continue // code, not model content
			}
			s, ok := conf[typ].(string)
			if !ok {
				errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: file, Detail: section + "." + typ + " is not a string"})
				continue
			}
			for _, line := range strings.Split(s, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				p := line
				switch {
				case base != "":
					p = filepath.Join(base, line)
				case !filepath.IsAbs(p):
					p = filepath.Join(dir, line)
				}
				claims = append(claims, source.Claim{
					Holder: source.Holder{Program: program}, Purpose: source.PurposeInstalled,
					Path: filepath.Clean(p), Base: base, Store: "comfyui-extra:" + section,
					Basis: content.Basis{Index: file, Key: section + "." + typ},
				})
			}
		}
	}
	return claims, errs
}

// Observe reads an installation's models tree, held by ComfyUI through the
// tree itself. A comfyui-extra store a claim opened (an A1111 tree) is read
// with no holder of its own: ComfyUI holds only what its yaml names there.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	if strings.HasPrefix(loc.Name, "comfyui-extra:") {
		return tree.Observe(loc, env, tree.Options{Scheme: "comfyui"})
	}
	return tree.Observe(loc, env, tree.Options{Scheme: "comfyui", Holder: program, Purpose: source.PurposeInstalled, Index: "models tree"})
}
