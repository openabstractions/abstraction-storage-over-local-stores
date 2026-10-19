package source

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	identity "github.com/openabstractions/abstraction-identity"
)

// File is one directory entry the walk saw, by lstat.
type File struct {
	// Rel is slash-separated and relative to the walk root.
	Rel     string
	Size    int64
	ModTime time.Time
	// Link is a symbolic link, junction or other reparse point; its size is
	// the link's own and it is never followed.
	Link bool
}

// Walk lists every regular file and link under root, sorted by Rel. It reads
// directory entries and lstat results only: it opens no file, follows no link
// and descends into no junction. A directory it cannot list becomes a
// StoreError and the walk continues. A missing root is an empty walk.
func Walk(root string) ([]File, []content.StoreError) {
	var out []File
	var errs []content.StoreError
	var visit func(dir, rel string)
	visit = func(dir, rel string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if rel == "" && errors.Is(err, fs.ErrNotExist) {
				return
			}
			errs = append(errs, content.StoreError{Kind: ErrorUnreadableTree, Locator: slashRel(rel), Detail: err.Error()})
			return
		}
		for _, d := range entries {
			childRel := d.Name()
			if rel != "" {
				childRel = rel + "/" + d.Name()
			}
			full := filepath.Join(dir, d.Name())
			info, err := d.Info()
			if err != nil {
				errs = append(errs, content.StoreError{Kind: ErrorUnreadableTree, Locator: childRel, Detail: err.Error()})
				continue
			}
			mode := info.Mode()
			switch {
			case mode&fs.ModeSymlink != 0 || mode&fs.ModeIrregular != 0 && d.IsDir():
				out = append(out, File{Rel: childRel, Size: info.Size(), ModTime: info.ModTime(), Link: true})
			case mode&fs.ModeIrregular != 0:
				// A Windows junction or mount point reports irregular; never descend.
				out = append(out, File{Rel: childRel, Size: 0, ModTime: info.ModTime(), Link: true})
			case d.IsDir():
				visit(full, childRel)
			case mode.IsRegular():
				out = append(out, File{Rel: childRel, Size: info.Size(), ModTime: info.ModTime()})
			}
		}
	}
	visit(root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, errs
}

func slashRel(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// Stamp formats a modification or observation time in the contract's
// timestamp write grammar: UTC, exactly six fractional digits.
func Stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

var partialSuffixes = []string{".part", ".partial", ".incomplete", ".tmp", ".download", ".crdownload", ".downloading"}

// IsPartial reports a name download tools give an unfinished file: the
// suffixes of browsers, huggingface_hub (.incomplete), LM Studio and ComfyUI
// managers (.part), and Ollama's blobs/sha256-<hex>-partial[-N].
func IsPartial(name string) bool {
	base := strings.ToLower(pathBase(name))
	for _, s := range partialSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	if i := strings.Index(base, "-partial"); i > 0 {
		rest := base[i+len("-partial"):]
		if rest == "" {
			return true
		}
		if rest[0] == '-' && isDigits(rest[1:]) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func pathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Within reports whether p is root or lies under it, and p's path below root
// with forward slashes. When root exists it asks by file identity
// (identity.SameFile and identity.WithinRel), which sees junctions and 8.3
// aliases; when it does not, nothing can lie in it except a path spelled
// exactly below it (identity.InsideRel). A non-nil error means the question
// was not answered: a path identity refuses (identity.ErrNotLocalPath) or
// cannot read. Such a path is neither inside nor outside, and a caller
// makes no claim and no store for it.
func Within(root, p string) (string, bool, error) {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if err := identity.LocalPath(root); err != nil {
		return "", false, err
	}
	if err := identity.LocalPath(p); err != nil {
		return "", false, err
	}
	if root == p {
		return "", true, nil
	}
	if same, err := identity.SameFile(root, p); err == nil && same {
		return "", true, nil
	}
	rel, inside, err := identity.InsideRel(p, root)
	return filepath.ToSlash(rel), inside, err
}
