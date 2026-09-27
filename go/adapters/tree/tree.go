// Package tree reads a store whose directory tree is its only index: LM
// Studio, ComfyUI, Stability Matrix, Pinokio's drive, SwarmUI, Lemonade's
// extra models directory and operator-named roots. Each weight file is one
// manifest named by its path; each partial download is an object in no
// manifest; every other file is counted and skipped.
package tree

import (
	"path"
	"path/filepath"

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
		case source.IsWeightFile(full, f.Rel, opt.ExtraWeights...):
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
			entry := source.FileEntry(source.RoleOf(f.Rel, opt.ExtraWeights...), f.Rel, f.Size)
			descriptor := env.Descriptor(full, f.Rel, opt.Scheme)
			m := source.NewManifest(loc.Name, source.KindModel, []content.Entry{entry},
				[]content.Name{{Scheme: opt.Scheme, Name: f.Rel}}, descriptor)
			i := o.AddManifest(m)
			if opt.Holder != "" {
				o.Hold(i, opt.Holder, purpose, opt.Index, path.Dir(f.Rel), at)
			}
		default:
			o.OtherFiles++
			o.OtherBytes += f.Size
		}
	}
	return o.Finish()
}
