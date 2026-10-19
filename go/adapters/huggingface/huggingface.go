// Package huggingface reads the Hugging Face hub cache and the two Lemonade
// indexes that name content in it.
//
// Layout (huggingface_hub, "Manage cache"): <hub>/models--<org>--<repo>/ with
// refs/<name> holding a commit, snapshots/<commit>/<path> as links into blobs/
// (or plain files where links are unavailable, as on Windows without Developer
// Mode and on the owner's machine), and blobs/<etag>[.incomplete]. Lemonade
// writes .lemonade_registry.json into each repo it uses and keeps
// user_models.json in its configuration directory; both name
// "<org>/<repo>:<file or quant>" selections per role.
package huggingface

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	identity "github.com/openabstractions/abstraction-identity"
)

const (
	program  = "huggingface"
	lemonade = "lemonade"
	// ambiguousBinExt is ".bin": ambiguous by name alone elsewhere (see
	// source.IsWeightFile), but every file this adapter walks sits inside a
	// repository's snapshots/<rev>/, the Hugging Face cache layout that
	// already settles the question.
	ambiguousBinExt = ".bin"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover applies huggingface_hub's own precedence for the cache in use:
// HF_HUB_CACHE, then the legacy HUGGINGFACE_HUB_CACHE, then HF_HOME/hub, then
// $XDG_CACHE_HOME/huggingface/hub, then ~/.cache/huggingface/hub. A program
// started with another environment may have used the default, so the default
// is also reported when it exists and differs. Lemonade's config.json adds its
// models_dir when not "auto" and its extra_models_dir.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	def := filepath.Join(env.Home, ".cache", "huggingface", "hub")
	var root, rule string
	switch {
	case env.Var("HF_HUB_CACHE") != "":
		root, rule = env.Expand(env.Var("HF_HUB_CACHE")), "environment HF_HUB_CACHE"
	case env.Var("HUGGINGFACE_HUB_CACHE") != "":
		root, rule = env.Expand(env.Var("HUGGINGFACE_HUB_CACHE")), "environment HUGGINGFACE_HUB_CACHE (legacy)"
	case env.Var("HF_HOME") != "":
		root, rule = filepath.Join(env.Expand(env.Var("HF_HOME")), "hub"), "environment HF_HOME"
	case env.Var("XDG_CACHE_HOME") != "":
		root, rule = filepath.Join(env.Expand(env.Var("XDG_CACHE_HOME")), "huggingface", "hub"), "environment XDG_CACHE_HOME"
	default:
		root, rule = def, "default location ~/.cache/huggingface/hub"
	}
	hub := source.NewStore("huggingface", program, root, rule)
	cfgPath, cfg, err := lemonadeConfig(env)
	if err != nil {
		hub.Errors = append(hub.Errors, content.StoreError{Kind: kindOf(err), Locator: cfgPath, Detail: err.Error()})
	}
	d.Stores = append(d.Stores, hub)
	if !fixturePathEqual(root, def) && source.IsDir(def) {
		d.Stores = append(d.Stores, source.NewStore("huggingface:default", program, def, "default location ~/.cache/huggingface/hub"))
	}
	if md := strings.TrimSpace(cfg.ModelsDir); md != "" && !strings.EqualFold(md, "auto") {
		p := env.Expand(md)
		if !fixturePathEqual(p, root) && !fixturePathEqual(p, def) {
			d.Stores = append(d.Stores, source.NewStore("huggingface:lemonade", program, p, "lemonade "+cfgPath+" models_dir"))
		}
	}
	if extra := strings.TrimSpace(cfg.ExtraModelsDir); extra != "" {
		d.Stores = append(d.Stores, source.NewStore("lemonade-extra", lemonade, env.Expand(extra), "lemonade "+cfgPath+" extra_models_dir"))
	}
	return d
}

// fixturePathEqual reports whether a and b name one folder: by file identity
// when both exist, else by their exact cleaned spelling.
func fixturePathEqual(a, b string) bool {
	if same, err := identity.SameFile(a, b); err == nil {
		return same
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

type lemonadeCfg struct {
	ModelsDir      string `json:"models_dir"`
	ExtraModelsDir string `json:"extra_models_dir"`
}

// lemonadeConfig reads ~/.cache/lemonade/config.json, or the older
// ~/.config/lemonade/config.json when the first does not exist.
func lemonadeConfig(env source.Env) (string, lemonadeCfg, error) {
	var cfg lemonadeCfg
	for _, p := range []string{
		filepath.Join(env.Home, ".cache", "lemonade", "config.json"),
		filepath.Join(env.Home, ".config", "lemonade", "config.json"),
	} {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, cfg, unreadable{err}
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return p, lemonadeCfg{}, err
		}
		return p, cfg, nil
	}
	return "", cfg, nil
}

type unreadable struct{ error }

func kindOf(err error) content.StoreErrorKind {
	var u unreadable
	if errors.As(err, &u) {
		return source.ErrorUnreadableIndex
	}
	return source.ErrorMalformedIndex
}

func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	if loc.Name == "lemonade-extra" {
		return tree.Observe(loc, env, tree.Options{Scheme: lemonade, Holder: lemonade, Purpose: source.PurposeInstalled, Index: "lemonade config.json extra_models_dir"})
	}
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	at := source.Stamp(env.At())
	dirs, err := os.ReadDir(loc.Locator)
	if err != nil {
		o.Fail(source.ErrorUnreadableTree, ".", err)
		return o.Finish()
	}
	repos := map[string]*repo{}
	var pending []selection
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "models--") {
			if !d.IsDir() {
				o.OtherFiles++
			}
			continue
		}
		r := readRepo(&o, env, loc.Locator, d.Name(), at)
		repos[strings.ToLower(r.id)] = r
		pending = append(pending, readRegistry(&o, loc.Locator, r)...)
	}
	pending = append(pending, readUserModels(&o, env)...)
	for _, s := range pending {
		resolve(&o, repos, s, at)
	}
	return o.Finish()
}

type file struct {
	rel      string // inside the snapshot
	locator  string // inside the store
	size     int64
	digest   string
	evidence content.Evidence
}

type artifact struct {
	key      string
	files    []string
	manifest int
}

type repo struct {
	dir  string
	id   string
	refs map[string][]string // revision -> ref names
	revs map[string][]artifact
}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var shard = regexp.MustCompile(`-\d{5}-of-\d{5}(\.[A-Za-z0-9]+)$`)

func readRepo(o *source.StoreObservation, env source.Env, root, dir string, at string) *repo {
	r := &repo{dir: dir, id: strings.ReplaceAll(strings.TrimPrefix(dir, "models--"), "--", "/"), refs: map[string][]string{}, revs: map[string][]artifact{}}
	base := filepath.Join(root, dir)

	refs, errs := source.Walk(filepath.Join(base, "refs"))
	o.Errors = append(o.Errors, under(errs, dir+"/refs")...)
	for _, f := range refs {
		index := dir + "/refs/" + f.Rel
		b, err := os.ReadFile(filepath.Join(base, "refs", filepath.FromSlash(f.Rel)))
		if err != nil {
			o.Fail(source.ErrorUnreadableIndex, index, err)
			continue
		}
		rev := strings.TrimSpace(string(b))
		if !hex40.MatchString(rev) {
			o.Fail(source.ErrorMalformedIndex, index, fmt.Errorf("ref content %q is not a 40-hex commit", truncate(rev)))
			continue
		}
		r.refs[rev] = append(r.refs[rev], f.Rel)
	}

	blobs := map[string]file{}
	blobFiles, errs := source.Walk(filepath.Join(base, "blobs"))
	o.Errors = append(o.Errors, under(errs, dir+"/blobs")...)
	for _, f := range blobFiles {
		locator := dir + "/blobs/" + f.Rel
		if f.Link {
			o.OtherFiles++
			continue
		}
		digest, evidence := "", content.EvidenceNone
		name := path.Base(f.Rel)
		if !source.IsPartial(name) {
			switch {
			case hex64.MatchString(name):
				digest, evidence = "sha256:"+name, content.EvidenceNamed
			case hex40.MatchString(name):
				digest, evidence = "gitsha1:"+name, content.EvidenceForeign
			}
		}
		o.AddObject(locator, f, digest, evidence)
		blobs[name] = file{locator: locator, size: f.Size, digest: digest, evidence: evidence}
	}

	snaps, err := os.ReadDir(filepath.Join(base, "snapshots"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		o.Fail(source.ErrorUnreadableTree, dir+"/snapshots", err)
	}
	for _, s := range snaps {
		if !s.IsDir() {
			continue
		}
		rev := s.Name()
		revDir := filepath.Join(base, "snapshots", rev)
		prefix := dir + "/snapshots/" + rev + "/"
		walked, errs := source.Walk(revDir)
		o.Errors = append(o.Errors, under(errs, prefix[:len(prefix)-1])...)
		var weights, others []file
		for _, f := range walked {
			var x file
			if f.Link {
				target, err := os.Readlink(filepath.Join(revDir, filepath.FromSlash(f.Rel)))
				if err != nil {
					o.Fail(source.ErrorUnreadableIndex, prefix+f.Rel, err)
					continue
				}
				b, ok := blobs[filepath.Base(target)]
				if !ok {
					expected := ""
					if name := filepath.Base(target); hex64.MatchString(name) {
						expected = "sha256:" + name
					}
					o.Want(source.Holder{Program: program}, content.Basis{Index: prefix + f.Rel, Key: f.Rel}, filepath.ToSlash(target), expected, 0, at)
					continue
				}
				x = b
				x.rel = f.Rel
			} else {
				// Without the symlink privilege (Windows without Developer
				// Mode, the owner's own machine — model-bridge
				// EXPERIMENT-2026-09-22.md), huggingface_hub copies the
				// blob's bytes into the snapshot instead of linking it. The
				// copy carries no name or metadata pointing back at its
				// blob, so it is matched to the one blob of the same size
				// (and, on a rare size collision, the one whose bytes are
				// identical), giving it the same digest a symlink to that
				// blob would carry.
				x = file{rel: f.Rel, locator: prefix + f.Rel, size: f.Size, evidence: content.EvidenceNone}
				if b, ok := matchBlobBySize(blobs, root, filepath.Join(revDir, filepath.FromSlash(f.Rel)), f.Size); ok {
					x.digest, x.evidence = b.digest, b.evidence
				}
				o.AddObject(x.locator, f, x.digest, x.evidence)
				if source.IsPartial(f.Rel) {
					continue
				}
			}
			// Every file here is inside this repository's
			// snapshots/<rev>/, so a ".bin" is always in the Hugging Face
			// cache layout IsWeightFile treats as weight evidence; passing
			// it as an extra extension here skips the sibling-manifest and
			// header checks a generic tree walk needs.
			if source.IsWeights(f.Rel, ambiguousBinExt) {
				// A pickle (pytorch_model.bin, a .pth) is listed and never
				// a manifest's weights or entry (LS-1).
				if source.IsPickle(filepath.Join(revDir, filepath.FromSlash(f.Rel)), f.Rel) {
					continue
				}
				weights = append(weights, x)
			} else {
				others = append(others, x)
			}
		}

		groups := map[string][]file{}
		var keys []string
		for _, w := range weights {
			k := shard.ReplaceAllString(w.rel, "$1")
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], w)
		}
		sort.Strings(keys)
		held := r.refs[rev]
		name := revisionName(r, rev, len(snaps))
		addHolds := func(i int) {
			for _, name := range held {
				o.Hold(i, program, source.PurposeCache, dir+"/refs/"+name, name, at)
			}
		}
		for _, k := range keys {
			fs := groups[k]
			sort.Slice(fs, func(i, j int) bool { return fs[i].rel < fs[j].rel })
			var entries []content.Entry
			var rels []string
			for _, f := range fs {
				entries = append(entries, entryOf(f, source.RoleOf(f.rel)))
				rels = append(rels, f.rel)
			}
			for _, f := range others {
				entries = append(entries, entryOf(f, roleOrFile(f.rel)))
			}
			descriptor := source.Named(name+":"+k, source.FormatOf(k), program)
			if len(fs) > 0 {
				if g, ok := env.Header(filepath.Join(root, filepath.FromSlash(fs[0].locator))); ok {
					descriptor = descriptor.WithHeader(g)
				}
			}
			// A repository whose only weight file is a projector or VAE is a
			// component of a family, not a family of its own (MODEL-C3): its
			// own name, not the account it was published under, states which.
			if role := componentRoleOf(fs); role != "" {
				descriptor = descriptor.WithComponent(fs[0].rel, role)
			}
			m := source.NewManifest(o.Name, source.KindModel, entries, []content.Name{{Scheme: "hf", Name: name + ":" + k}}, descriptor.Map())
			i := o.AddManifest(m)
			addHolds(i)
			r.revs[rev] = append(r.revs[rev], artifact{key: k, files: rels, manifest: i})
		}
		if len(keys) == 0 && len(others) > 0 {
			var entries []content.Entry
			for _, f := range others {
				entries = append(entries, entryOf(f, roleOrFile(f.rel)))
			}
			short := rev
			if len(short) > 12 {
				short = short[:12]
			}
			m := source.NewManifest(o.Name, source.KindSnapshot, entries, []content.Name{{Scheme: "hf", Name: r.id + "@" + short}}, nil)
			addHolds(o.AddManifest(m))
		}
	}
	return r
}

// matchBlobBySize finds the blob a snapshot copy at fullPath (of the given
// size, no link) was made from: the one blob of that size, or, when several
// share it, the one whose bytes equal the copy's.
func matchBlobBySize(blobs map[string]file, root, fullPath string, size int64) (file, bool) {
	var candidates []file
	for _, b := range blobs {
		if b.size == size {
			candidates = append(candidates, b)
		}
	}
	switch len(candidates) {
	case 0:
		return file{}, false
	case 1:
		return candidates[0], true
	default:
		for _, b := range candidates {
			if filesEqual(fullPath, filepath.Join(root, filepath.FromSlash(b.locator))) {
				return b, true
			}
		}
		return file{}, false
	}
}

// filesEqual compares two files' contents in bounded chunks, never holding
// more than one chunk of either in memory.
func filesEqual(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, erra := fa.Read(ba)
		nb, errb := fb.Read(bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if erra == io.EOF && errb == io.EOF {
			return true
		}
		if erra != nil && !errors.Is(erra, io.EOF) {
			return false
		}
		if errb != nil && !errors.Is(errb, io.EOF) {
			return false
		}
	}
}

// revisionName is the repository part of a manifest name. A name-addressed id
// covers names[0], so two revisions holding a file of one name and size need
// different names. The revision refs/main names, or a repository's only
// revision, is <org>/<repo>, as hf cache ls and Lemonade spell it; a revision
// only other refs name is <org>/<repo>@<ref>; a revision no ref names is
// <org>/<repo>@<first 12 hex of the commit>.
func revisionName(r *repo, rev string, revisions int) string {
	refs := append([]string(nil), r.refs[rev]...)
	sort.Strings(refs)
	for _, ref := range refs {
		if ref == "main" {
			return r.id
		}
	}
	if revisions <= 1 {
		return r.id
	}
	if len(refs) > 0 {
		return r.id + "@" + refs[0]
	}
	short := rev
	if len(short) > 12 {
		short = short[:12]
	}
	return r.id + "@" + short
}

func entryOf(f file, role string) content.Entry {
	e := source.FileEntry(role, f.locator, f.size)
	e.Digest, e.Evidence = f.digest, f.evidence
	if e.Evidence == 0 {
		e.Evidence = content.EvidenceNone
	}
	e.MediaType = source.MediaTypeOf(f.rel)
	return e
}

func roleOrFile(rel string) string {
	if r := source.RoleOf(rel); r != "" {
		return r
	}
	return "huggingface/file"
}

// componentRoleOf answers RoleOf's role when every weight file in a shard
// group names the same component role (projector or VAE), empty otherwise: a
// shard set is one artifact, and its own descriptor states one role or none,
// never a mix a single Role key could not carry.
func componentRoleOf(fs []file) string {
	role := ""
	for _, f := range fs {
		r := source.RoleOf(f.rel)
		if r != source.RoleProjector && r != source.RoleVAE {
			return ""
		}
		if role == "" {
			role = r
		} else if role != r {
			return ""
		}
	}
	return role
}

func under(errs []content.StoreError, dir string) []content.StoreError {
	for i := range errs {
		if errs[i].Locator == "." {
			errs[i].Locator = dir
		} else {
			errs[i].Locator = dir + "/" + errs[i].Locator
		}
	}
	return errs
}

func truncate(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
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

func readRegistry(o *source.StoreObservation, root string, r *repo) []selection {
	index := r.dir + "/.lemonade_registry.json"
	b, err := os.ReadFile(filepath.Join(root, r.dir, ".lemonade_registry.json"))
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
			if strings.EqualFold(repoID, r.id) {
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

// readUserModels reads Lemonade's user_models.json from its configuration and
// cache directories.
func readUserModels(o *source.StoreObservation, env source.Env) []selection {
	var out []selection
	for _, p := range []string{
		filepath.Join(env.Home, ".config", "lemonade", "user_models.json"),
		filepath.Join(env.Home, ".cache", "lemonade", "user_models.json"),
	} {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			o.Fail(source.ErrorUnreadableIndex, p, err)
			continue
		}
		var models map[string]userModel
		if err := json.Unmarshal(b, &models); err != nil {
			o.Fail(source.ErrorMalformedIndex, p, err)
			continue
		}
		var names []string
		for n := range models {
			names = append(names, n)
		}
		sort.Strings(names)
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
	}
	return out
}

// resolve attaches a Lemonade hold to the artifacts a selection names, or
// records it as dangling. A selection with no revision applies to the
// revisions a ref names, or to every revision when none does.
func resolve(o *source.StoreObservation, repos map[string]*repo, s selection, at string) {
	r, ok := repos[strings.ToLower(s.repo)]
	if !ok {
		o.Want(source.Holder{Program: lemonade}, s.basis(), s.repo+":"+s.spec, "", 0, at)
		return
	}
	var revs []string
	if s.rev != "" {
		revs = []string{s.rev}
	} else {
		for rev := range r.refs {
			if _, ok := r.revs[rev]; ok {
				revs = append(revs, rev)
			}
		}
		if len(revs) == 0 {
			for rev := range r.revs {
				revs = append(revs, rev)
			}
		}
	}
	sort.Strings(revs)
	matched := false
	for _, rev := range revs {
		for _, i := range match(r.revs[rev], s.spec) {
			o.HoldBy(i, source.Holder{Program: lemonade}, source.PurposeInstalled, s.basis(), at)
			matched = true
		}
	}
	if !matched {
		o.Want(source.Holder{Program: lemonade}, s.basis(), s.repo+":"+s.spec, "", 0, at)
	}
}

// match finds the artifacts a Lemonade spec names: an exact file path or
// artifact name, else a quantization variant that is a folder name or the
// suffix of a file name (Q4_K_M names Tiny-Q4_K_M.gguf). A projector file
// matches a variant only when the variant says so.
func match(arts []artifact, spec string) []int {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return nil
	}
	for _, a := range arts {
		if strings.ToLower(a.key) == s {
			return []int{a.manifest}
		}
		for _, f := range a.files {
			if strings.ToLower(f) == s {
				return []int{a.manifest}
			}
		}
	}
	var out []int
	for _, a := range arts {
		for _, f := range a.files {
			lf := strings.ToLower(f)
			if first, _, ok := strings.Cut(lf, "/"); ok && first == s {
				out = append(out, a.manifest)
				break
			}
			base := strings.ToLower(shard.ReplaceAllString(path.Base(f), "$1"))
			base = strings.TrimSuffix(base, path.Ext(base))
			if strings.Contains(base, "mmproj") && !strings.Contains(s, "mmproj") {
				continue
			}
			if base == s || strings.HasSuffix(base, "-"+s) || strings.HasSuffix(base, "_"+s) || strings.HasSuffix(base, "."+s) {
				out = append(out, a.manifest)
				break
			}
		}
	}
	return out
}
