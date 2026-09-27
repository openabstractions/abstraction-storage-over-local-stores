package fastflowlm_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/fastflowlm"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

func TestDiscovery(t *testing.T) {
	home := fixture.Home(t, "fastflowlm")
	a := fastflowlm.New()
	l := fixture.Location(t, a.Discover(fixture.Env(home, nil)), "fastflowlm")
	if !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, ".flm", "models")) {
		t.Fatalf("windows default %+v", l)
	}
	env := fixture.Env(home, map[string]string{"FLM_MODEL_PATH": filepath.Join(home, "npu")})
	l = fixture.Location(t, a.Discover(env), "fastflowlm")
	if !fixture.SamePath(l.Locator, filepath.Join(home, "npu")) || !strings.Contains(l.Rule, "FLM_MODEL_PATH") {
		t.Fatalf("env %+v", l)
	}
	env = fixture.Env(home, nil)
	env.GOOS = "linux"
	l = fixture.Location(t, a.Discover(env), "fastflowlm")
	if !fixture.SamePath(l.Locator, filepath.Join(home, ".config", "flm", "models")) {
		t.Fatalf("linux default %+v", l)
	}
	env = fixture.Env(home, nil)
	env.Config.FastFlowLM = []string{filepath.Join(home, "chosen", "models")}
	d := a.Discover(env)
	var operator bool
	for _, l := range d.Stores {
		if fixture.SamePath(l.Locator, filepath.Join(home, "chosen", "models")) && strings.Contains(l.Rule, "operator") {
			operator = true
		}
	}
	if !operator {
		t.Fatalf("operator location missing from %+v", d.Stores)
	}
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, fastflowlm.New(), "fastflowlm", ".flm/models")
}

func TestModelDirectoryIsOneManifest(t *testing.T) {
	home := fixture.Home(t, "fastflowlm")
	o := fixture.Observe(t, fastflowlm.New(), fixture.Env(home, nil), "fastflowlm")
	if len(o.Errors) != 0 {
		t.Fatalf("errors %v", o.Errors)
	}
	fixture.Equal(t, "names", fixture.Names(o), []string{"flm:Tiny-NPU2"})
	m := fixture.MustManifest(t, o, "flm", "Tiny-NPU2")
	fixture.Equal(t, "holders", fixture.Holders(m), []string{"fastflowlm"})
	var got []string
	for _, e := range m.Manifest.Entries {
		got = append(got, e.Role+" "+e.Locator)
	}
	fixture.Equal(t, "entries", got, []string{
		"template Tiny-NPU2/chat_template.jinja",
		"weights.config Tiny-NPU2/config.json",
		"weights Tiny-NPU2/model.q4nx",
		"weights.config Tiny-NPU2/tokenizer.json",
		"weights.config Tiny-NPU2/tokenizer_config.json",
		"projector Tiny-NPU2/vision_weight.q4nx",
	})
	half, ok := fixture.Object(o, "Half-NPU/model.q4nx.part")
	if !ok || !half.Partial {
		t.Fatalf("partial %+v", half)
	}
	if len(o.Objects) != 7 || o.OtherFiles != 1 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
}
