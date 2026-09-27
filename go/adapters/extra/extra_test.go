package extra_test

import (
	"path/filepath"
	"strings"
	"testing"

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
