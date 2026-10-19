package service_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	storage "github.com/openabstractions/abstraction-storage/go"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
)

// observation is one take that names its store by n, so a test can tell
// which take a page came from.
func observation(n int) snapshot.Result {
	name := "store-" + string(rune('a'+n))
	return snapshot.Result{ObservedAt: "2026-11-01T14:00:0" + string(rune('0'+n)) + "Z", Page: content.SourcePage{Outcome: content.SourcePageOutcomePage,
		Stores: []content.Store{{Name: name, Present: true, Placement: content.PlacementLocal, Errors: []content.StoreError{}}}}}
}

func firstStore(t *testing.T, s *service.Source) string {
	t.Helper()
	name, _ := storeAndErrors(t, s)
	return name
}

func storeAndErrors(t *testing.T, s *service.Source) (string, []content.StoreError) {
	t.Helper()
	page, err := s.Snapshot("", 256)
	if err != nil || page.Outcome != content.SourcePageOutcomePage || len(page.Stores) != 1 {
		t.Fatalf("snapshot %+v %v", page, err)
	}
	return page.Stores[0].Name, page.Stores[0].Errors
}

// A store slower than a caller's deadline is read once and keeps being read
// whoever stopped waiting: two callers share the one observation in progress,
// and once one has finished a caller waits at most TakeWait for a newer one,
// then answers with the finished one while the newer one runs on and serves
// the next call.
func TestASlowStoreIsReadOnceAndServesTheNextCall(t *testing.T) {
	var takes atomic.Int32
	release := make(chan struct{}, 4)
	// A take still waiting when the test ends is let go.
	t.Cleanup(func() {
		for range 4 {
			select {
			case release <- struct{}{}:
			default:
			}
		}
	})
	s := service.New(func() snapshot.Result {
		n := int(takes.Add(1))
		<-release
		return observation(n)
	})
	s.TakeWait = 50 * time.Millisecond

	// No observation has finished: both callers wait for the one in progress.
	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = firstStore(t, s) }()
	}
	time.Sleep(50 * time.Millisecond)
	release <- struct{}{}
	wg.Wait()
	if takes.Load() != 1 || got[0] != "store-b" || got[1] != "store-b" {
		t.Fatalf("takes %d, stores %v; want one take both callers share", takes.Load(), got)
	}

	// The next observation is slow: the caller answers with the finished one
	// after TakeWait, and the observation goes on.
	start := time.Now()
	name, errs := storeAndErrors(t, s)
	if name != "store-b" {
		t.Fatalf("a slow observation answered %q, want the finished store-b", name)
	}
	// The answer says it comes from that earlier observation, and when.
	if len(errs) != 1 || errs[0].Detail != storage.StaleObservationError+"2026-11-01T14:00:01Z" {
		t.Fatalf("a stale answer's store errors %+v, want the earlier observation's time", errs)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the caller waited %v for a slow store", waited)
	}
	release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if name := firstStore(t, s); name == "store-c" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the slow observation's result never served a later call")
		}
		release <- struct{}{}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestVerifyReportsStaleAndUnreadableStoreEvidence(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	s := service.New(func() snapshot.Result {
		n := int(calls.Add(1))
		if n > 1 {
			<-release
		}
		r := observation(n)
		r.Page.Stores[0].Errors = []content.StoreError{{Kind: content.StoreErrorKindUnreadableIndex, Locator: "index", Detail: "unreadable"}}
		return r
	})
	s.TakeWait = 10 * time.Millisecond
	first, err := s.Verify("target")
	if err != nil || len(first.Stores) != 1 || len(first.Stores[0].Errors) != 1 || first.Stores[0].Errors[0].Kind != content.StoreErrorKindUnreadableIndex {
		t.Fatalf("verify lost the observation's unreadable index: %+v %v", first.Stores, err)
	}
	second, err := s.Verify("target")
	if err != nil || len(second.Stores) != 1 || len(second.Stores[0].Errors) != 2 ||
		second.Stores[0].Errors[1].Detail != storage.StaleObservationError+"2026-11-01T14:00:01Z" {
		t.Fatalf("verify reported stale evidence as current: %+v %v", second.Stores, err)
	}
}
