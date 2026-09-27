package swarmui_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/swarmui"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

func TestModelRootList(t *testing.T) {
	home := fixture.Home(t, "swarmui")
	env := fixture.Env(home, nil)
	env.Config.SwarmUI = []string{filepath.Join(home, "SwarmUI")}
	d := swarmui.New().Discover(env)
	var got []string
	for _, c := range d.Claims {
		if c.Holder.Program != "swarmui" || c.Store != "swarmui" || c.Purpose != source.PurposeInstalled {
			t.Fatalf("claim %+v", c)
		}
		rel, _ := filepath.Rel(home, c.Path)
		got = append(got, filepath.ToSlash(rel))
	}
	sort.Strings(got)
	fixture.Equal(t, "model roots", got, []string{
		"AppData/Local/AMD/AI_Bundle/ComfyUI/ComfyUI/models",
		"SwarmUI/Models",
	})
	if len(d.Stores) != 0 {
		t.Fatalf("SwarmUI roots become stores through claims only: %+v", d.Stores)
	}
}

func TestDefaultModelRoot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Swarm", "Models"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, nil)
	env.Config.SwarmUI = []string{filepath.Join(home, "Swarm")}
	d := swarmui.New().Discover(env)
	if len(d.Claims) != 1 || !fixture.SamePath(d.Claims[0].Path, filepath.Join(home, "Swarm", "Models")) {
		t.Fatalf("no Settings.fds means ModelRoot Models: %+v", d.Claims)
	}
}

func TestUnreadableSettings(t *testing.T) {
	home := t.TempDir()
	// A directory where Settings.fds should be cannot be read as a file.
	if err := os.MkdirAll(filepath.Join(home, "Swarm", "Data", "Settings.fds"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, nil)
	env.Config.SwarmUI = []string{filepath.Join(home, "Swarm")}
	d := swarmui.New().Discover(env)
	if len(d.Claims) != 1 || len(d.Claims[0].Errors) != 1 || d.Claims[0].Errors[0].Kind != source.ErrorUnreadableIndex {
		t.Fatalf("claims %+v", d.Claims)
	}
}
