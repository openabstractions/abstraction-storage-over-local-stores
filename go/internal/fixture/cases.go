package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// Label is the placeholder digest the corpus generator derived from a label.
func Label(label string, n int) string {
	s := sha256.Sum256([]byte(label))
	return hex.EncodeToString(s[:])[:n]
}

// EmptyAndMissing checks the two boring cases through discovery: a home where
// the store directory does not exist reports the location absent, and a home
// where it exists with the given empty subdirectories reports it present with
// nothing in it and no error.
func EmptyAndMissing(t *testing.T, a source.Adapter, store string, rel string, subdirs ...string) {
	t.Helper()
	t.Run("missing", func(t *testing.T) {
		home := t.TempDir()
		env := Env(home, nil)
		loc := Location(t, a.Discover(env), store)
		if loc.Present {
			t.Fatalf("location %s present in an empty home", loc.Locator)
		}
		if want := filepath.Join(home, filepath.FromSlash(rel)); !SamePath(loc.Locator, want) {
			t.Fatalf("root %s, want %s", loc.Locator, want)
		}
		o := a.Observe(loc, env)
		if len(o.Objects) != 0 || len(o.Manifests) != 0 || len(o.Errors) != 0 {
			t.Fatalf("missing store reported %d objects, %d manifests, errors %v", len(o.Objects), len(o.Manifests), o.Errors)
		}
		if o.Present {
			t.Fatalf("observation of a missing store says present")
		}
	})
	t.Run("empty", func(t *testing.T) {
		home := t.TempDir()
		root := filepath.Join(home, filepath.FromSlash(rel))
		for _, s := range append([]string{""}, subdirs...) {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(s)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		env := Env(home, nil)
		before := State(t, home)
		loc := Location(t, a.Discover(env), store)
		if !loc.Present {
			t.Fatalf("empty store %s not present", loc.Locator)
		}
		o := a.Observe(loc, env)
		if len(o.Objects) != 0 || len(o.Manifests) != 0 || len(o.Errors) != 0 {
			t.Fatalf("empty store reported %d objects, %d manifests, errors %v", len(o.Objects), len(o.Manifests), o.Errors)
		}
		if o.Objects == nil || o.Manifests == nil || o.Errors == nil {
			t.Fatalf("empty store lists must be empty, not absent")
		}
		if after := State(t, home); after != before {
			t.Fatalf("observation changed the tree: %s", Changed(before, after))
		}
	})
}

// Equal fails with both values when got differs from want.
func Equal(t testing.TB, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %#v\n want %#v", what, got, want)
	}
}

// NoWrite observes every location an adapter discovers and fails if the tree
// under home changed.
func NoWrite(t *testing.T, a source.Adapter, env source.Env) []source.StoreObservation {
	t.Helper()
	before := State(t, env.Home)
	var out []source.StoreObservation
	d := a.Discover(env)
	for _, l := range d.Stores {
		out = append(out, a.Observe(l, env))
	}
	if after := State(t, env.Home); after != before {
		t.Fatalf("adapter %s changed the tree: %s", a.Program(), Changed(before, after))
	}
	if len(out) == 0 {
		t.Fatalf("adapter %s discovered no location", a.Program())
	}
	return out
}

// Observe discovers and observes one store by name.
func Observe(t *testing.T, a source.Adapter, env source.Env, store string) source.StoreObservation {
	t.Helper()
	for _, o := range NoWrite(t, a, env) {
		if o.Name == store {
			return o
		}
	}
	t.Fatalf("adapter %s observed no store %s", a.Program(), store)
	return source.StoreObservation{}
}
