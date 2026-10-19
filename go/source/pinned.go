package source

import (
	"context"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// PinnedFile is one file of a reserved Setup store's catalogue.
type PinnedFile struct {
	// Path is slash-separated and relative to the store's root.
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Role is the file's role in the models it belongs to; it may be empty.
	Role string `json:"role,omitempty"`
}

// PinnedModel is one model of a reserved Setup store's catalogue: its name
// and the paths of the pinned files it is made of.
type PinnedModel struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

// Pinned is a reserved Setup store's catalogue, by path.
type Pinned struct {
	Files  map[string]PinnedFile
	Models []PinnedModel
}

// NewPinned indexes files by path; no files pin nothing.
func NewPinned(files []PinnedFile, models []PinnedModel) Pinned {
	p := Pinned{Models: models}
	if len(files) > 0 {
		p.Files = make(map[string]PinnedFile, len(files))
		for _, f := range files {
			p.Files[f.Path] = f
		}
	}
	return p
}

// Present reports whether a file of size at path is the pinned file there:
// the catalogue names that path with that size.
func (p Pinned) Present(path string, size int64) bool {
	f, ok := p.Files[path]
	return ok && size > 0 && f.Size == size
}

// Complete is the catalogue's models every file of which is present, in
// catalogue order. size answers a path's current size, false when no
// regular file is there. It is the one rule of what a Setup store holds:
// inventoryd applies it to its walk and Setup to its own folder.
func (p Pinned) Complete(size func(path string) (int64, bool)) []string {
	var out []string
	for _, m := range p.Models {
		complete := len(m.Files) > 0
		for _, path := range m.Files {
			n, ok := size(path)
			complete = complete && ok && p.Present(path, n)
		}
		if complete {
			out = append(out, m.Name)
		}
	}
	return out
}

// CompleteAt observes each pinned file beneath one held local root.
func (p Pinned) CompleteAt(root string) []string {
	if len(p.Models) == 0 {
		return nil
	}
	dir, err := fileid.Open(context.Background(), fileid.Self, root, fileid.Options{})
	if err != nil {
		return nil
	}
	defer dir.Close()
	return p.Complete(func(path string) (int64, bool) {
		file, err := dir.Beneath(path, fileid.Options{})
		if err != nil {
			return 0, false
		}
		defer file.Close()
		info, err := file.File().Stat()
		if err != nil || !info.Mode().IsRegular() {
			return 0, false
		}
		return info.Size(), true
	})
}

// PinnedRoot is the catalogue of the reserved Setup store at locator, the
// root's expanded path; an empty Pinned when it pins nothing. Only Setup's
// own stores pin files: another extra root is read as any tree is.
func (e Env) PinnedRoot(locator string) Pinned {
	for _, root := range e.Config.ExtraRoots {
		if len(root.Files) > 0 && setupInventoryStore(root.Store) && e.Expand(root.Path) == locator {
			return NewPinned(root.Files, root.Models)
		}
	}
	return Pinned{}
}
