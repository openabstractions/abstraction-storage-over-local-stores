package extra_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

func TestOperatorRoots(t *testing.T) {
	home := fixture.Home(t, "extra")
	env := fixture.Env(home, map[string]string{"ABSTRACTION_MODEL_ROOTS": "tank=" + filepath.Join(home, "Tank")})
	env.Config.ExtraRoots = []source.ExtraRoot{
		{Store: "nas", Path: filepath.Join(home, "NAS", "models"), Holder: "archivist"},
		{Store: "gone", Path: filepath.Join(home, "gone")},
	}
	a := extra.New()
	d := a.Discover(env)
	if l := fixture.Location(t, d, "nas"); !l.Present || !strings.Contains(l.Rule, "operator") {
		t.Fatalf("nas %+v", l)
	}
	if l := fixture.Location(t, d, "tank"); !l.Present || !strings.Contains(l.Rule, "ABSTRACTION_MODEL_ROOTS") {
		t.Fatalf("tank %+v", l)
	}
	if l := fixture.Location(t, d, "gone"); l.Present {
		t.Fatalf("gone %+v", l)
	}

	nas := fixture.Observe(t, a, env, "nas")
	m := fixture.MustManifest(t, nas, "nas", "archive/old-model.gguf")
	fixture.Equal(t, "operator-named holder", fixture.Holders(m), []string{"archivist"})
	tank := fixture.Observe(t, a, env, "tank")
	fixture.Equal(t, "no holder evidence", fixture.Holders(fixture.MustManifest(t, tank, "tank", "weights.safetensors")), []string(nil))
}

func TestSetupPinnedRootRefusesDirectoryReplacement(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "chosen")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "original.gguf"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := identity.FileKey(root)
	if err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, nil)
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "chosen", Path: root, ExpectedFileKey: key}}
	adapter := extra.New()

	before := adapter.Discover(env)
	if len(before.Stores) != 1 || len(before.Stores[0].Errors) != 0 {
		t.Fatalf("initial selected root: %+v", before)
	}
	old := root + "-old"
	if err := os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "replacement.gguf"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	observed := adapter.Observe(before.Stores[0], env)
	if len(observed.Objects) != 0 || len(observed.Manifests) != 0 || len(observed.Errors) == 0 || !strings.Contains(observed.Errors[0].Detail, "identity changed") {
		t.Fatalf("replacement folder was read: %+v", observed)
	}
	after := adapter.Discover(env)
	if len(after.Stores) != 1 || len(after.Stores[0].Errors) == 0 {
		t.Fatalf("replacement folder remained approved by config: %+v", after)
	}
}

func TestIgnoreEnvironmentRootsKeepsOnlyConfiguredRoots(t *testing.T) {
	home := fixture.Home(t, "extra")
	env := fixture.Env(home, map[string]string{"ABSTRACTION_MODEL_ROOTS": "ambient=" + filepath.Join(home, "Ambient")})
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "chosen", Path: filepath.Join(home, "Chosen")}}
	if err := os.MkdirAll(filepath.Join(home, "Chosen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Ambient"), 0o755); err != nil {
		t.Fatal(err)
	}
	env.IgnoreEnvironmentRoots = true
	d := extra.New().Discover(env)
	if l := fixture.Location(t, d, "chosen"); !l.Present || !strings.Contains(l.Rule, "operator") {
		t.Fatalf("configured root %+v", l)
	}
	for _, store := range d.Stores {
		if store.Name == "ambient" {
			t.Fatalf("environment root survived explicit-root mode: %+v", store)
		}
	}
	if !strings.Contains(env.Var("ABSTRACTION_MODEL_ROOTS"), "Ambient") {
		t.Fatal("test environment lost its ambient root before adapter discovery")
	}
}
