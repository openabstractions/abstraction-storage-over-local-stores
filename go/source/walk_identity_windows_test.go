//go:build windows

package source

import (
	"errors"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// A path identity refuses is not answered, whatever its spelling (ID-F1).
func TestWithinRefusedDriveIsNotAnswered(t *testing.T) {
	restore := identity.TreatDriveAsNetwork('Y')
	defer restore()
	local := t.TempDir()
	for _, c := range [][2]string{
		{`Y:\root`, `Y:\root\a`},
		{`Y:\root`, `Y:\root`},
		{local, `Y:\elsewhere`},
		{`Y:\root`, filepath.Join(local, "a")},
	} {
		if rel, inside, err := Within(c[0], c[1]); inside || !errors.Is(err, identity.ErrNotLocalPath) {
			t.Errorf("Within(%q, %q) = %q, %v, %v; want ErrNotLocalPath", c[0], c[1], rel, inside, err)
		}
	}
}

// A root still to be created is matched by spelling.
func TestWithinMissingRootUsesSpelling(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if rel, inside, err := Within(root, filepath.Join(root, "a", "b")); err != nil || !inside || rel != "a/b" {
		t.Fatalf("Within = %q, %v, %v; want a/b, true", rel, inside, err)
	}
	if _, inside, err := Within(root, filepath.Join(filepath.Dir(root), "other")); err != nil || inside {
		t.Fatalf("a sibling of a missing root: %v, %v", inside, err)
	}
}
