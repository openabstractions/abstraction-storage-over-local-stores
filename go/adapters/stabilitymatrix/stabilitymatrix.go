// Package stabilitymatrix reads Stability Matrix's shared ModelsDirectory and
// the packages its settings say share it.
package stabilitymatrix

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"

	identity "github.com/openabstractions/abstraction-identity"
)

const program = "stabilitymatrix"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover follows StabilityMatrix.Core SettingsManager: the library is
// LibraryPath in <ApplicationData>/StabilityMatrix/library.json, or a portable
// Data directory the operator names. settings.json in the library holds
// ModelDirectoryOverride (else <library>/Models) and InstalledPackages; each
// package whose PreferredSharedFolderMethod is not None claims the models
// directory.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	type library struct{ dir, rule string }
	var libs []library
	libraryJSON := filepath.Join(env.DotnetAppData(), "StabilityMatrix", "library.json")
	if b, err := os.ReadFile(libraryJSON); err == nil {
		var l struct {
			LibraryPath string `json:"LibraryPath"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			d.Errors = append(d.Errors, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: libraryJSON, Detail: err.Error()})
		} else if l.LibraryPath != "" {
			libs = append(libs, library{env.Expand(l.LibraryPath), "library.json LibraryPath"})
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		d.Errors = append(d.Errors, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: libraryJSON, Detail: err.Error()})
	}
	for _, dir := range env.Config.StabilityMatrix {
		libs = append(libs, library{env.Expand(dir), "operator configuration stability_matrix"})
	}

	seen := map[string]bool{}
	for _, lib := range libs {
		// One folder named twice: keyed by its final path when it exists
		// (identity.PlaceKey), else by its cleaned spelling.
		key, err := identity.PlaceKey(lib.dir)
		if err != nil {
			key = filepath.Clean(lib.dir)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		store := program
		if len(d.Stores) > 0 {
			store = fmt.Sprintf("%s#%d", program, len(d.Stores)+1)
		}
		models, rule := filepath.Join(lib.dir, "Models"), lib.rule+", ModelsDirectory <library>/Models"
		settingsPath := filepath.Join(lib.dir, "settings.json")
		var errs []content.StoreError
		var claims []source.Claim
		b, err := os.ReadFile(settingsPath)
		switch {
		case err == nil:
			var s settings
			if err := json.Unmarshal(b, &s); err != nil {
				errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: settingsPath, Detail: err.Error()})
				break
			}
			if s.ModelDirectoryOverride != "" {
				models, rule = env.Expand(s.ModelDirectoryOverride), lib.rule+", settings.json ModelDirectoryOverride"
			}
			for _, p := range s.InstalledPackages {
				method, err := methodOf(p.PreferredSharedFolderMethod)
				if err != nil {
					errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: settingsPath, Detail: err.Error()})
					continue
				}
				if method == "None" {
					continue
				}
				name := p.PackageName
				if name == "" {
					name = p.DisplayName
				}
				claims = append(claims, source.Claim{
					Holder:  PackageHolder(name),
					Purpose: source.PurposeInstalled,
					Store:   store,
					Basis:   content.Basis{Index: settingsPath, Key: "InstalledPackages " + p.DisplayName, Role: method},
				})
			}
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: settingsPath, Detail: err.Error()})
		}
		loc := source.NewStore(store, program, models, rule)
		loc.Errors = errs
		for i := range claims {
			claims[i].Path = loc.Locator
		}
		d.Stores = append(d.Stores, loc)
		d.Claims = append(d.Claims, claims...)
	}
	return d
}

// PackageHolder is the holder a Stability Matrix package is: the package name
// in lower case is the program (ComfyUI is comfyui, as a standalone ComfyUI),
// and the package under Stability Matrix is the installation.
func PackageHolder(name string) source.Holder {
	return source.Holder{Program: strings.ToLower(name), Instance: program + ":" + name}
}

type settings struct {
	ModelDirectoryOverride string `json:"ModelDirectoryOverride"`
	InstalledPackages      []struct {
		DisplayName                 string          `json:"DisplayName"`
		PackageName                 string          `json:"PackageName"`
		PreferredSharedFolderMethod json.RawMessage `json:"PreferredSharedFolderMethod"`
	} `json:"InstalledPackages"`
}

// methodOf reads SharedFolderMethod {Symlink, Configuration, None} as a name or
// its ordinal. An absent value is the package default, which shares.
func methodOf(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "Default", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		switch n {
		case 0:
			return "Symlink", nil
		case 1:
			return "Configuration", nil
		case 2:
			return "None", nil
		}
	}
	return "", fmt.Errorf("PreferredSharedFolderMethod %s", string(raw))
}

// Observe reads the models directory as a tree. A model downloaded through
// Stability Matrix's browser has a <name>.cm-info.json record beside it, which
// is Stability Matrix's own evidence that it fetched the file.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	o := tree.Observe(loc, env, tree.Options{Scheme: program})
	at := source.Stamp(env.At())
	for i, m := range o.Manifests {
		for _, l := range source.EntryLocators(m.Manifest) {
			info := strings.TrimSuffix(l, filepath.Ext(l)) + ".cm-info.json"
			if source.IsFile(filepath.Join(loc.Locator, filepath.FromSlash(info))) {
				o.Hold(i, program, source.PurposeFetched, info, "cm-info", at)
			}
		}
	}
	return o.Finish()
}
