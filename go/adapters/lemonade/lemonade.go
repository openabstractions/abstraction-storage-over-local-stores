// Package lemonade reads what Lemonade holds. Lemonade keeps its state in its
// configuration directory (config.json, user_models.json; Lemonade moved them
// there from its cache directory in v2026.42, deleting the old copy), shares
// the Hugging Face cache, writes .lemonade_registry.json into each repository
// it uses, downloads ModelScope repositories into the same cache as
// modelscope--models--<org>--<repo>, and may name a models_dir of its own and
// an extra_models_dir of loose files. Its indexes name
// "<org>/<repo>:<file or quant>" selections per role.
package lemonade

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

const program = "lemonade"

// Store names: lemonade-cache is the Hugging Face cache Lemonade shares, where it
// owns only the ModelScope repositories; lemonade-models is a models_dir of
// Lemonade's own, where it owns every repository; lemonade-extra is
// extra_models_dir.
const (
	storeShared = "lemonade-cache"
	storeOwn    = "lemonade-models"
	storeExtra  = "lemonade-extra"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// configDirs are the directories Lemonade's state may be in, in the order
// Lemonade itself reads them: the configuration directory ($XDG_CONFIG_HOME/
// lemonade, else ~/.config/lemonade), then the cache directory it used before
// (LEMONADE_CACHE_DIR, $XDG_CACHE_HOME/lemonade, ~/.cache/lemonade), then the
// system service's directories on Linux (/var/lib/lemonade) and macOS
// (/Library/Application Support/lemonade) where they exist.
func configDirs(env source.Env) []string {
	var out []string
	if c := filepath.Join(env.Expand(env.ConfigHome()), "lemonade"); c != filepath.Join(env.Home, ".config", "lemonade") {
		out = append(out, c)
	}
	out = append(out, filepath.Join(env.Home, ".config", "lemonade"))
	if x := env.Var("LEMONADE_CACHE_DIR"); x != "" {
		out = append(out, env.Expand(x))
	}
	if cache, set := huggingface.CacheHome(env); set {
		out = append(out, filepath.Join(cache, "lemonade"))
	}
	out = append(out, filepath.Join(env.Home, ".cache", "lemonade"))
	var service string
	switch env.GOOS {
	case "linux":
		service = "/var/lib/lemonade"
	case "darwin":
		service = "/Library/Application Support/lemonade"
	}
	if service != "" && source.IsDir(service) {
		out = append(out, filepath.Join(service, ".config", "lemonade"), filepath.Join(service, ".cache", "lemonade"))
	}
	return out
}

// firstFile reads the first of dirs/name that exists.
func firstFile(dirs []string, name string) (string, []byte, error) {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, nil, unreadable{err}
		}
		return p, b, nil
	}
	return "", nil, nil
}

type config struct {
	ModelsDir      string `json:"models_dir"`
	ExtraModelsDir string `json:"extra_models_dir"`
}

type unreadable struct{ error }

func kindOf(err error) content.StoreErrorKind {
	var u unreadable
	if errors.As(err, &u) {
		return source.ErrorUnreadableIndex
	}
	return source.ErrorMalformedIndex
}

func readConfig(env source.Env) (string, config, error) {
	var cfg config
	p, b, err := firstFile(configDirs(env), "config.json")
	if err != nil || b == nil {
		return p, cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return p, config{}, err
	}
	return p, cfg, nil
}

// Discover reports the cache Lemonade reads models from and its
// extra_models_dir. In the shared Hugging Face cache, Lemonade's selections of
// huggingface repositories become claims, so its holds land on the manifests
// the huggingface adapter reads there.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	cfgPath, cfg, err := readConfig(env)
	if err != nil {
		d.Errors = append(d.Errors, content.StoreError{Kind: kindOf(err), Locator: cfgPath, Detail: err.Error()})
	}
	hfRoot, hfRule, hfDef := huggingface.HubRoot(env)
	hub := source.NewStore(storeShared, program, hfRoot, "the Hugging Face cache by "+hfRule)
	if md := strings.TrimSpace(cfg.ModelsDir); md != "" && !strings.EqualFold(md, "auto") {
		p := env.Expand(md)
		if !huggingface.SameFolder(p, hfRoot) && !huggingface.SameFolder(p, hfDef) {
			hub = source.NewStore(storeOwn, program, p, "lemonade "+cfgPath+" models_dir")
		}
	}
	d.Stores = append(d.Stores, hub)
	if extra := strings.TrimSpace(cfg.ExtraModelsDir); extra != "" {
		d.Stores = append(d.Stores, source.NewStore(storeExtra, program, env.Expand(extra), "lemonade "+cfgPath+" extra_models_dir"))
	}

	if hub.Name == storeShared && hub.Present {
		scratch := source.Begin(hub)
		at := source.Stamp(env.At())
		repos, owned, sels := read(&scratch, &scratch, env, hub.Locator, false, at)
		for _, s := range sels {
			r, arts := resolve(repos, s)
			if r == nil || owned[r] {
				continue
			}
			for _, a := range arts {
				if len(a.Locators) == 0 {
					continue
				}
				d.Claims = append(d.Claims, source.Claim{
					Holder: source.Holder{Program: program}, Purpose: source.PurposeInstalled,
					Path: filepath.Join(hub.Locator, filepath.FromSlash(a.Locators[0])), Basis: s.basis(),
				})
			}
		}
	}
	return d
}

// Observe reads the repositories Lemonade owns in a cache store with the
// Hugging Face cache reader, attaches its holds to them, and records every
// selection that names nothing present.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	if loc.Name == storeExtra || strings.HasPrefix(loc.Name, storeExtra+"#") {
		return tree.Observe(loc, env, tree.Options{Scheme: program, Holder: program, Purpose: source.PurposeInstalled, Index: "lemonade config.json extra_models_dir"})
	}
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())
	own := strings.HasPrefix(loc.Name, storeOwn)
	// Repositories the huggingface adapter observes are read into a scratch
	// observation, only to match selections; their holds travel as claims.
	scratch := source.Begin(loc)
	repos, owned, sels := read(&o, &scratch, env, loc.Locator, own, at)
	for _, s := range sels {
		r, arts := resolve(repos, s)
		switch {
		case len(arts) == 0:
			o.Want(source.Holder{Program: program}, s.basis(), s.repo+":"+s.spec, "", 0, at)
		case owned[r]:
			for _, a := range arts {
				o.HoldBy(a.Manifest, source.Holder{Program: program}, source.PurposeInstalled, s.basis(), at)
			}
		}
	}
	return o.Finish()
}

// read reads every repository under root, the ones Lemonade owns into o and
// the rest into scratch, and the selections Lemonade's indexes name.
func read(o, scratch *source.StoreObservation, env source.Env, root string, own bool, at string) (map[string]*huggingface.Repo, map[*huggingface.Repo]bool, []selection) {
	repos := map[string]*huggingface.Repo{}
	owned := map[*huggingface.Repo]bool{}
	var sels []selection
	dirs, err := os.ReadDir(root)
	if err != nil {
		o.Fail(source.ErrorUnreadableTree, ".", err)
		return repos, owned, nil
	}
	// ModelScope repositories sort first; a Hugging Face repository of the
	// same id replaces one in the map, so a selection names it.
	sort.Slice(dirs, func(i, j int) bool {
		mi, mj := strings.HasPrefix(dirs[i].Name(), huggingface.ModelScopePrefix), strings.HasPrefix(dirs[j].Name(), huggingface.ModelScopePrefix)
		if mi != mj {
			return mi
		}
		return dirs[i].Name() < dirs[j].Name()
	})
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		var r *huggingface.Repo
		switch {
		case strings.HasPrefix(name, huggingface.ModelScopePrefix), own && strings.HasPrefix(name, huggingface.RepoPrefix):
			r = huggingface.ReadRepo(o, env, root, name, program, at)
			owned[r] = true
		case strings.HasPrefix(name, huggingface.RepoPrefix):
			r = huggingface.ReadRepo(scratch, env, root, name, program, at)
		default:
			continue
		}
		repos[strings.ToLower(r.ID)] = r
		sels = append(sels, readRegistry(o, root, r)...)
	}
	sels = append(sels, readUserModels(o, env)...)
	return repos, owned, sels
}

// selection is one "<role>=<org>/<repo>:<spec>" a Lemonade index names.
type selection struct {
	index string
	key   string
	role  string
	repo  string
	spec  string
	rev   string
}

func (s selection) basis() content.Basis {
	return content.Basis{Index: s.index, Key: s.key, Role: s.role}
}

type registry struct {
	ProcessedModels map[string]struct {
		Selection  string `json:"selection"`
		SnapshotID string `json:"snapshot_id"`
	} `json:"processed_models"`
	SnapshotID string `json:"snapshot_id"`
}

func readRegistry(o *source.StoreObservation, root string, r *huggingface.Repo) []selection {
	index := r.Dir + "/.lemonade_registry.json"
	b, err := os.ReadFile(filepath.Join(root, r.Dir, ".lemonade_registry.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		o.Fail(source.ErrorUnreadableIndex, index, err)
		return nil
	}
	var reg registry
	if err := json.Unmarshal(b, &reg); err != nil {
		o.Fail(source.ErrorMalformedIndex, index, err)
		return nil
	}
	var names []string
	for n := range reg.ProcessedModels {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []selection
	for _, n := range names {
		pm := reg.ProcessedModels[n]
		rev := pm.SnapshotID
		if rev == "" {
			rev = reg.SnapshotID
		}
		for _, line := range strings.Split(pm.Selection, "\n") {
			role, ref, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue // the first line names the recipe
			}
			repoID, spec, ok := strings.Cut(ref, ":")
			if !ok {
				o.Fail(source.ErrorMalformedIndex, index, fmt.Errorf("%s: selection %q has no <repo>:<file>", n, line))
				continue
			}
			s := selection{index: index, key: n, role: role, repo: repoID, spec: spec}
			if strings.EqualFold(repoID, r.ID) {
				s.rev = rev
			}
			out = append(out, s)
		}
	}
	return out
}

type userModel struct {
	Checkpoint  string            `json:"checkpoint"`
	Checkpoints map[string]string `json:"checkpoints"`
}

// readUserModels reads user_models.json from the first directory configDirs
// names that has one.
func readUserModels(o *source.StoreObservation, env source.Env) []selection {
	p, b, err := firstFile(configDirs(env), "user_models.json")
	if err != nil {
		o.Fail(kindOf(err), p, err)
		return nil
	}
	if b == nil {
		return nil
	}
	var models map[string]userModel
	if err := json.Unmarshal(b, &models); err != nil {
		o.Fail(source.ErrorMalformedIndex, p, err)
		return nil
	}
	var names []string
	for n := range models {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []selection
	for _, n := range names {
		m := models[n]
		refs := map[string]string{}
		for role, ref := range m.Checkpoints {
			refs[role] = ref
		}
		if m.Checkpoint != "" {
			refs["main"] = m.Checkpoint
		}
		var roles []string
		for role := range refs {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			repoID, spec, ok := strings.Cut(refs[role], ":")
			if !ok {
				o.Fail(source.ErrorMalformedIndex, p, fmt.Errorf("%s: checkpoint %q has no <repo>:<file>", n, refs[role]))
				continue
			}
			out = append(out, selection{index: p, key: n, role: role, repo: repoID, spec: spec})
		}
	}
	return out
}

// resolve finds the repository and the artifacts a selection names. A
// selection with no revision applies to the revisions a ref names, or to
// every revision when none does.
func resolve(repos map[string]*huggingface.Repo, s selection) (*huggingface.Repo, []huggingface.Artifact) {
	r, ok := repos[strings.ToLower(s.repo)]
	if !ok {
		return nil, nil
	}
	var revs []string
	if s.rev != "" {
		revs = []string{s.rev}
	} else {
		for rev := range r.Refs {
			if _, ok := r.Revs[rev]; ok {
				revs = append(revs, rev)
			}
		}
		if len(revs) == 0 {
			for rev := range r.Revs {
				revs = append(revs, rev)
			}
		}
	}
	sort.Strings(revs)
	var out []huggingface.Artifact
	for _, rev := range revs {
		out = append(out, match(r.Revs[rev], s.spec)...)
	}
	return r, out
}

// match finds the artifacts a Lemonade spec names: a file path or artifact
// name spelled exactly, else a quantization variant that is a folder name or
// the suffix of a file name in any case (Q4_K_M names Tiny-Q4_K_M.gguf). A
// projector file matches a variant only when the variant says so. Lemonade
// compares a full file name exactly and a variant ignoring case
// (src/cpp/server/model_manager.cpp, identify_gguf_models cases 1, 3 and 4).
func match(arts []huggingface.Artifact, spec string) []huggingface.Artifact {
	exact := strings.TrimSpace(spec)
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return nil
	}
	for _, a := range arts {
		if a.Key == exact {
			return []huggingface.Artifact{a}
		}
		for _, f := range a.Files {
			if f == exact {
				return []huggingface.Artifact{a}
			}
		}
	}
	var out []huggingface.Artifact
	for _, a := range arts {
		for _, f := range a.Files {
			lf := strings.ToLower(f)
			if first, _, ok := strings.Cut(lf, "/"); ok && first == s {
				out = append(out, a)
				break
			}
			base := strings.ToLower(huggingface.Shard.ReplaceAllString(path.Base(f), "$1"))
			base = strings.TrimSuffix(base, path.Ext(base))
			if strings.Contains(base, "mmproj") && !strings.Contains(s, "mmproj") {
				continue
			}
			if base == s || strings.HasSuffix(base, "-"+s) || strings.HasSuffix(base, "_"+s) || strings.HasSuffix(base, "."+s) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}
