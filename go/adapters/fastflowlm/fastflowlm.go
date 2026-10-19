// Package fastflowlm reads FastFlowLM's NPU model store: one directory per
// model holding .q4nx weights, tokenizer, configuration and chat template. The
// directory is the unit, so it is one manifest.
package fastflowlm

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	identity "github.com/openabstractions/abstraction-identity"
)

const program = "fastflowlm"

var q4nx = []string{".q4nx"}

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover follows FastFlowLM's README: models live in ~/.flm/models on
// Windows (the installer can choose another base folder, which only the
// operator can name) and ~/.config/flm/models on Linux; FLM_MODEL_PATH
// overrides the location.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	add := func(root, rule string) {
		for _, l := range d.Stores {
			// One store found twice: by file identity when it exists, else
			// by its exact spelling.
			if same, err := identity.SameFile(l.Locator, root); err == nil && same || err != nil && filepath.Clean(l.Locator) == filepath.Clean(root) {
				return
			}
		}
		store := "fastflowlm"
		if len(d.Stores) > 0 {
			store = fmt.Sprintf("fastflowlm#%d", len(d.Stores)+1)
		}
		d.Stores = append(d.Stores, source.NewStore(store, program, root, rule))
	}
	switch {
	case env.Var("FLM_MODEL_PATH") != "":
		add(env.Expand(env.Var("FLM_MODEL_PATH")), "environment FLM_MODEL_PATH")
	case env.GOOS == "windows":
		add(filepath.Join(env.Home, ".flm", "models"), "default location ~/.flm/models")
	case env.GOOS == "linux":
		add(filepath.Join(env.Home, ".config", "flm", "models"), "default location ~/.config/flm/models")
	}
	for _, p := range env.Config.FastFlowLM {
		add(env.Expand(p), "operator configuration fastflowlm")
	}
	return d
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())
	files, errs := source.Walk(loc.Locator)
	o.Errors = append(o.Errors, errs...)
	groups := map[string][]source.File{}
	var dirs []string
	for _, f := range files {
		dir, _, nested := strings.Cut(f.Rel, "/")
		if !nested || f.Link {
			o.OtherFiles++
			o.OtherBytes += f.Size
			continue
		}
		if _, ok := groups[dir]; !ok {
			dirs = append(dirs, dir)
		}
		groups[dir] = append(groups[dir], f)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		group := groups[dir]
		hasWeights := false
		for _, f := range group {
			if !source.IsPartial(f.Rel) && source.IsWeights(f.Rel, q4nx...) && !pickle(loc, f) {
				hasWeights = true
			}
		}
		var entries []content.Entry
		for _, f := range group {
			switch {
			case source.IsPartial(f.Rel), hasWeights && pickle(loc, f):
				// A pickle is listed and never an entry (LS-1).
				o.AddObject(f.Rel, f, "", content.EvidenceNone)
			case hasWeights:
				o.AddObject(f.Rel, f, "", content.EvidenceNone)
				role := source.RoleOf(f.Rel, q4nx...)
				if role == "" {
					role = "fastflowlm/file"
				}
				entries = append(entries, source.FileEntry(role, f.Rel, f.Size))
			default:
				o.OtherFiles++
				o.OtherBytes += f.Size
			}
		}
		if !hasWeights {
			continue
		}
		m := source.NewManifest(loc.Name, source.KindModel, entries, []content.Name{{Scheme: "flm", Name: dir}}, source.Named(dir, "q4nx", program).Map())
		i := o.AddManifest(m)
		o.Hold(i, program, source.PurposeInstalled, dir+"/", dir, at)
	}
	return o.Finish()
}

// pickle is a weight file in the store OA never loads (LS-1): it is listed
// and is never an entry of a model directory's manifest.
func pickle(loc content.Store, f source.File) bool {
	return source.IsWeights(f.Rel, q4nx...) && source.IsPickle(filepath.Join(loc.Locator, filepath.FromSlash(f.Rel)), f.Rel)
}
