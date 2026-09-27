package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
)

var serial atomic.Uint64

// liveHost serves the fixture machine on a temporary endpoint in this test
// process and returns a generated client bound to it. Nothing is registered.
func liveHost(t *testing.T, owner string) (*Host, *content.InventorySourceClient, string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program process/path evidence is not implemented on macOS")
	}
	home := fixture.Home(t, "ollama", "hf", "comfyui")
	env := fixture.Env(home, nil)
	src := New(func() snapshot.Result { return snapshot.Take(env, snapshot.Adapters()) })
	endpoint := listen.Endpoint(fmt.Sprintf("inventoryd-test-%d-%d", os.Getpid(), serial.Add(1)))
	h, err := Listen(endpoint, src)
	if err != nil {
		t.Fatal(err)
	}
	if owner != "" {
		h.owner = owner
	}
	var failures atomic.Int32
	h.OnError = func(error) { failures.Add(1) }
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("inventoryd host failed to stop")
		}
	})
	client := content.NewInventorySourceClient(listen.FrameClient{Endpoint: endpoint, Timeout: 40 * time.Second, MaxFrame: MaxFrameBytes})
	return h, client, home
}

func TestListenerServesTheContract(t *testing.T) {
	_, c, home := liveHost(t, "")
	d, err := c.Describe()
	if err != nil || d.Outcome != content.SourceDescriptionOutcomeDescribed || d.Name != "inventoryd" || len(d.Stores) == 0 {
		t.Fatalf("describe %+v %v", d, err)
	}
	var manifests, pages int
	continuation := ""
	for {
		p, err := c.Snapshot(continuation, 4)
		if err != nil || p.Outcome != content.SourcePageOutcomePage {
			t.Fatalf("snapshot %+v %v", p.Outcome, err)
		}
		manifests += len(p.Manifests)
		pages++
		if p.Complete {
			break
		}
		continuation = p.Continuation
	}
	if manifests == 0 || pages < 2 {
		t.Fatalf("%d manifests in %d pages", manifests, pages)
	}
	start, err := c.Observe("", 8, 0)
	if err != nil || start.Outcome != content.SourcePageOutcomePage {
		t.Fatalf("observe %+v %v", start, err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ollama", "models", "blobs", "sha256-"+fixture.Label("added-orphan", 64)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waited, err := c.Observe(start.Cursor, 8, 5000)
	if err != nil || len(waited.Changes) != 1 || waited.Changes[0].Kind != content.SourceChangeKindAdded {
		t.Fatalf("observe after an added blob %+v %v", waited, err)
	}
	v, err := c.Verify("sha256:" + fixture.Label("ollama-missing", 64))
	if err != nil || len(v.Dangling) != 1 {
		t.Fatalf("verify %+v %v", v, err)
	}
	if r, err := c.Remove("anything"); err != nil || r.Outcome != content.RemoveOutcomeUnsupported {
		t.Fatalf("remove %+v %v", r, err)
	}
	if p, err := c.Snapshot("", 0); err != nil || p.Outcome != content.SourcePageOutcomeInvalid {
		t.Fatalf("invalid limit over the wire %+v %v", p.Outcome, err)
	}
}

// A bound peer of another account reads forbidden on every call and learns
// nothing about the stores.
func TestListenerRefusesAnotherAccount(t *testing.T) {
	_, c, _ := liveHost(t, "a-different-principal")
	d, err := c.Describe()
	if err != nil || d.Outcome != content.SourceDescriptionOutcomeForbidden || len(d.Stores) != 0 || d.Name != "" {
		t.Fatalf("describe %+v %v", d, err)
	}
	for name, call := range map[string]func() (content.SourcePage, error){
		"snapshot": func() (content.SourcePage, error) { return c.Snapshot("", 10) },
		"observe":  func() (content.SourcePage, error) { return c.Observe("", 10, 0) },
		"verify":   func() (content.SourcePage, error) { return c.Verify("x") },
	} {
		p, err := call()
		if err != nil || p.Outcome != content.SourcePageOutcomeForbidden || len(p.Stores)+len(p.Manifests)+len(p.Objects)+len(p.Dangling)+len(p.Changes) != 0 {
			t.Fatalf("%s %+v %v", name, p, err)
		}
	}
	if r, err := c.Remove("x"); err != nil || r.Outcome != content.RemoveOutcomeForbidden {
		t.Fatalf("remove %+v %v", r, err)
	}
}

// An endpoint another listener holds is refused, never taken over.
func TestListenerRefusesTakenEndpoint(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Program process/path evidence is not implemented on macOS")
	}
	endpoint := listen.Endpoint(fmt.Sprintf("inventoryd-test-%d-%d", os.Getpid(), serial.Add(1)))
	src := New(func() snapshot.Result { return snapshot.Result{} })
	h, err := Listen(endpoint, src)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if second, err := Listen(endpoint, src); err == nil {
		second.Close()
		t.Fatal("a second listener took a held endpoint")
	} else if runtime.GOOS == "windows" && !errors.Is(err, listen.ErrTaken) {
		t.Fatalf("refusal %v", err)
	}
}

// Missing identity evidence is an error code on the reply channel, never an
// outcome that looks like a decision.
func TestIdentityErrorCodes(t *testing.T) {
	h := &Host{owner: "someone"}
	// A peer whose user attribute carries no proof.
	_, err := h.admits(&identity.Peer{})
	var se *content.ServiceError
	if !errors.As(err, &se) || se.Code != content.ServiceErrorCodeIdentityRequired {
		t.Fatalf("peer below the Program proof: %v", err)
	}
}
