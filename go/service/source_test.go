package service_test

import (
	"os"
	"path/filepath"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

var every = []string{"ollama", "hf", "lmstudio", "comfyui", "fastflowlm", "jan", "stabilitymatrix", "pinokio", "swarmui", "extra"}

func machine(t *testing.T) (source.Env, *service.Source) {
	home := fixture.Home(t, every...)
	env := fixture.Env(home, nil)
	env.Config.SwarmUI = []string{filepath.Join(home, "SwarmUI")}
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "nas", Path: filepath.Join(home, "NAS", "models")}}
	return env, service.New(func() snapshot.Result { return snapshot.Take(env, snapshot.Adapters()) })
}

func TestDescribe(t *testing.T) {
	env, src := machine(t)
	d, err := src.Describe()
	if err != nil {
		t.Fatal(err)
	}
	want := snapshot.Take(env, snapshot.Adapters()).Description
	if d.Outcome != content.SourceDescriptionOutcomeDescribed || string(content.EncodeSourceDescription(&d)) != string(content.EncodeSourceDescription(&want)) {
		t.Fatalf("describe %+v", d)
	}
}

// Paging a snapshot returns every manifest, stray object and dangling
// reference exactly once, with stores only on the first page.
func TestSnapshotPages(t *testing.T) {
	env, src := machine(t)
	whole := snapshot.Take(env, snapshot.Adapters()).Page
	var got content.SourcePage
	continuation, pages := "", 0
	for {
		p, err := src.Snapshot(continuation, 7)
		if err != nil || p.Outcome != content.SourcePageOutcomePage {
			t.Fatalf("page %d: %+v %v", pages, p.Outcome, err)
		}
		if pages > 0 && len(p.Stores) != 0 {
			t.Fatalf("stores on page %d", pages)
		}
		if n := len(p.Manifests) + len(p.Objects) + len(p.Dangling); n > 7 || len(p.Changes) != 0 || p.Cursor == "" {
			t.Fatalf("page %d carries %d items, %d changes, cursor %q", pages, n, len(p.Changes), p.Cursor)
		}
		got.Stores = append(got.Stores, p.Stores...)
		got.Manifests = append(got.Manifests, p.Manifests...)
		got.Objects = append(got.Objects, p.Objects...)
		got.Dangling = append(got.Dangling, p.Dangling...)
		pages++
		if p.Complete {
			if p.Continuation != "" {
				t.Fatalf("complete page with continuation %q", p.Continuation)
			}
			break
		}
		continuation = p.Continuation
	}
	got.Outcome, got.Changes, got.Complete = content.SourcePageOutcomePage, []content.SourceChange{}, true
	if pages < 3 || string(content.EncodeSourcePage(&got)) != string(content.EncodeSourcePage(&whole)) {
		t.Fatalf("%d pages did not reassemble the observation", pages)
	}
	// A finished or unknown continuation is gap; limits outside 1..256 are invalid.
	if p, _ := src.Snapshot(continuation, 7); p.Outcome != content.SourcePageOutcomeGap {
		t.Fatalf("spent continuation: %s", p.Outcome)
	}
	if p, _ := src.Snapshot("nonsense", 7); p.Outcome != content.SourcePageOutcomeGap {
		t.Fatalf("unknown continuation: %s", p.Outcome)
	}
	for _, limit := range []int64{0, 257} {
		if p, _ := src.Snapshot("", limit); p.Outcome != content.SourcePageOutcomeInvalid {
			t.Fatalf("limit %d: %s", limit, p.Outcome)
		}
	}
}

// §5.4 (c): two passes on an unchanged tree report nothing; one added file is
// exactly one added change; a cursor from before a restart is gap.
func TestObserveReportsOneAddedFile(t *testing.T) {
	env, src := machine(t)
	first, _ := src.Observe("", 16, 0)
	if first.Outcome != content.SourcePageOutcomePage || len(first.Changes) != 0 || first.Cursor == "" {
		t.Fatalf("first observe %+v", first)
	}
	again, _ := src.Observe(first.Cursor, 16, 0)
	if again.Outcome != content.SourcePageOutcomePage || len(again.Changes) != 0 || again.Cursor != first.Cursor {
		t.Fatalf("unchanged tree reported %+v", again.Changes)
	}

	part := filepath.Join(env.Home, ".flm", "models", "Half-NPU", "second.q4nx.part")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _ := src.Observe(first.Cursor, 16, 2000)
	if changed.Outcome != content.SourcePageOutcomePage || len(changed.Changes) != 1 {
		t.Fatalf("one added file: %+v", changed.Changes)
	}
	c := changed.Changes[0]
	if c.Kind != content.SourceChangeKindAdded || c.Target != "fastflowlm/Half-NPU/second.q4nx.part" || c.Hold != nil || c.Store != nil || !changed.Complete {
		t.Fatalf("change %+v", c)
	}
	if after, _ := src.Observe(changed.Cursor, 16, 0); len(after.Changes) != 0 {
		t.Fatalf("change reported twice: %+v", after.Changes)
	}

	restarted := service.New(func() snapshot.Result { return snapshot.Take(env, snapshot.Adapters()) })
	if p, _ := restarted.Observe(changed.Cursor, 16, 0); p.Outcome != content.SourcePageOutcomeGap {
		t.Fatalf("cursor across a restart: %s", p.Outcome)
	}
	for _, bad := range [][2]int64{{0, 0}, {257, 0}, {1, -1}, {1, 30001}} {
		if p, _ := src.Observe("", bad[0], bad[1]); p.Outcome != content.SourcePageOutcomeInvalid {
			t.Fatalf("max %d wait %d: %s", bad[0], bad[1], p.Outcome)
		}
	}
}

// A new weights file in a tree store is a manifest added and a hold its
// program's tree proves.
func TestObserveReportsHeld(t *testing.T) {
	env, src := machine(t)
	start, _ := src.Observe("", 16, 0)
	p := filepath.Join(env.Home, "LMStudioModels", "acme", "New-GGUF", "New-Q4_K_M.gguf")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, _ := src.Observe(start.Cursor, 16, 2000)
	kinds := map[content.SourceChangeKind]int{}
	for _, c := range page.Changes {
		kinds[c.Kind]++
		if c.Kind == content.SourceChangeKindHeld && (c.Hold == nil || c.Hold.Holder.Program != "lmstudio" || c.Hold.Target != c.Target || c.Hold.Basis == nil) {
			t.Fatalf("held change %+v", c)
		}
	}
	if kinds[content.SourceChangeKindAdded] != 1 || kinds[content.SourceChangeKindHeld] != 1 || len(page.Changes) != 2 {
		t.Fatalf("changes %+v", page.Changes)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	gone, _ := src.Observe(page.Cursor, 16, 2000)
	kinds = map[content.SourceChangeKind]int{}
	for _, c := range gone.Changes {
		kinds[c.Kind]++
	}
	if kinds[content.SourceChangeKindRemoved] != 1 || kinds[content.SourceChangeKindReleased] != 1 {
		t.Fatalf("removal changes %+v", gone.Changes)
	}
}

// Verify re-reads one target: a manifest with its holds, and the dangling
// references expecting a digest.
func TestVerify(t *testing.T) {
	env, src := machine(t)
	whole := snapshot.Take(env, snapshot.Adapters()).Page
	var target content.ManifestHolders
	for _, m := range whole.Manifests {
		if len(m.Holds) >= 2 {
			target = m
			break
		}
	}
	p, _ := src.Verify(target.Manifest.ID)
	if p.Outcome != content.SourcePageOutcomePage || len(p.Manifests) != 1 || p.Manifests[0].Manifest.ID != target.Manifest.ID || len(p.Manifests[0].Holds) != len(target.Holds) || !p.Complete {
		t.Fatalf("verify %s: %+v", target.Manifest.ID, p)
	}
	missing := "sha256:" + fixture.Label("ollama-missing", 64)
	if p, _ := src.Verify(missing); len(p.Dangling) != 1 || p.Dangling[0].Holder.Program != "ollama" {
		t.Fatalf("verify dangling want: %+v", p.Dangling)
	}
	if p, _ := src.Verify("sha256:unknown"); p.Outcome != content.SourcePageOutcomePage || len(p.Manifests)+len(p.Objects)+len(p.Dangling) != 0 {
		t.Fatalf("unknown target %+v", p)
	}
	if p, _ := src.Verify(""); p.Outcome != content.SourcePageOutcomeInvalid {
		t.Fatalf("empty target %s", p.Outcome)
	}
	if r, _ := src.Remove(target.Manifest.ID); r.Outcome != content.RemoveOutcomeUnsupported {
		t.Fatalf("remove %s", r.Outcome)
	}
}

// Serving reads nothing it may not: the fixture tree is unchanged after every call.
func TestCallsWriteNothing(t *testing.T) {
	env, src := machine(t)
	before := fixture.State(t, env.Home)
	cursor, _ := src.Observe("", 1, 0)
	src.Describe()
	src.Snapshot("", 256)
	src.Observe(cursor.Cursor, 256, 300)
	src.Verify("sha256:x")
	if after := fixture.State(t, env.Home); after != before {
		t.Fatalf("a call changed the tree: %s", fixture.Changed(before, after))
	}
}
