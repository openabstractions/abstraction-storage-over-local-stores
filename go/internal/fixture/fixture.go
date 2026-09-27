// Package fixture copies the synthetic store layouts under testdata/ into a
// temporary home and reads observations back for assertions.
package fixture

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Testdata is the corpus directory.
func Testdata() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// Home copies the named fixture homes into one temporary directory and returns
// it. Each testdata/<name> is a synthetic home for one program; nothing in it
// is copied from a real store and file contents are placeholders of chosen
// sizes. The copy expands tokens that keep checked-in paths under Windows'
// 260-character limit inside a worktree checkout:
//
//	~hub       top-level directory   .cache/huggingface/hub
//	~amd       top-level directory   AppData/Local/AMD/AI_Bundle/ComfyUI/ComfyUI
//	~roaming   top-level directory   AppData/Roaming
//	%N.label%  path or file content  first N hex characters of sha256(label)
//	{{ROOT}}   file content          the temporary home, forward slashes
func Home(t testing.TB, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		src := filepath.Join(Testdata(), n)
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("fixture %s: %v", n, err)
		}
		if err := copyTree(src, root); err != nil {
			t.Fatalf("copy fixture %s: %v", n, err)
		}
	}
	return root
}

var aliases = map[string]string{
	"~hub":     ".cache/huggingface/hub",
	"~amd":     "AppData/Local/AMD/AI_Bundle/ComfyUI/ComfyUI",
	"~roaming": "AppData/Roaming",
}

var token = regexp.MustCompile(`%(\d+)\.([A-Za-z0-9_-]+)%`)

func expand(s string) string {
	return token.ReplaceAllStringFunc(s, func(m string) string {
		parts := token.FindStringSubmatch(m)
		//unchecked: parts[1] is captured by the \d+ regex group, so it can only fail to parse on an overflow no real fixture path produces
		n, _ := strconv.Atoi(parts[1])
		return Label(parts[2], n)
	})
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		//unchecked: p always comes from walking src via WalkDir, so Rel(src, p) cannot fail here
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		first, rest, _ := strings.Cut(rel, "/")
		if a, ok := aliases[first]; ok {
			rel = a
			if rest != "" {
				rel += "/" + rest
			}
		}
		target := filepath.Join(dst, filepath.FromSlash(expand(rel)))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		body := strings.ReplaceAll(string(b), "{{ROOT}}", filepath.ToSlash(dst))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(expand(body)), 0o644)
	})
}

// Env is a Windows-shaped environment over a synthetic home: AppData under
// the home, no environment variables unless vars names them.
func Env(home string, vars map[string]string) source.Env {
	return source.Env{
		GOOS: "windows",
		Home: home,
		Getenv: func(k string) string {
			return vars[k]
		},
		Now: func() time.Time { return time.Date(2026, 9, 16, 17, 13, 21, 0, time.UTC) },
		// Headers is set as inventoryd sets it, so a fixture whose weight file
		// carries a synthetic GGUF header is read the way a real one is.
		Headers: source.NewHeaders(),
	}
}

// Manifest finds the manifest carrying a name.
func Manifest(o source.StoreObservation, scheme, name string) (content.ManifestHolders, bool) {
	for _, m := range o.Manifests {
		for _, n := range m.Manifest.Names {
			if n.Scheme == scheme && n.Name == name {
				return m, true
			}
		}
	}
	return content.ManifestHolders{}, false
}

// MustManifest is Manifest or a test failure listing what exists.
func MustManifest(t testing.TB, o source.StoreObservation, scheme, name string) content.ManifestHolders {
	t.Helper()
	m, ok := Manifest(o, scheme, name)
	if !ok {
		t.Fatalf("store %s: no manifest %s:%s; have %v", o.Name, scheme, name, Names(o))
	}
	return m
}

// Names lists every manifest name as scheme:name, sorted.
func Names(o source.StoreObservation) []string {
	var out []string
	for _, m := range o.Manifests {
		for _, n := range m.Manifest.Names {
			out = append(out, n.Scheme+":"+n.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Holders lists the holders of a manifest once each, sorted: the program, or
// program@instance where the source names the installation.
func Holders(m content.ManifestHolders) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range m.Holds {
		name := h.Holder.Program
		if h.Holder.Instance != "" {
			name += "@" + h.Holder.Instance
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Bases lists every hold's basis of a manifest as program index|key|role, sorted.
func Bases(m content.ManifestHolders) []string {
	var out []string
	for _, h := range m.Holds {
		if h.Basis == nil {
			out = append(out, h.Holder.Program+" <no basis>")
			continue
		}
		out = append(out, h.Holder.Program+" "+h.Basis.Index+"|"+h.Basis.Key+"|"+h.Basis.Role)
	}
	sort.Strings(out)
	return out
}

// Object finds an object by locator.
func Object(o source.StoreObservation, locator string) (content.Object, bool) {
	for _, x := range o.Objects {
		if x.Locator == locator {
			return x, true
		}
	}
	return content.Object{}, false
}

// Locators lists every object locator, sorted.
func Locators(o source.StoreObservation) []string {
	var out []string
	for _, x := range o.Objects {
		out = append(out, x.Locator)
	}
	sort.Strings(out)
	return out
}

// Roles lists entry roles in order.
func Roles(m content.ManifestHolders) []string {
	var out []string
	for _, e := range m.Manifest.Entries {
		out = append(out, e.Role)
	}
	return out
}

// State is every path under root, with size, mode and modification time for
// files. Equal states before and after a read mean nothing was written, moved,
// created or deleted. Directory times are left out: NTFS updates the time a
// parent's listing reports for a directory lazily, so it moves when a
// directory is merely opened; a created or deleted entry still shows as a line.
func State(t testing.TB, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		//unchecked: p always comes from walking root via WalkDir, so Rel(root, p) cannot fail here
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			lines = append(lines, filepath.ToSlash(rel)+"/")
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s\t%d\t%s\t%d", filepath.ToSlash(rel), info.Size(), info.Mode(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatalf("state %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// Changed lists the lines two States disagree on.
func Changed(before, after string) string {
	b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
	inB, inA := map[string]bool{}, map[string]bool{}
	for _, l := range b {
		inB[l] = true
	}
	for _, l := range a {
		inA[l] = true
	}
	var out []string
	for _, l := range b {
		if !inA[l] {
			out = append(out, "-"+l)
		}
	}
	for _, l := range a {
		if !inB[l] {
			out = append(out, "+"+l)
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return fmt.Sprintf("%d -> %d entries: %s", len(b), len(a), strings.Join(out, "; "))
}

// ErrorKinds lists error kinds with locators, sorted.
func ErrorKinds(o source.StoreObservation) []string {
	var out []string
	for _, e := range o.Errors {
		out = append(out, string(e.Kind)+" "+e.Locator)
	}
	sort.Strings(out)
	return out
}

// Size is a fixture file's size on disk.
func Size(t testing.TB, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return st.Size()
}

// Location finds a discovered location by store name.
func Location(t testing.TB, d source.Discovery, store string) content.Store {
	t.Helper()
	for _, l := range d.Stores {
		if l.Name == store {
			return l
		}
	}
	var have []string
	for _, l := range d.Stores {
		have = append(have, l.Name+"="+l.Locator)
	}
	t.Fatalf("no location %s; have %v", store, have)
	return content.Store{}
}

// SamePath compares the exact cleaned spelling of fixture locators, including
// paths that do not exist yet. Fixture expectations preserve directory case.
func SamePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
