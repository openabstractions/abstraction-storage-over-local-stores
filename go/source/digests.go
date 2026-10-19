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

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Digests is the record of whole-file SHA-256 digests this reader takes for
// LS-12, keyed by path and valid while the file keeps the size,
// modification time and file identity (volume serial number and file ID on
// Windows, device and inode elsewhere) it had when it was hashed. A file is
// hashed once, in the background; a changed size, time or identity hashes
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
	FileID   string `json:"file_id"`
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
// Version 2 added the file identity.
const digestRecordVersion = 2

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
		if e.Path != "" && e.FileID != "" && len(e.SHA256) == sha256.Size*2 {
			d.at[e.Path] = e
		}
	}
	return d
}

// SHA256 is the lowercase hex SHA-256 of the regular file at path, which a
// caller's walk saw with the given size and modification time. It never
// reads the file's content itself: a record for the same path, size, time
// and file identity answers at once, and any other file is queued for one
// background hash and answers ErrPending until that hash ends. A file that
// changed while it was hashed, or is no regular file of the stated size,
// answers the error that hash met until its size, time or identity changes.
func (d *Digests) SHA256(path string, size int64, modified time.Time) (string, error) {
	if d == nil {
		return "", errors.New("no digest record: pinned files are not hashed")
	}
	id, err := statID(path)
	if err != nil {
		return "", err
	}
	key := digestEntry{Path: path, Size: size, Modified: modified.UTC().Format(time.RFC3339Nano), FileID: id}
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
	return a.Size == b.Size && a.Modified == b.Modified && a.FileID == b.FileID
}

// work hashes queued files one at a time, so a listing never waits on a
// read of many gigabytes and two listings never hash one file twice.
func (d *Digests) work() {
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
// of the key's size, time and identity before and after the read, so a
// file replaced or written during the read is never recorded.
func hashFile(key digestEntry) (string, error) {
	f, err := openShared(key.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	same := func() error {
		st, err := f.Stat()
		if err != nil {
			return err
		}
		id, err := fileID(f)
		switch {
		case err != nil:
			return err
		case !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", key.Path)
		case st.Size() != key.Size || st.ModTime().UTC().Format(time.RFC3339Nano) != key.Modified || id != key.FileID:
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
