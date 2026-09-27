// Package lmstudio reads LM Studio's models folder, publisher/repo/file, whose
// tree is its only index.
package lmstudio

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const program = "lmstudio"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover follows lmstudio-js findLMStudioHome: ~/.lmstudio-home-pointer names
// the home; otherwise ~/.cache/lm-studio when it exists; otherwise ~/.lmstudio.
// The models folder is settings.json downloadsFolder in that home, or
// <home>/models.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	var errs []content.StoreError

	home, homeRule := filepath.Join(env.Home, ".lmstudio"), "default home ~/.lmstudio"
	pointer := filepath.Join(env.Home, ".lmstudio-home-pointer")
	b, err := os.ReadFile(pointer)
	switch {
	case err == nil && strings.TrimSpace(string(b)) != "":
		home, homeRule = env.Expand(strings.TrimSpace(string(b))), "home from ~/.lmstudio-home-pointer"
	case err == nil:
		errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: pointer, Detail: "empty home pointer"})
	case !errors.Is(err, fs.ErrNotExist):
		errs = append(errs, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: pointer, Detail: err.Error()})
	default:
		if cache := filepath.Join(env.Home, ".cache", "lm-studio"); source.IsDir(cache) {
			home, homeRule = cache, "home ~/.cache/lm-studio"
		}
	}

	root, rule := filepath.Join(home, "models"), homeRule+", models folder <home>/models"
	settings := filepath.Join(home, "settings.json")
	b, err = os.ReadFile(settings)
	switch {
	case err == nil:
		var s struct {
			DownloadsFolder string `json:"downloadsFolder"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			errs = append(errs, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: settings, Detail: err.Error()})
		} else if s.DownloadsFolder != "" {
			root, rule = env.Expand(s.DownloadsFolder), homeRule+", settings.json downloadsFolder"
		}
	case !errors.Is(err, fs.ErrNotExist):
		errs = append(errs, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: settings, Detail: err.Error()})
	}
	loc := source.NewStore("lmstudio", program, root, rule)
	loc.Errors = errs
	d.Stores = append(d.Stores, loc)
	return d
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	return tree.Observe(loc, env, tree.Options{Scheme: "lmstudio", Holder: program, Purpose: source.PurposeInstalled, Index: "models folder tree"})
}
