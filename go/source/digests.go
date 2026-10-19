package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Digests is the record of whole-file SHA-256 digests this reader takes for
// LS-12, keyed by path and the opaque held-file stamp it had when hashed.
// The stamp includes identity, size and available change/modified/birth times.
// A file is hashed once, in the background; a changed stamp hashes
// it again, so a same-size replacement renamed in with the old time is
// hashed again too. The record lives in a folder private to this account
// in OA's own cache, never inside a store (LS-7), and survives a restart of
// the provider when it names a file. A nil *Digests hashes nothing.
type Digests struct {
	mu   sync.Mutex
	file string
	at   map[string]digestEntry
	// failed holds the last hashing failure per path, for the stamp it was
	// taken at; a new stamp queues the file again.
	failed  map[string]digestFailure
	queue   []digestEntry
	queued  map[string]bool
	working bool
	ran     bool
	idle    *sync.Cond
}

type digestEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Stamp    string `json:"stamp"`
	SHA256   string `json:"sha256"`
}

type digestFailure struct {
	key digestEntry
	err error
}

type digestDocument struct {
	Version int           `json:"version"`
	Files   []digestEntry `json:"files"`
}

// digestRecordVersion is the record file's format version; a file of
// another version is ignored and every pinned file is hashed again.
// Version 3 replaces identity-only metadata with the held-file change stamp.
// Older records are ignored and rehashed; their missing change time is unknown.
const digestRecordVersion = 3

// ErrPending reports a file queued for hashing: its digest is not known
// yet, and a later lookup answers it.
var ErrPending = errors.New("digest pending: the file is being hashed")

// NewDigests is a record kept in the JSON file at file, read now if it
// exists in a folder private to this account. An empty file keeps the
// record in memory for this process only. A record that cannot be read or
// parsed, or whose folder is not private, is ignored: the files it named
// are hashed again, which costs time and loses nothing.
func NewDigests(file string) *Digests {
	d := &Digests{file: file, at: map[string]digestEntry{}, failed: map[string]digestFailure{}, queued: map[string]bool{}}
	d.idle = sync.NewCond(&d.mu)
	if file == "" || privatedir.CheckDir(filepath.Dir(file)) != nil {
		return d
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return d
	}
	var doc digestDocument
	if json.Unmarshal(b, &doc) != nil || doc.Version != digestRecordVersion {
		return d
	}
	for _, e := range doc.Files {
		if e.Path != "" && e.Stamp != "" && len(e.SHA256) == sha256.Size*2 {
			d.at[e.Path] = e
		}
	}
	return d
}

// SHA256 is the lowercase hex SHA-256 of the regular file at path, which a
// caller's walk saw with the given size and modification time. It never
// reads the file's content itself: a record for the same path, size, time
// and held-file stamp answers at once, and any other file is queued for one
// background hash and answers ErrPending until that hash ends. A file that
// changed while it was hashed, or is no regular file of the stated size,
// answers the error that hash met until its stamp changes. Stamps are cache
// invalidators with identity.FileStamp's limitations, not content proofs.
func (d *Digests) SHA256(path string, size int64, modified time.Time) (string, error) {
	if d == nil {
		return "", errors.New("no digest record: pinned files are not hashed")
	}
	f, err := openShared(path)
	if err != nil {
		return "", err
	}
	stamp, _, err := identity.FileStamp(f)
	f.Close()
	if err != nil {
		return "", err
	}
	key := digestEntry{Path: path, Size: size, Modified: modified.UTC().Format(time.RFC3339Nano), Stamp: stamp}
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.at[path]; ok && sameFile(e, key) {
		return e.SHA256, nil
	}
	if f, ok := d.failed[path]; ok && sameFile(f.key, key) {
		return "", f.err
	}
	if !d.queued[path] {
		d.queued[path] = true
		d.queue = append(d.queue, key)
		if !d.working {
			d.working = true
			go d.work()
		}
	}
	return "", ErrPending
}

func sameFile(a, b digestEntry) bool {
	return a.Size == b.Size && a.Modified == b.Modified && a.Stamp == b.Stamp
}

// work hashes queued files one at a time, so a listing never waits on a
// read of many gigabytes and two listings never hash one file twice.
func (d *Digests) work() {
	restore := backgroundIO()
	defer restore()
	d.mu.Lock()
	for len(d.queue) > 0 {
		key := d.queue[0]
		d.queue = d.queue[1:]
		d.mu.Unlock()
		sum, err := hashFile(key)
		d.mu.Lock()
		delete(d.queued, key.Path)
		d.ran = true
		if err != nil {
			d.failed[key.Path] = digestFailure{key: key, err: err}
			continue
		}
		delete(d.failed, key.Path)
		key.SHA256 = sum
		d.at[key.Path] = key
		//unchecked: the digest is right whether or not the record file is written; a failed write loses only the record's survival across a restart of the provider
		d.save()
	}
	d.working = false
	d.idle.Broadcast()
	d.mu.Unlock()
}

// Recorded answers path's SHA-256 from the record alone: a record for the
// same path, size and time whose held stamp still matches. It queues
// nothing and opens the file only when a record names it, so a listing
// that asks it of every file hashes nothing (LS-6).
func (d *Digests) Recorded(path string, size int64, modified time.Time) (string, bool) {
	if d == nil {
		return "", false
	}
	at := modified.UTC().Format(time.RFC3339Nano)
	d.mu.Lock()
	e, ok := d.at[path]
	d.mu.Unlock()
	if !ok || e.Size != size || e.Modified != at {
		return "", false
	}
	f, err := openShared(path)
	if err != nil {
		return "", false
	}
	stamp, _, err := identity.FileStamp(f)
	f.Close()
	if err != nil || stamp != e.Stamp {
		return "", false
	}
	return e.SHA256, true
}

// Request queues one on-demand hash of the weights file at path (LS-13): a
// file on a local volume, of the given size and time, that the record does
// not answer yet. It returns at once; the hash runs in the background, at
// background IO priority on Windows, and Recorded answers it afterwards. A
// file on a remote or device volume is refused, and never read.
func (d *Digests) Request(path string, size int64, modified time.Time) error {
	if d == nil {
		return errors.New("no digest record: nothing is hashed")
	}
	if err := identity.LocalPath(path); err != nil {
		return fmt.Errorf("not hashed: %w", err)
	}
	if _, ok := d.Recorded(path, size, modified); ok {
		return nil
	}
	if _, err := d.SHA256(path, size, modified); err != nil && !errors.Is(err, ErrPending) {
		return err
	}
	return nil
}

// ContentDigest is the digest a listing shows for a weights file its store
// names by no SHA-256: the one an earlier on-demand hash recorded (LS-13),
// while the file's size, time and held stamp are unchanged; otherwise "" and
// evidence none. It hashes nothing. A partial download, a pickle and a file
// that is not weights have none.
func ContentDigest(env Env, full, rel string, size int64, modified time.Time, extraWeights ...string) (string, content.Evidence) {
	if env.Digests == nil || IsPartial(rel) || !IsWeights(rel, extraWeights...) || IsPickle(full, rel) {
		return "", content.EvidenceNone
	}
	sum, ok := env.Digests.Recorded(full, size, modified)
	if !ok {
		return "", content.EvidenceNone
	}
	return "sha256:" + sum, content.EvidenceHashed
}

// Wait blocks until no file is queued or being hashed, and reports whether
// any hash ended since the previous Wait. `inventoryd snapshot` waits once
// so its one listing names what it hashed; serve never waits.
func (d *Digests) Wait() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.working {
		d.idle.Wait()
	}
	ran := d.ran
	d.ran = false
	return ran
}

// Keep drops every record under root whose path is not in seen: a file a
// listing of root no longer found leaves the record.
func (d *Digests) Keep(root string, seen map[string]bool) {
	if d == nil {
		return
	}
	prefix := filepath.Clean(root) + string(filepath.Separator)
	d.mu.Lock()
	defer d.mu.Unlock()
	dropped := false
	for p := range d.at {
		if strings.HasPrefix(p, prefix) && !seen[p] {
			delete(d.at, p)
			dropped = true
		}
	}
	for p := range d.failed {
		if strings.HasPrefix(p, prefix) && !seen[p] {
			delete(d.failed, p)
		}
	}
	if dropped {
		//unchecked: as in work, a failed write loses only the record's survival across a restart
		d.save()
	}
}

// hashFile reads the whole file once, through a handle that lets another
// program delete or rename it meanwhile. The handle must be a regular file
// of the key's size, time and stamp before and after the read. A detected
// replacement or write is not recorded; FileStamp's metadata limits remain.
func hashFile(key digestEntry) (string, error) {
	f, err := openShared(key.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	same := func() error {
		stamp, st, err := identity.FileStamp(f)
		if err != nil {
			return err
		}
		switch {
		case !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", key.Path)
		case st.Size() != key.Size || st.ModTime().UTC().Format(time.RFC3339Nano) != key.Modified || stamp != key.Stamp:
			return fmt.Errorf("%s changed since it was listed", key.Path)
		}
		return nil
	}
	if err := same(); err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	if n != key.Size {
		return "", fmt.Errorf("%s: read %d bytes, listed %d", key.Path, n, key.Size)
	}
	if err := same(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// save writes the record into its private folder beside its final name and
// renames it into place, so a reader never sees half a record.
func (d *Digests) save() error {
	if d.file == "" {
		return nil
	}
	doc := digestDocument{Version: digestRecordVersion, Files: make([]digestEntry, 0, len(d.at))}
	for _, e := range d.at {
		doc.Files = append(doc.Files, e)
	}
	sort.Slice(doc.Files, func(i, j int) bool { return doc.Files[i].Path < doc.Files[j].Path })
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(d.file)
	if err := privatedir.Ensure(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".digests-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		//unchecked: the write already failed; closing and removing the temporary file is cleanup whose own failure adds nothing
		tmp.Close()
		//unchecked: cleanup of the temporary file after a failed write
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		//unchecked: cleanup of the temporary file after a failed close
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, d.file); err != nil {
		//unchecked: cleanup of the temporary file after a failed rename
		os.Remove(name)
		return err
	}
	return nil
}

// DigestRecordPath is where inventoryd keeps its digest record: a folder
// private to this account in the per-user cache class under LocalData,
// never inside a store.
func (e Env) DigestRecordPath() string {
	return filepath.Join(e.LocalData(), "OpenAbstractions", "providers", "inventoryd", "digests.json")
}
