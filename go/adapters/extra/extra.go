// Package extra reads directories the operator names as stores.
package extra

import (
	"path/filepath"
	"strings"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return "extra" }

// Discover reads extra_roots from the operator configuration and
// ABSTRACTION_MODEL_ROOTS, the list abstraction-model's Inventory already
// honours: entries separated by the platform list separator, each "store=dir"
// or a bare directory. A holder is reported only where the operator names one.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	used := map[string]bool{}
	name := func(want string) string {
		if want == "" {
			want = "extra"
		}
		n, i := want, 2
		for used[n] {
			n = want + "#" + itoa(i)
			i++
		}
		used[n] = true
		return n
	}
	for _, r := range env.Config.ExtraRoots {
		if r.Path == "" {
			continue
		}
		d.Stores = append(d.Stores, source.NewStore(name(r.Store), r.Holder, env.Expand(r.Path), "operator configuration extra_roots"))
	}
	for _, entry := range filepath.SplitList(env.Var("ABSTRACTION_MODEL_ROOTS")) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		store, dir, named := strings.Cut(entry, "=")
		if !named || dir == "" {
			store, dir = "", entry
		}
		d.Stores = append(d.Stores, source.NewStore(name(store), "", env.Expand(dir), "environment ABSTRACTION_MODEL_ROOTS"))
	}
	return d
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// Observe reads the root as a tree; Store.Program carries the operator's
// holder.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	return tree.Observe(loc, env, tree.Options{Scheme: loc.Name, Holder: loc.Program, Purpose: source.PurposeInstalled, Index: "operator configuration"})
}
