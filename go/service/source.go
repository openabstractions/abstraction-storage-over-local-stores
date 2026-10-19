// Package service serves abstraction.storage/inventory-source@1: Describe,
// Snapshot, Observe and Verify over what inventoryd's adapters observe, behind
// the shared identity-bound listener. The runtime service is its only intended
// caller; applications resolve inventory@1 and never read a source.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	cursorcodec "github.com/openabstractions/abstraction-watch/go/cursor"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	storage "github.com/openabstractions/abstraction-storage/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	watch "github.com/openabstractions/abstraction-watch/go"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// Limits the contract states for inventory-source@1 calls.
const (
	MaxLimit      = 256
	MaxChanges    = 256
	MaxWaitMs     = 30000
	maxSnapshots  = 8
	snapshotIdle  = 30 * time.Second
	journalLength = 4096
	pollInterval  = 250 * time.Millisecond
)

// Source implements content.InventorySource over one machine. Take observes
// the machine; each call that needs current state takes a new observation,
// which reads directory entries by lstat, the programs' own indexes and the
// key-value header of each GGUF weight file.
type Source struct {
	take  func() snapshot.Result
	now   func() time.Time
	epoch string
	// Hash requests one on-demand hash of a weights file (LS-13,
	// source.Digests.Request); nil hashes nothing.
	Hash func(path string, size int64, modified time.Time) error

	// TakeWait bounds how long a call waits for an observation still in
	// progress when an earlier one has finished; the call then answers with
	// that earlier one and the observation keeps going (observeNow). Zero is
	// defaultTakeWait.
	TakeWait time.Duration
	takeMu   sync.Mutex
	inflight *taking
	last     *snapshot.Result

	mu        sync.Mutex
	next      int
	snapshots map[string]*frozen
	state     map[string]entry
	synced    bool
	journal   []content.SourceChange
	sequence  int64
	observe   *watch.Gate
}

// defaultTakeWait is TakeWait's default: well inside a runtime's 60 s call
// budget, so a slow store never costs a caller its whole answer.
const defaultTakeWait = 20 * time.Second

// taking is one observation in progress; done closes when r is set.
type taking struct {
	done chan struct{}
	r    snapshot.Result
}

// observeNow is the current observation. Every caller shares the one in
// progress, and an observation runs to its end whoever stopped waiting for
// it, so a store that takes longer than a caller's deadline is read once and
// its result serves the next call. A caller waits for the observation in
// progress up to TakeWait when an earlier one has finished, then answers with
// that earlier one; with none finished yet it waits for this one. stale
// reports an answer from that earlier observation.
func (s *Source) observeNow() (r snapshot.Result, stale bool) {
	s.takeMu.Lock()
	t := s.inflight
	if t == nil {
		t = &taking{done: make(chan struct{})}
		s.inflight = t
		go func() {
			r := s.take()
			s.takeMu.Lock()
			t.r = r
			s.last = &t.r
			s.inflight = nil
			s.takeMu.Unlock()
			close(t.done)
		}()
	}
	last := s.last
	s.takeMu.Unlock()
	if last == nil {
		<-t.done
		return t.r, false
	}
	wait := s.TakeWait
	if wait <= 0 {
		wait = defaultTakeWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-t.done:
		return t.r, false
	case <-timer.C:
		return *last, true
	}
}

// markStale is stores with the error that says they come from an earlier
// observation taken at observedAt (storage.StaleObservationError), so a
// reader knows this answer did not read them now.
func markStale(stores []content.Store, observedAt string) []content.Store {
	out := make([]content.Store, len(stores))
	for i, st := range stores {
		st.Errors = append(slices.Clone(st.Errors), content.StoreError{Kind: content.StoreErrorKindOther,
			Locator: st.Locator, Detail: storage.StaleObservationError + observedAt})
		out[i] = st
	}
	return out
}

type frozen struct {
	page     content.SourcePage
	items    int
	lastUsed time.Time
}

// entry is one observed thing the journal tracks: a manifest, a stray object,
// a hold or a store, with the bytes that say whether it changed.
type entry struct {
	kind   string // manifest, object, hold, store
	target string
	value  string
	hold   *content.Hold
	store  *content.Store
}

// New serves observations take produces. A new Source starts a new epoch, so
// a cursor or continuation from an earlier process reads gap.
func New(take func() snapshot.Result) *Source {
	var b [8]byte
	//unchecked: crypto/rand.Read on the OS's own CSPRNG does not fail in practice; a worst-case all-zero epoch would still be a valid, if lower-entropy, epoch value
	_, _ = rand.Read(b[:])
	return &Source{take: take, now: time.Now, epoch: hex.EncodeToString(b[:]), snapshots: map[string]*frozen{}, observe: watch.NewGate(watch.DefaultWaiters)}
}

// Describe reports the source's stores, schemes and capabilities.
func (s *Source) Describe() (content.SourceDescription, error) {
	r, _ := s.observeNow()
	return r.Description, nil
}

// Snapshot pages through one frozen observation: stores on the first page,
// then manifests, stray objects and dangling references, limit items a page.
func (s *Source) Snapshot(continuation string, limit int64) (content.SourcePage, error) {
	if limit < 1 || limit > MaxLimit || len(continuation) > 256 {
		return refusal(content.SourcePageOutcomeInvalid), nil
	}
	var r snapshot.Result
	var stale bool
	if continuation == "" {
		// Observed before the lock: a slow store holds no other call.
		r, stale = s.observeNow()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	var id string
	var offset int
	if continuation == "" {
		s.syncLocked(r)
		s.next++
		id = s.epoch + "-" + strconv.Itoa(s.next)
		page := r.Page
		if stale {
			page.Stores = markStale(page.Stores, r.ObservedAt)
		}
		page.Cursor = s.cursorLocked()
		s.snapshots[id] = &frozen{page: page, items: len(page.Manifests) + len(page.Objects) + len(page.Dangling), lastUsed: s.now()}
		s.evictLocked()
	} else {
		var ok bool
		id, offset, ok = parseContinuation(continuation)
		if !ok {
			return refusal(content.SourcePageOutcomeGap), nil
		}
	}
	f, ok := s.snapshots[id]
	if !ok || offset > f.items {
		return refusal(content.SourcePageOutcomeGap), nil
	}
	f.lastUsed = s.now()
	out := content.SourcePage{
		Outcome:   content.SourcePageOutcomePage,
		Stores:    []content.Store{},
		Manifests: []content.ManifestHolders{},
		Objects:   []content.ObjectHolders{},
		Dangling:  []content.Dangling{},
		Changes:   []content.SourceChange{},
		Cursor:    f.page.Cursor,
	}
	if offset == 0 {
		out.Stores = append(out.Stores, f.page.Stores...)
	}
	end := offset + int(limit)
	if end > f.items {
		end = f.items
	}
	nm, no := len(f.page.Manifests), len(f.page.Objects)
	for i := offset; i < end; i++ {
		switch {
		case i < nm:
			out.Manifests = append(out.Manifests, f.page.Manifests[i])
		case i < nm+no:
			out.Objects = append(out.Objects, f.page.Objects[i-nm])
		default:
			out.Dangling = append(out.Dangling, f.page.Dangling[i-nm-no])
		}
	}
	if end == f.items {
		out.Complete = true
		delete(s.snapshots, id)
	} else {
		out.Continuation = (cursorcodec.Codec{Separator: ":"}).Encode(cursorcodec.Envelope{Epoch: id, Payload: strconv.Itoa(end)})
	}
	return out, nil
}

// Observe reports changes after cursor in journal order. An empty cursor
// starts at the current end. A cursor from another epoch, one older than the
// retained journal, or one past its end reads gap.
func (s *Source) Observe(cursor string, maxChanges, waitMs int64) (content.SourcePage, error) {
	return s.ObserveContext(context.Background(), cursor, maxChanges, waitMs)
}

// ObserveContext is Observe whose wait ends when ctx does.
func (s *Source) ObserveContext(ctx context.Context, cursor string, maxChanges, waitMs int64) (content.SourcePage, error) {
	return s.observeContext(ctx, cursor, maxChanges, waitMs, func() error { return nil })
}

func (s *Source) observeContext(ctx context.Context, cursor string, maxChanges, waitMs int64, recheck func() error) (content.SourcePage, error) {
	if maxChanges < 1 || maxChanges > MaxChanges || waitMs < 0 || waitMs > MaxWaitMs || len(cursor) > 256 {
		return refusal(content.SourcePageOutcomeInvalid), nil
	}
	r, _ := s.observeNow()
	s.mu.Lock()
	s.syncLocked(r)
	after, ok := s.parseCursorLocked(cursor)
	s.mu.Unlock()
	if !ok {
		return refusal(content.SourcePageOutcomeGap), nil
	}
	var page content.SourcePage
	outcome, err := s.observe.Wait(ctx, time.Duration(waitMs)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			r, _ := s.observeNow()
			s.mu.Lock()
			defer s.mu.Unlock()
			s.syncLocked(r)
			changes := s.changesAfterLocked(after, int(maxChanges))
			page = content.SourcePage{
				Outcome:   content.SourcePageOutcomePage,
				Stores:    []content.Store{},
				Manifests: []content.ManifestHolders{},
				Objects:   []content.ObjectHolders{},
				Dangling:  []content.Dangling{},
				Changes:   changes,
				Cursor:    s.cursorAtLocked(after),
				Complete:  true,
			}
			if n := len(changes); n > 0 {
				page.Cursor = s.cursorAtLocked(changes[n-1].Sequence)
				page.Complete = changes[n-1].Sequence == s.sequence
			}
			return len(changes) > 0, nil
		},
		Recheck:  recheck,
		Deadline: func() time.Time { return time.Now().Add(pollInterval) },
	})
	if ctx.Err() != nil {
		page := refusal(content.SourcePageOutcomePage)
		page.Cursor, page.Complete = s.cursorAtLocked(after), true
		return page, nil
	}
	if err != nil {
		return content.SourcePage{}, err
	}
	if outcome == watch.Busy {
		return refusal(content.SourcePageOutcomeUnavailable), nil
	}
	return page, nil
}

// Verify re-reads the programs' indexes now and reports one target: the
// manifests whose id is target or that name it as an entry digest, with
// their holds; stray objects carrying it as digest or store/locator; and
// dangling references expecting it or naming it.
func (s *Source) Verify(target string) (content.SourcePage, error) {
	if target == "" || len(target) > 1024 {
		return refusal(content.SourcePageOutcomeInvalid), nil
	}
	r, stale := s.observeNow()
	stores := slices.Clone(r.Page.Stores)
	if stale {
		stores = markStale(stores, r.ObservedAt)
	}
	s.mu.Lock()
	s.syncLocked(r)
	cursor := s.cursorLocked()
	s.mu.Unlock()
	page := content.SourcePage{
		Outcome:   content.SourcePageOutcomePage,
		Stores:    stores,
		Manifests: []content.ManifestHolders{},
		Objects:   []content.ObjectHolders{},
		Dangling:  []content.Dangling{},
		Changes:   []content.SourceChange{},
		Complete:  true,
		Cursor:    cursor,
	}
	names := map[string]bool{target: true}
	for _, m := range r.Page.Manifests {
		match := m.Manifest.ID == target
		for _, e := range m.Manifest.Entries {
			if e.Digest != "" && e.Digest == target {
				match = true
			}
		}
		if match {
			page.Manifests = append(page.Manifests, m)
			for _, n := range m.Manifest.Names {
				names[n.Name] = true
			}
			if m.Manifest.ID == target {
				s.requestHash(r, m.Manifest)
			}
		}
	}
	for _, o := range r.Page.Objects {
		if (o.Object.Digest != "" && o.Object.Digest == target) || ObjectTarget(o) == target {
			page.Objects = append(page.Objects, o)
		}
	}
	for _, d := range r.Page.Dangling {
		if (d.ExpectedDigest != "" && d.ExpectedDigest == target) || names[d.Reference] {
			page.Dangling = append(page.Dangling, d)
		}
	}
	return page, nil
}

// requestHash is LS-13's on-demand hash: Verify on a model manifest's own
// id asks for its weights entries that carry no digest yet to be hashed,
// in the background, on local volumes only. A caller that asks for one
// pinned file names the same-size candidates it found; nothing else in any
// store is hashed. A later listing shows the recorded digest on the entry.
func (s *Source) requestHash(r snapshot.Result, m content.Manifest) {
	if s.Hash == nil || m.Kind != source.KindModel {
		return
	}
	root := ""
	for _, st := range r.Page.Stores {
		if st.Name == m.Store {
			root = st.Locator
		}
	}
	if root == "" {
		return
	}
	for _, e := range m.Entries {
		if e.Role != source.RoleWeights || e.Digest != "" || e.Locator == "" || strings.Contains(e.Locator, "!/") {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(e.Locator))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() || info.Size() != e.Size {
			continue
		}
		//unchecked: a refused request (a remote volume, an unreadable file) leaves the entry without a digest, which is the answer the caller already has
		_ = s.Hash(full, info.Size(), info.ModTime())
	}
}

// Remove is unsupported: inventoryd lists no remove capability and never
// changes another program's store.
func (s *Source) Remove(target string) (content.RemoveResult, error) {
	if target == "" {
		return content.RemoveResult{Outcome: content.RemoveOutcomeInvalid, Holders: []content.Holder{}}, nil
	}
	return content.RemoveResult{Outcome: content.RemoveOutcomeUnsupported, Holders: []content.Holder{}}, nil
}

// ObjectTarget is how a change names a stray object: its store and locator,
// because a locator alone is relative to its store.
func ObjectTarget(o content.ObjectHolders) string { return o.Store + "/" + o.Object.Locator }

func refusal(outcome content.SourcePageOutcome) content.SourcePage {
	return content.SourcePage{
		Outcome:   outcome,
		Stores:    []content.Store{},
		Manifests: []content.ManifestHolders{},
		Objects:   []content.ObjectHolders{},
		Dangling:  []content.Dangling{},
		Changes:   []content.SourceChange{},
	}
}

func parseContinuation(c string) (string, int, bool) {
	envelope, ok := (cursorcodec.Codec{Separator: ":"}).Parse(c)
	if !ok || envelope.Epoch == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(envelope.Payload)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return envelope.Epoch, n, true
}

func (s *Source) expire() {
	now := s.now()
	for id, f := range s.snapshots {
		if now.Sub(f.lastUsed) > snapshotIdle {
			delete(s.snapshots, id)
		}
	}
}

func (s *Source) evictLocked() {
	for len(s.snapshots) > maxSnapshots {
		oldest := ""
		for id, f := range s.snapshots {
			if oldest == "" || f.lastUsed.Before(s.snapshots[oldest].lastUsed) {
				oldest = id
			}
		}
		delete(s.snapshots, oldest)
	}
}

func (s *Source) cursorLocked() string { return s.cursorAtLocked(s.sequence) }

func (s *Source) cursorAtLocked(seq int64) string {
	return (cursorcodec.Codec{Separator: "."}).Encode(cursorcodec.Envelope{Epoch: s.epoch, Payload: strconv.FormatInt(seq, 10)})
}

// parseCursorLocked returns the sequence after which changes are wanted.
func (s *Source) parseCursorLocked(cursor string) (int64, bool) {
	if cursor == "" {
		return s.sequence, true
	}
	envelope, ok := (cursorcodec.Codec{Separator: "."}).Parse(cursor)
	if !ok || !envelope.Bound(s.epoch, "") {
		return 0, false
	}
	seq, err := strconv.ParseInt(envelope.Payload, 10, 64)
	if err != nil || seq < 0 || seq > s.sequence {
		return 0, false
	}
	if len(s.journal) > 0 && seq < s.journal[0].Sequence-1 {
		return 0, false
	}
	return seq, true
}

func (s *Source) changesAfterLocked(after int64, max int) []content.SourceChange {
	out := []content.SourceChange{}
	for _, c := range s.journal {
		if c.Sequence > after {
			out = append(out, c)
			if len(out) == max {
				break
			}
		}
	}
	return out
}

// syncLocked compares an observation with the last one and journals the
// difference. The first observation of an epoch journals nothing.
func (s *Source) syncLocked(r snapshot.Result) {
	now := stateOf(r)
	if !s.synced {
		s.state, s.synced = now, true
		return
	}
	var keys []string
	for k := range s.state {
		keys = append(keys, k)
	}
	for k := range now {
		if _, ok := s.state[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		before, had := s.state[k]
		after, has := now[k]
		target := after.target
		if !has {
			target = before.target
		}
		switch {
		case had && has && before.value == after.value:
		case after.kind == "store" || before.kind == "store":
			st := after.store
			if !has {
				gone := *before.store
				gone.Present = false
				st = &gone
			}
			s.appendLocked(content.SourceChange{Kind: content.SourceChangeKindStoreChanged, Target: target, Store: st})
		case after.kind == "hold" || before.kind == "hold":
			if had && !has {
				s.appendLocked(content.SourceChange{Kind: content.SourceChangeKindReleased, Target: before.hold.Target, Hold: before.hold})
			}
			if has && !had {
				s.appendLocked(content.SourceChange{Kind: content.SourceChangeKindHeld, Target: after.hold.Target, Hold: after.hold})
			}
		default:
			if had {
				s.appendLocked(content.SourceChange{Kind: content.SourceChangeKindRemoved, Target: target})
			}
			if has {
				s.appendLocked(content.SourceChange{Kind: content.SourceChangeKindAdded, Target: target})
			}
		}
	}
	s.state = now
}

func (s *Source) appendLocked(c content.SourceChange) {
	s.sequence++
	c.Sequence = s.sequence
	s.journal = append(s.journal, c)
	if len(s.journal) > journalLength {
		s.journal = append([]content.SourceChange(nil), s.journal[len(s.journal)-journalLength:]...)
	}
}

func stateOf(r snapshot.Result) map[string]entry {
	out := map[string]entry{}
	for i := range r.Page.Stores {
		st := r.Page.Stores[i]
		out["store\x00"+st.Name] = entry{kind: "store", target: st.Name, value: string(content.EncodeStore(&st)), store: &st}
	}
	for _, m := range r.Page.Manifests {
		out["manifest\x00"+m.Manifest.Store+"\x00"+m.Manifest.ID] = entry{kind: "manifest", target: m.Manifest.ID, value: m.Manifest.ID}
		for i := range m.Holds {
			h := m.Holds[i]
			h.ObservedAt = r.ObservedAt
			out["hold\x00"+m.Manifest.Store+"\x00"+source.HoldKey(h)] = entry{kind: "hold", target: h.Target, value: h.Purpose + "\x00" + h.Lifetime.String(), hold: &h}
		}
	}
	for _, o := range r.Page.Objects {
		out["object\x00"+ObjectTarget(o)] = entry{kind: "object", target: ObjectTarget(o), value: fmt.Sprintf("%d\x00%s\x00%t", o.Object.Size, o.Object.Modified, o.Object.Partial)}
	}
	return out
}
