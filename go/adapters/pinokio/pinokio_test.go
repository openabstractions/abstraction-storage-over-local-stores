package pinokio_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/pinokio"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

func TestDiscoveryReadsDrivesJSON(t *testing.T) {
	home := fixture.Home(t, "pinokio")
	d := pinokio.New().Discover(fixture.Env(home, nil))
	l := fixture.Location(t, d, "pinokio")
	if !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, "pinokio", "drive")) {
		t.Fatalf("location %+v", l)
	}
	var got []string
	for _, c := range d.Claims {
		rel, _ := filepath.Rel(l.Locator, c.Path)
		got = append(got, c.Holder.Program+"@"+c.Holder.Instance+" "+c.Basis.Role+" "+filepath.ToSlash(rel))
	}
	sort.Strings(got)
	fixture.Equal(t, "claims", got, []string{
		"comfy@pinokio:https://github.com/cocktailpeanut/comfy.git app/models/checkpoints drives/peers/d17/checkpoints",
		"forge@pinokio:https://github.com/pinokiofactory/forge.git app/models/Lora drives/peers/d18/gone",
		"forge@pinokio:https://github.com/pinokiofactory/forge.git app/models/Stable-diffusion drives/peers/d17/checkpoints",
	})

	// PINOKIO_HOME names the home without ~/.pinokio.
	bare := fixture.Home(t, "pinokio")
	if err := os.RemoveAll(filepath.Join(bare, ".pinokio")); err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(bare, map[string]string{"PINOKIO_HOME": filepath.Join(bare, "pinokio")})
	if l := fixture.Location(t, pinokio.New().Discover(env), "pinokio"); !l.Present || !strings.Contains(l.Rule, "PINOKIO_HOME") {
		t.Fatalf("PINOKIO_HOME %+v", l)
	}
}

func TestMissing(t *testing.T) {
	d := pinokio.New().Discover(fixture.Env(t.TempDir(), nil))
	if len(d.Stores) != 0 {
		t.Fatalf("no Pinokio home, no store: %+v", d.Stores)
	}
	env := fixture.Env(t.TempDir(), nil)
	env.Config.Pinokio = []string{filepath.Join(env.Home, "gone")}
	if l := fixture.Location(t, pinokio.New().Discover(env), "pinokio"); l.Present {
		t.Fatalf("absent home present %+v", l)
	}
}

func TestDriveTree(t *testing.T) {
	home := fixture.Home(t, "pinokio")
	o := fixture.Observe(t, pinokio.New(), fixture.Env(home, nil), "pinokio")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"pinokio:drives/peers/d17/checkpoints/shared.safetensors",
		"pinokio:drives/peers/d17/loras/unrouted.safetensors",
	})
	if len(o.Objects) != 2 || o.OtherFiles != 1 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
}

func TestMalformedDrivesJSON(t *testing.T) {
	home := fixture.Home(t, "pinokio-malformed")
	env := fixture.Env(home, nil)
	if d := pinokio.New().Discover(env); len(d.Claims) != 0 {
		t.Fatalf("claims from malformed drives.json %+v", d.Claims)
	}
	o := fixture.Observe(t, pinokio.New(), env, "pinokio")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index drives.json"})
	if len(o.Manifests) != 1 {
		t.Fatalf("manifests %v", fixture.Names(o))
	}
}
