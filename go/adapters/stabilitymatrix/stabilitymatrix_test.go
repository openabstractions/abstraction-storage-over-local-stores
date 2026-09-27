package stabilitymatrix_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/stabilitymatrix"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

func TestDiscoveryReadsLibraryAndPackages(t *testing.T) {
	home := fixture.Home(t, "stabilitymatrix")
	d := stabilitymatrix.New().Discover(fixture.Env(home, nil))
	l := fixture.Location(t, d, "stabilitymatrix")
	if !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, "StabilityMatrix", "Models")) || !strings.Contains(l.Rule, "library.json") {
		t.Fatalf("location %+v", l)
	}
	var programs []string
	for _, c := range d.Claims {
		if !fixture.SamePath(c.Path, l.Locator) || !strings.HasSuffix(filepath.ToSlash(c.Basis.Index), "StabilityMatrix/settings.json") {
			t.Fatalf("claim %+v", c)
		}
		programs = append(programs, c.Holder.Program+"@"+c.Holder.Instance+" "+c.Basis.Role)
	}
	sort.Strings(programs)
	fixture.Equal(t, "sharing packages", programs, []string{"comfyui@stabilitymatrix:ComfyUI Configuration", "stable-diffusion-webui-forge@stabilitymatrix:stable-diffusion-webui-forge Symlink"})
}

func TestPortableLibraryFromOperator(t *testing.T) {
	home := fixture.Home(t, "stabilitymatrix")
	env := fixture.Env(home, nil)
	env.Config.StabilityMatrix = []string{filepath.Join(home, "StabilityMatrix")}
	d := stabilitymatrix.New().Discover(env)
	if len(d.Stores) != 1 {
		t.Fatalf("the same library found twice is one store: %+v", d.Stores)
	}
}

func TestMissing(t *testing.T) {
	home := t.TempDir()
	d := stabilitymatrix.New().Discover(fixture.Env(home, nil))
	if len(d.Stores) != 0 || len(d.Errors) != 0 {
		t.Fatalf("no library.json is no store: %+v", d)
	}
	// An operator-named library that does not exist is reported absent.
	env := fixture.Env(home, nil)
	env.Config.StabilityMatrix = []string{filepath.Join(home, "gone")}
	l := fixture.Location(t, stabilitymatrix.New().Discover(env), "stabilitymatrix")
	if l.Present {
		t.Fatalf("absent library present: %+v", l)
	}
}

func TestModelsDirectory(t *testing.T) {
	home := fixture.Home(t, "stabilitymatrix")
	o := fixture.Observe(t, stabilitymatrix.New(), fixture.Env(home, nil), "stabilitymatrix")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"stabilitymatrix:Lora/local.safetensors", "stabilitymatrix:StableDiffusion/juggernaut.safetensors",
	})
	// Downloaded through Stability Matrix's browser: it wrote .cm-info.json beside the file.
	fixture.Equal(t, "fetched", fixture.Holders(fixture.MustManifest(t, o, "stabilitymatrix", "StableDiffusion/juggernaut.safetensors")), []string{"stabilitymatrix"})
	fixture.Equal(t, "placed by hand", fixture.Holders(fixture.MustManifest(t, o, "stabilitymatrix", "Lora/local.safetensors")), []string(nil))
	if len(o.Objects) != 2 || o.OtherFiles != 1 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
}

func TestMalformedSettings(t *testing.T) {
	home := fixture.Home(t, "stabilitymatrix-malformed")
	env := fixture.Env(home, nil)
	d := stabilitymatrix.New().Discover(env)
	if len(d.Claims) != 0 {
		t.Fatalf("claims from a malformed settings.json: %+v", d.Claims)
	}
	o := fixture.Observe(t, stabilitymatrix.New(), env, "stabilitymatrix")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index " + filepath.Join(home, "StabilityMatrix", "settings.json")})
	fixture.MustManifest(t, o, "stabilitymatrix", "Lora/local.safetensors")
}
