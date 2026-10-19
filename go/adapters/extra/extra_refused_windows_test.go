//go:build windows

package extra_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

func requireLocalRootRefusal(t *testing.T, store content.Store) {
	t.Helper()
	if store.Present || len(store.Errors) != 1 || store.Errors[0].Kind != content.StoreErrorKindUnreadableConfiguration ||
		store.Errors[0].Locator != store.Locator || !strings.Contains(store.Errors[0].Detail, "a local folder is required") {
		t.Fatalf("root was not refused as configuration: %+v", store)
	}
}

func TestRemoteExtraRootsRefusedBeforeDiscoveryAndObservation(t *testing.T) {
	home := t.TempDir()
	for _, root := range []string{`\\nas.test\models`, `\\?\UNC\nas.test\models`, `Y:\models`} {
		t.Run(root, func(t *testing.T) {
			restore := identity.TreatDriveAsNetwork('Y')
			defer restore()
			var opened []string
			stop, ok := fileid.ObserveOpens(func(path string) { opened = append(opened, path) })
			if !ok {
				t.Fatal("identity open observer already held")
			}
			defer stop()
			env := fixture.Env(home, map[string]string{"ABSTRACTION_MODEL_ROOTS": "ambient=" + root})
			env.Config.ExtraRoots = []source.ExtraRoot{{Store: "configured", Path: root, Holder: "fixture", ExpectedFileKey: "must-not-be-read"}}
			adapter := extra.New()
			d := adapter.Discover(env)
			if len(d.Stores) != 2 {
				t.Fatalf("discovery %+v", d)
			}
			for _, store := range d.Stores {
				requireLocalRootRefusal(t, store)
				observed := adapter.Observe(store, env)
				if len(observed.Objects)+len(observed.Manifests)+len(observed.Dangling) != 0 {
					t.Fatalf("refused discovery was observed: %+v", observed)
				}
			}
			// An independent caller can supply a Store without discovery's error.
			// Observe must refuse before the configured identity recheck or walk.
			observed := adapter.Observe(content.Store{Name: "direct", Locator: root, Present: true}, env)
			requireLocalRootRefusal(t, observed.Store)
			if len(observed.Objects)+len(observed.Manifests)+len(observed.Dangling) != 0 || len(opened) != 0 {
				t.Fatalf("refused root opened or enumerated: %+v, opens %v", observed, opened)
			}
		})
	}
}

func TestMappedExtraRootCannotReachVerifyOrWeightRead(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "models")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.gguf"), []byte("fixture weight"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, nil)
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "chosen", Path: root}}
	take := func() snapshot.Result { return snapshot.Take(env, []source.Adapter{extra.New()}) }
	local := take()
	if len(local.Page.Manifests) != 1 {
		t.Fatalf("local root did not yield its model: %+v", local.Page)
	}
	id := local.Page.Manifests[0].Manifest.ID
	restore := identity.TreatDriveAsNetwork(home[0])
	defer restore()
	var opened []string
	stop, ok := fileid.ObserveOpens(func(path string) { opened = append(opened, path) })
	if !ok {
		t.Fatal("identity open observer already held")
	}
	defer stop()
	src := service.New(take)
	src.Hash = func(string, int64, time.Time) error {
		t.Error("Verify requested a refused weight read")
		return nil
	}
	page, err := src.Snapshot("", 256)
	if err != nil || len(page.Stores) != 1 || len(page.Objects)+len(page.Manifests) != 0 {
		t.Fatalf("refused snapshot %+v, %v", page, err)
	}
	requireLocalRootRefusal(t, page.Stores[0])
	verified, err := src.Verify(id)
	if err != nil || len(verified.Objects)+len(verified.Manifests) != 0 || len(opened) != 0 {
		t.Fatalf("refused Verify %+v, %v, opens %v", verified, err, opened)
	}
}
