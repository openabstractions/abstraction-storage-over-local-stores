package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestSameInodeRewriteRestoredTimeRehashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weights")
	write(t, path, []byte("first bytes"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDigests("")
	if _, err := d.SHA256(path, info.Size(), info.ModTime()); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	d.Wait()
	old, err := d.SHA256(path, info.Size(), info.ModTime())
	if err != nil {
		t.Fatal(err)
	}
	if d.Wait() {
		t.Fatal("unchanged cache hit queued another hash")
	}
	rewriteStampedFile(t, path, []byte("other bytes"))
	if _, err := d.SHA256(path, info.Size(), info.ModTime()); !errors.Is(err, ErrPending) {
		t.Fatal("stale digest returned", err)
	}
	d.Wait()
	got, err := d.SHA256(path, info.Size(), info.ModTime())
	sum := sha256.Sum256([]byte("other bytes"))
	want := hex.EncodeToString(sum[:])
	if err != nil || got != want || got == old {
		t.Fatal(got, old, err)
	}
}

func TestDigestVersionTwoRecordIsNotReused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weights")
	record := filepath.Join(dir, "private", "inventoryd", "digests.json")
	write(t, path, []byte("first bytes"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDigests(record)
	d.SHA256(path, info.Size(), info.ModTime())
	d.Wait()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"version": 3`, `"version": 2`, 1))
	if err := os.WriteFile(record, data, 0600); err != nil {
		t.Fatal(err)
	}
	if again := NewDigests(record); len(again.at) != 0 {
		t.Fatal("v2 digest admitted without change stamp")
	}
}

// LS-12: a lookup never reads the file; the background hash records the
// digest in a private folder, a malformed or non-private record is ignored,
// a stated size the file does not have fails that hash without a record,
// and a nil record hashes nothing.
func TestDigestsRecordSurvivesAndRecovers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "weights.safetensors")
	body := []byte("pinned bytes")
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	// A malformed record is ignored and replaced by a valid one, in a
	// folder made private to this account.
	record := filepath.Join(dir, "cache", "inventoryd", "digests.json")
	d := NewDigests(record)
	if _, err := d.SHA256(file, st.Size(), st.ModTime()); !errors.Is(err, ErrPending) {
		t.Fatalf("first lookup: %v", err)
	}
	if !d.Wait() {
		t.Fatal("no hash ran")
	}
	got, err := d.SHA256(file, st.Size(), st.ModTime())
	if err != nil || got != want {
		t.Fatalf("digest %q %v, want %q", got, err, want)
	}
	if again := NewDigests(record); len(again.at) != 1 || again.at[file].SHA256 != want || again.at[file].Stamp == "" {
		t.Fatalf("reloaded record %+v", again.at)
	}
	if err := os.WriteFile(record, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := NewDigests(record); len(again.at) != 0 {
		t.Fatalf("malformed record read: %+v", again.at)
	}

	// A record in a folder open to others is not trusted.
	open := filepath.Join(dir, "open")
	if err := os.MkdirAll(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(open, "digests.json"), []byte(`{"version":2,"files":[{"path":"`+strings.ReplaceAll(file, `\`, `\\`)+`","size":12,"modified":"x","file_id":"y","sha256":"`+want+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if again := NewDigests(filepath.Join(open, "digests.json")); len(again.at) != 0 {
		t.Fatalf("a record outside a private folder was read: %+v", again.at)
	}

	// A stated size the file does not have fails its hash and is not recorded.
	other := filepath.Join(dir, "other.safetensors")
	if err := os.WriteFile(other, body, 0o644); err != nil {
		t.Fatal(err)
	}
	ost, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SHA256(other, ost.Size()+1, ost.ModTime()); !errors.Is(err, ErrPending) {
		t.Fatalf("wrong size, first lookup: %v", err)
	}
	d.Wait()
	if _, err := d.SHA256(other, ost.Size()+1, ost.ModTime()); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("wrong size: %v", err)
	}
	if _, ok := d.at[other]; ok {
		t.Fatal("refused file was recorded")
	}

	// Keep drops what a listing no longer found.
	d.Keep(dir, map[string]bool{})
	if len(d.at) != 0 || len(d.failed) != 0 {
		t.Fatalf("after Keep: %+v %+v", d.at, d.failed)
	}

	var none *Digests
	if _, err := none.SHA256(file, st.Size(), st.ModTime()); err == nil {
		t.Fatal("a nil record hashed")
	}
}
