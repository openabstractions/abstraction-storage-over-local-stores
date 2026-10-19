// Package huggingface reads the Hugging Face hub cache, and exports its
// repository reader for programs that keep their own indexes over that
// layout (the lemonade adapter).
//
// Layout (huggingface_hub, "Manage cache"): <hub>/models--<org>--<repo>/ with
// refs/<name> holding a commit, snapshots/<commit>/<path> as links into blobs/
// (or plain files where links are unavailable, as on Windows without Developer
// Mode and on the owner's machine), and blobs/<etag>[.incomplete].
package huggingface

import (
	"bytes"
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

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	identity "github.com/openabstractions/abstraction-identity"
)

const (
	program = "huggingface"
	// ambiguousBinExt is ".bin": ambiguous by name alone elsewhere (see
	// source.IsWeightFile), but every file this adapter walks sits inside a
	// repository's snapshots/<rev>/, the Hugging Face cache layout that
	// already settles the question.
	ambiguousBinExt = ".bin"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// HubRoot applies huggingface_hub's own precedence for the cache in use:
// HF_HUB_CACHE, then the legacy HUGGINGFACE_HUB_CACHE, then HF_HOME/hub, then
// $XDG_CACHE_HOME/huggingface/hub, then ~/.cache/huggingface/hub. It returns
// the root, the rule that chose it, and the default root.
func HubRoot(env source.Env) (root, rule, def string) {
	def = filepath.Join(env.Home, ".cache", "huggingface", "hub")
	switch {
	case env.Var("HF_HUB_CACHE") != "":
		root, rule = env.Expand(env.Var("HF_HUB_CACHE")), "environment HF_HUB_CACHE"
	case env.Var("HUGGINGFACE_HUB_CACHE") != "":
		root, rule = env.Expand(env.Var("HUGGINGFACE_HUB_CACHE")), "environment HUGGINGFACE_HUB_CACHE (legacy)"
	case env.Var("HF_HOME") != "":
		root, rule = filepath.Join(env.Expand(env.Var("HF_HOME")), "hub"), "environment HF_HOME"
	default:
		if cache, set := CacheHome(env); set {
			root, rule = filepath.Join(cache, "huggingface", "hub"), "environment XDG_CACHE_HOME"
		} else {
			root, rule = def, "default location ~/.cache/huggingface/hub"
		}
	}
	return root, rule, def
}

// CacheHome is the XDG cache directory huggingface_hub resolves its default
// under. It returns $XDG_CACHE_HOME and true when it is set, else ~/.cache
// and false. Lemonade resolves its own cache directory the same way.
func CacheHome(env source.Env) (string, bool) {
	if x := env.Var("XDG_CACHE_HOME"); x != "" {
		return env.Expand(x), true
	}
	return filepath.Join(env.Home, ".cache"), false
}

// Discover reports the cache HubRoot names. A program started with another
// environment may have used the default, so the default is also reported
// when it exists and differs.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	root, rule, def := HubRoot(env)
	d.Stores = append(d.Stores, source.NewStore("huggingface", program, root, rule))
	if !SameFolder(root, def) && source.IsDir(def) {
		d.Stores = append(d.Stores, source.NewStore("huggingface-default", program, def, "default location ~/.cache/huggingface/hub"))
	}
	return d
}

// SameFolder reports whether a and b name one folder: by file identity when
// both exist, else by their exact cleaned spelling.
func SameFolder(a, b string) bool {
	if same, err := identity.SameFile(a, b); err == nil {
		return same
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Observe reads every models--<org>--<repo> repository in the cache.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
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
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), RepoPrefix) {
			if !d.IsDir() {
				o.OtherFiles++
			}
			continue
		}
		ReadRepo(&o, env, loc.Locator, d.Name(), program, at)
	}
	return o.Finish()
}

type file struct {
	rel      string // inside the snapshot
	locator  string // inside the store
	size     int64
	digest   string
	evidence content.Evidence
	// recorded is the digest an on-demand hash recorded for a snapshot
	// file no blob names (LS-13); it goes on the entry, never into the
	// manifest's identity.
	recorded string
}

// Artifact is one weights artifact of a revision: a file or a shard set.
type Artifact struct {
	// Key is the artifact's name inside the revision.
	Key string
	// Files are the weight files, relative to the snapshot.
	Files []string
	// Locators are the entry locators of Files, relative to the store.
	Locators []string
	// Manifest is the index of the artifact's manifest in the observation.
	Manifest int
}

// Repo is one repository ReadRepo read.
type Repo struct {
	Dir  string
	ID   string
	Refs map[string][]string // revision -> ref names
	Revs map[string][]Artifact
}

// RepoPrefix marks a Hugging Face repository directory; ModelScopePrefix
// marks a ModelScope repository written in the same layout (Lemonade).
const (
	RepoPrefix       = "models--"
	ModelScopePrefix = "modelscope--models--"
)

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Shard matches the -NNNNN-of-NNNNN part of a shard file name.
var Shard = regexp.MustCompile(`-\d{5}-of-\d{5}(\.[A-Za-z0-9]+)$`)

// ReadRepo reads one repository directory in the hub cache layout into o:
// its blobs and snapshot files as objects, a manifest per weights artifact
// of each revision, and a hold by holder per ref naming that revision. A
// models-- repository is named in scheme hf, a ModelScope one in modelscope.
func ReadRepo(o *source.StoreObservation, env source.Env, root, dir, holder, at string) *Repo {
	scheme, id := "hf", strings.TrimPrefix(dir, RepoPrefix)
	if strings.HasPrefix(dir, ModelScopePrefix) {
		scheme, id = "modelscope", strings.TrimPrefix(dir, ModelScopePrefix)
	}
	r := &Repo{Dir: dir, ID: strings.ReplaceAll(id, "--", "/"), Refs: map[string][]string{}, Revs: map[string][]Artifact{}}
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
		r.Refs[rev] = append(r.Refs[rev], f.Rel)
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
					o.Want(source.Holder{Program: holder}, content.Basis{Index: prefix + f.Rel, Key: f.Rel}, filepath.ToSlash(target), expected, 0, at)
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
				} else {
					// The hub moved the blob into its snapshot: no blob names
					// the file. A digest an on-demand hash recorded (LS-13)
					// is shown on the object and the entry only.
					x.recorded, _ = source.ContentDigest(env, filepath.Join(revDir, filepath.FromSlash(f.Rel)), f.Rel, f.Size, f.ModTime, ambiguousBinExt)
				}
				digest, evidence := x.digest, x.evidence
				if x.recorded != "" {
					digest, evidence = x.recorded, content.EvidenceHashed
				}
				o.AddObject(x.locator, f, digest, evidence)
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
			k := Shard.ReplaceAllString(w.rel, "$1")
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], w)
		}
		sort.Strings(keys)
		held := r.Refs[rev]
		name := revisionName(r, rev, len(snaps))
		addHolds := func(i int) {
			for _, name := range held {
				o.Hold(i, holder, source.PurposeCache, dir+"/refs/"+name, name, at)
			}
		}
		for _, k := range keys {
			fs := groups[k]
			sort.Slice(fs, func(i, j int) bool { return fs[i].rel < fs[j].rel })
			var entries []content.Entry
			var rels, locs []string
			for _, f := range fs {
				entries = append(entries, entryOf(f, source.RoleOf(f.rel)))
				rels = append(rels, f.rel)
				locs = append(locs, f.locator)
			}
			for _, f := range others {
				entries = append(entries, entryOf(f, roleOrFile(f.rel)))
			}
			descriptor := source.Named(name+":"+k, source.FormatOf(k), holder)
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
			m := source.NewManifest(o.Name, source.KindModel, entries, []content.Name{{Scheme: scheme, Name: name + ":" + k}}, descriptor.Map())
			m = source.WithRecordedDigests(m, recordedOf(fs, others))
			i := o.AddManifest(m)
			addHolds(i)
			r.Revs[rev] = append(r.Revs[rev], Artifact{Key: k, Files: rels, Locators: locs, Manifest: i})
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
			m := source.NewManifest(o.Name, source.KindSnapshot, entries, []content.Name{{Scheme: scheme, Name: r.ID + "@" + short}}, nil)
			m = source.WithRecordedDigests(m, recordedOf(nil, others))
			addHolds(o.AddManifest(m))
		}
	}
	return r
}

// recordedOf maps each file's locator to the digest an on-demand hash
// recorded for it (LS-13).
func recordedOf(groups ...[]file) map[string]string {
	out := map[string]string{}
	for _, files := range groups {
		for _, f := range files {
			if f.recorded != "" {
				out[f.locator] = f.recorded
			}
		}
	}
	return out
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
func revisionName(r *Repo, rev string, revisions int) string {
	refs := append([]string(nil), r.Refs[rev]...)
	sort.Strings(refs)
	for _, ref := range refs {
		if ref == "main" {
			return r.ID
		}
	}
	if revisions <= 1 {
		return r.ID
	}
	if len(refs) > 0 {
		return r.ID + "@" + refs[0]
	}
	short := rev
	if len(short) > 12 {
		short = short[:12]
	}
	return r.ID + "@" + short
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
