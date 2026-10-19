//go:build windows

package jan_test

import (
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/jan"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

// A Jan store on a drive identity refuses makes no claim on its own files:
// a path identity cannot place is neither inside nor outside (ID-F1).
func TestStoreOnARefusedDriveClaimsNoOwnFiles(t *testing.T) {
	home := fixture.Home(t, "jan")
	restore := identity.TreatDriveAsNetwork(home[0])
	d := jan.New().Discover(fixture.Env(home, nil))
	restore()
	// The drive is local again, so identity can place each claim.
	own := filepath.Join(home, filepath.FromSlash(models))
	for _, c := range d.Claims {
		inside, err := identity.Within(c.Path, own)
		if err != nil {
			t.Fatalf("Within(%s): %v", c.Path, err)
		}
		if inside {
			t.Errorf("claim on Jan's own file %s (store %s)", c.Path, c.Store)
		}
	}
}
