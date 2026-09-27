// Package pinokio reads Pinokio's shared drive: <home>/drive/drives/peers/<id>/
// folders that installed apps are linked to, and <home>/drive/drives.json,
// which records for each app which of its folders routes to which drive
// folder.
package pinokio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const program = "pinokio"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover follows pinokiod's kernel: the home is PINOKIO_HOME or "home" in
// ~/.pinokio/config.json; the operator may name more. The drive is
// <home>/drive; an app's own PINOKIO_DRIVE override lives in that app's
// environment file and is not read here. Every route in drives.json becomes a
// claim by pinokio/<app>.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	type home struct{ dir, rule string }
	var homes []home
	if v := env.Var("PINOKIO_HOME"); v != "" {
		homes = append(homes, home{env.Expand(v), "environment PINOKIO_HOME"})
	}
	cfg := filepath.Join(env.Home, ".pinokio", "config.json")
	if b, err := os.ReadFile(cfg); err == nil {
		var c struct {
			Home string `json:"home"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			d.Errors = append(d.Errors, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: cfg, Detail: err.Error()})
		} else if c.Home != "" {
			homes = append(homes, home{env.Expand(c.Home), "~/.pinokio/config.json home"})
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		d.Errors = append(d.Errors, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: cfg, Detail: err.Error()})
	}
	for _, h := range env.Config.Pinokio {
		homes = append(homes, home{env.Expand(h), "operator configuration pinokio"})
	}

	seen := map[string]bool{}
	for _, h := range homes {
		key := strings.ToLower(filepath.Clean(h.dir))
		if seen[key] {
			continue
		}
		seen[key] = true
		store := program
		if len(d.Stores) > 0 {
			store = fmt.Sprintf("%s#%d", program, len(d.Stores)+1)
		}
		loc := source.NewStore(store, program, filepath.Join(h.dir, "drive"), h.rule+", drive <home>/drive")
		drives := filepath.Join(loc.Locator, "drives.json")
		b, err := os.ReadFile(drives)
		switch {
		case err == nil:
			var routes map[string]map[string]string
			if err := json.Unmarshal(b, &routes); err != nil {
				loc.Errors = append(loc.Errors, content.StoreError{Kind: source.ErrorMalformedIndex, Locator: "drives.json", Detail: err.Error()})
				break
			}
			var uris []string
			for u := range routes {
				uris = append(uris, u)
			}
			sort.Strings(uris)
			for _, u := range uris {
				var names []string
				for r := range routes[u] {
					names = append(names, r)
				}
				sort.Strings(names)
				for _, r := range names {
					d.Claims = append(d.Claims, source.Claim{
						Holder:  AppHolder(u),
						Purpose: source.PurposeInstalled,
						Path:    filepath.Clean(env.Expand(routes[u][r])), Store: store,
						Basis: content.Basis{Index: drives, Key: u, Role: r},
					})
				}
			}
		case !errors.Is(err, fs.ErrNotExist):
			loc.Errors = append(loc.Errors, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: "drives.json", Detail: err.Error()})
		}
		d.Stores = append(d.Stores, loc)
	}
	return d
}

// AppHolder is the holder a Pinokio app is: the app's name in lower case is
// the program, and the app's git URI under Pinokio is the installation the
// source knows it by.
func AppHolder(uri string) source.Holder {
	return source.Holder{Program: strings.ToLower(appName(uri)), Instance: program + ":" + uri}
}

// appName is the last path segment of an app's git URI without .git.
func appName(uri string) string {
	u := strings.TrimSuffix(strings.TrimRight(uri, "/"), ".git")
	if i := strings.LastIndexAny(u, "/\\"); i >= 0 {
		u = u[i+1:]
	}
	if u == "" {
		return uri
	}
	return u
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	return tree.Observe(loc, env, tree.Options{Scheme: program})
}
