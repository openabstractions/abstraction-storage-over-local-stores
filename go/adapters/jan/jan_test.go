package jan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/jan"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

const models = "AppData/Roaming/Jan/data/llamacpp/models"

func TestDiscovery(t *testing.T) {
	home := fixture.Home(t, "jan")
	l := fixture.Location(t, jan.New().Discover(fixture.Env(home, nil)), "jan")
	if !l.Present || !fixture.SamePath(l.Locator, filepath.Join(home, filepath.FromSlash(models))) {
		t.Fatalf("location %+v", l)
	}

	// Jan's own settings move the data folder.
	moved := filepath.Join(home, "JanData")
	if err := os.MkdirAll(filepath.Join(home, "AppData", "Roaming", "jan.ai.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"data_folder": "` + filepath.ToSlash(moved) + `"}`
	if err := os.WriteFile(filepath.Join(home, "AppData", "Roaming", "jan.ai.app", "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	l = fixture.Location(t, jan.New().Discover(fixture.Env(home, nil)), "jan")
	if !fixture.SamePath(l.Locator, filepath.Join(moved, "llamacpp", "models")) || !strings.Contains(l.Rule, "data_folder") {
		t.Fatalf("moved %+v", l)
	}
}

func TestEmptyAndMissing(t *testing.T) {
	fixture.EmptyAndMissing(t, jan.New(), "jan", models)
}

func TestModelYAML(t *testing.T) {
	home := fixture.Home(t, "jan")
	o := fixture.Observe(t, jan.New(), fixture.Env(home, nil), "jan")
	fixture.Equal(t, "errors", fixture.ErrorKinds(o), []string{"malformed_index broken/model.yml"})
	fixture.Equal(t, "names", fixture.Names(o), []string{
		"jan:broken/model.gguf", "jan:mini", "jan:orphan/model.gguf", "jan:vision",
	})
	mini := fixture.MustManifest(t, o, "jan", "mini")
	fixture.Equal(t, "mini roles", fixture.Roles(mini), []string{"weights"})
	fixture.Equal(t, "mini holders", fixture.Holders(mini), []string{"jan"})
	vision := fixture.MustManifest(t, o, "jan", "vision")
	fixture.Equal(t, "vision roles", fixture.Roles(vision), []string{"weights", "projector"})
	fixture.Equal(t, "vision holders", fixture.Holders(vision), []string{"jan"})
	fixture.Equal(t, "orphan holders", fixture.Holders(fixture.MustManifest(t, o, "jan", "orphan/model.gguf")), []string(nil))
	fixture.Equal(t, "broken holders", fixture.Holders(fixture.MustManifest(t, o, "jan", "broken/model.gguf")), []string(nil))
	if len(o.Objects) != 5 || o.OtherFiles != 3 {
		t.Fatalf("objects %v other %d", fixture.Locators(o), o.OtherFiles)
	}
}
