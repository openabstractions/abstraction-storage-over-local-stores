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
	for _, f := range files {
		full := filepath.Join(loc.Locator, filepath.FromSlash(f.Rel))
		switch {
		case f.Link:
			// A link inside a tree points somewhere this store does not own.
			o.OtherFiles++
		case source.IsPartial(f.Rel):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
		case source.IsWeightFile(full, f.Rel, opt.ExtraWeights...) && source.IsPickle(full, f.Rel):
			// Listed, never loadable (LS-1).
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
		case source.IsWeightFile(full, f.Rel, opt.ExtraWeights...):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
			entry := source.FileEntry(source.RoleOf(f.Rel, opt.ExtraWeights...), f.Rel, f.Size)
			descriptor := env.Descriptor(full, f.Rel, opt.Scheme)
			model(&o, loc.Name, entry, f.Rel, descriptor, opt, purpose, at)
		case opt.Containers && strings.HasSuffix(strings.ToLower(f.Rel), ".zip"):
			o.OtherFiles++
			o.OtherBytes += f.Size
			members(&o, loc.Name, full, f, opt, purpose, at)
		default:
			o.OtherFiles++
			o.OtherBytes += f.Size
		}
	}
	return o.Finish()
}

// model records one weight file's manifest, held by the tree's holder.
func model(o *source.StoreObservation, store string, entry content.Entry, name string, descriptor map[string]string, opt Options, purpose, at string) {
	m := source.NewManifest(store, source.KindModel, []content.Entry{entry},
		[]content.Name{{Scheme: opt.Scheme, Name: name}}, descriptor)
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
			model(o, store, source.FileEntry(role, locator, m.Size), locator, descriptor.Map(), opt, purpose, at)
		case m.Format != "":
			o.AddObject(locator, object, "", content.EvidenceNone)
		}
	}
}
