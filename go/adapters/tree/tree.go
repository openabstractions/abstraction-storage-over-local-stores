// Package tree reads a store whose directory tree is its only index: LM
// Studio, ComfyUI, Stability Matrix, Pinokio's drive, SwarmUI, Lemonade's
// extra models directory and operator-named roots. Each weight file is one
// manifest named by its path; each partial download is an object in no
// manifest; every other file is counted and skipped.
package tree

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/container"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Options says how to name and attribute a tree.
type Options struct {
	// Scheme names manifests; the file's path relative to the root is the name.
	Scheme string
	// Holder, when set, holds every weight file: the program whose tree this is.
	Holder  string
	Purpose string
	// Index is the basis recorded for Holder's holds.
	Index string
	// ExtraWeights adds weight-file extensions beyond the prototype's set.
	ExtraWeights []string
	// Containers reads each .zip in the tree by its central directory and
	// reports its weight-format members as objects of their own (LS-8).
	Containers bool
	// Pinned is a reserved Setup store's catalogue. A file at a pinned path
	// with its pinned size is one object carrying the pinned digest as
	// hashed evidence; alone it is in no manifest and never a model of its
	// own. A catalogue model all of whose files are present is one model
	// manifest named by it, its files its entries.
	Pinned source.Pinned
}

// Observe walks the tree by lstat only.
func Observe(loc content.Store, env source.Env, opt Options) source.StoreObservation {
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	files, errs := source.Walk(loc.Locator)
	o.Errors = append(o.Errors, errs...)
	at := source.Stamp(env.At())
	purpose := opt.Purpose
	if purpose == "" {
		purpose = source.PurposeInstalled
	}
	sizes := map[string]int64{}
	for _, f := range files {
		full := filepath.Join(loc.Locator, filepath.FromSlash(f.Rel))
		if !f.Link {
			sizes[f.Rel] = f.Size
		}
		switch {
		case f.Link:
			// A link inside a tree points somewhere this store does not own.
			o.OtherFiles++
		case opt.Pinned.Present(f.Rel, f.Size):
			o.AddObject(f.Rel, f, "sha256:"+opt.Pinned.Files[f.Rel].SHA256, content.EvidenceHashed)
		case source.IsPartial(f.Rel):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
		case source.IsWeightFile(full, f.Rel, opt.ExtraWeights...) && source.IsPickle(full, f.Rel):
			// Listed, never loadable (LS-1).
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
		case source.IsWeightFile(full, f.Rel, opt.ExtraWeights...):
			// A tree names no file by content. A digest an on-demand hash
			// recorded (LS-13) is shown on the object and the entry only.
			digest, evidence := source.ContentDigest(env, full, f.Rel, f.Size, f.ModTime, opt.ExtraWeights...)
			o.AddObject(f.Rel, f, digest, evidence)
			entry := source.FileEntry(source.RoleOf(f.Rel, opt.ExtraWeights...), f.Rel, f.Size)
			descriptor := env.Descriptor(full, f.Rel, opt.Scheme)
			model(&o, loc.Name, entry, f.Rel, descriptor, opt, purpose, at, digest)
		case opt.Containers && strings.HasSuffix(strings.ToLower(f.Rel), ".zip"):
			o.OtherFiles++
			o.OtherBytes += f.Size
			members(&o, loc.Name, full, f, opt, purpose, at)
		default:
			o.OtherFiles++
			o.OtherBytes += f.Size
		}
	}
	pinnedModels(&o, loc.Name, opt, purpose, at, sizes)
	return o.Finish()
}

// pinnedModels records each complete catalogue model as one model manifest
// named by it, with its files as content-addressed entries.
func pinnedModels(o *source.StoreObservation, store string, opt Options, purpose, at string, sizes map[string]int64) {
	complete := opt.Pinned.Complete(func(path string) (int64, bool) {
		n, ok := sizes[path]
		return n, ok
	})
	for _, name := range complete {
		var files []string
		for _, m := range opt.Pinned.Models {
			if m.Name == name {
				files = m.Files
			}
		}
		entries := make([]content.Entry, 0, len(files))
		for _, p := range files {
			pin := opt.Pinned.Files[p]
			entry := source.FileEntry(pin.Role, p, pin.Size)
			entry.Digest, entry.Evidence = "sha256:"+pin.SHA256, content.EvidenceHashed
			entries = append(entries, entry)
		}
		m := source.NewManifest(store, source.KindModel, entries, []content.Name{{Scheme: opt.Scheme, Name: name}},
			source.Named(name, "safetensors", opt.Scheme).Map())
		i := o.AddManifest(m)
		if opt.Holder != "" {
			o.Hold(i, opt.Holder, purpose, opt.Index, name, at)
		}
	}
}

// model records one weight file's manifest, held by the tree's holder.
func model(o *source.StoreObservation, store string, entry content.Entry, name string, descriptor map[string]string, opt Options, purpose, at, recorded string) {
	m := source.NewManifest(store, source.KindModel, []content.Entry{entry},
		[]content.Name{{Scheme: opt.Scheme, Name: name}}, descriptor)
	m = source.WithRecordedDigests(m, map[string]string{entry.Locator: recorded})
	i := o.AddManifest(m)
	if opt.Holder != "" {
		o.Hold(i, opt.Holder, purpose, opt.Index, path.Dir(name), at)
	}
}

// members reports the members of the zip at full by its central directory
// alone (LS-8, LS-9): each loadable weight is an object with a manifest of
// its own, a pickle or nested zip is an object with none, and a refused
// container is a store error. Nothing is hashed here (LS-6).
func members(o *source.StoreObservation, store, full string, f source.File, opt Options, purpose, at string) {
	list, err := container.Members(full)
	if err != nil {
		o.Fail(content.StoreErrorKindOther, f.Rel, err)
		return
	}
	for _, m := range list {
		locator := container.MemberLocator(f.Rel, m.Chain()...)
		object := source.File{Rel: locator, Size: m.Size, ModTime: f.ModTime}
		switch {
		case m.Model():
			o.AddObject(locator, object, "", content.EvidenceNone)
			role := source.RoleOf(m.Name)
			descriptor := source.Named(m.Name, source.FormatOf(m.Name), opt.Scheme)
			if role == source.RoleProjector || role == source.RoleVAE {
				descriptor = descriptor.WithComponent(m.Name, role)
			}
			model(o, store, source.FileEntry(role, locator, m.Size), locator, descriptor.Map(), opt, purpose, at, "")
		case m.Format != "":
			o.AddObject(locator, object, "", content.EvidenceNone)
		}
	}
}
