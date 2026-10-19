//go:build windows

package snapshot_test

import (
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/jan"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// With Jan's store on a drive identity refuses, the snapshot has one Jan
// store and no jan-external store counting its files twice (ID-F1).
func TestStoreOnARefusedDriveIsOneStore(t *testing.T) {
	home := fixture.Home(t, "jan")
	env := fixture.Env(home, nil)
	restore := identity.TreatDriveAsNetwork(home[0])
	defer restore()
	r := snapshot.Take(env, []source.Adapter{jan.New()})
	var names []string
	for _, s := range r.Stores {
		if strings.HasPrefix(s.Name, "jan") {
			names = append(names, s.Name)
		}
	}
	if len(names) != 1 || names[0] != "jan" {
		t.Fatalf("Jan stores %v, want [jan]", names)
	}
}
