package service

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	watch "github.com/openabstractions/abstraction-watch/go"
)

func TestSourceObserveBoundsWaitersAndRechecksAfterWake(t *testing.T) {
	s := New(func() snapshot.Result { return snapshot.Result{} })
	s.observe = watch.NewGate(1)
	refused := errors.New("binding withdrawn")
	var withdrawn atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := s.observeContext(t.Context(), "", 1, 5000, func() error {
			if withdrawn.Load() {
				return refused
			}
			return nil
		})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for s.observe.Waiting() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not enter gate")
		}
		time.Sleep(time.Millisecond)
	}
	if page, err := s.ObserveContext(t.Context(), "", 1, 1); err != nil || page.Outcome != content.SourcePageOutcomeUnavailable {
		t.Fatalf("full gate: %+v %v", page, err)
	}
	withdrawn.Store(true)
	s.observe.Advance()
	select {
	case err := <-done:
		if !errors.Is(err, refused) {
			t.Fatalf("wake authority: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("advance did not wake observer")
	}
}
