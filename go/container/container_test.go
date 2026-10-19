package container

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// item is one synthetic zip entry. align places a STORED entry's data on a
// multiple of align; misalign places it one byte past a multiple of 32.
// raw writes the entry without a data descriptor; otherwise archive/zip's
// streaming writer adds one, as every streaming zip writer does.
type item struct {
	name     string
	data     []byte
	deflate  bool
	align    int64
	misalign bool
	streamed bool
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func payload(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

func deflated(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// zipOf builds a zip in memory with archive/zip, placing STORED data where
// each item asks by Android's alignment extra field (0xD935).
func zipOf(t *testing.T, comment string, items ...item) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, it := range items {
		if it.streamed {
			method := zip.Store
			if it.deflate {
				method = zip.Deflate
			}
			fw, err := w.CreateHeader(&zip.FileHeader{Name: it.name, Method: method})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(it.data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		h := &zip.FileHeader{Name: it.name, CRC32: crc32.ChecksumIEEE(it.data), UncompressedSize64: uint64(len(it.data))}
		body := it.data
		if it.deflate {
			h.Method = zip.Deflate
			body = deflated(t, it.data)
		} else {
			h.Method = zip.Store
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			start := int64(buf.Len()) + 30 + int64(len(it.name)) + 6
			pad := int64(0)
			switch {
			case it.align > 0:
				pad = (it.align - start%it.align) % it.align
			case it.misalign:
				pad = (33 - start%32) % 32
			}
			extra := make([]byte, 6+pad)
			binary.LittleEndian.PutUint16(extra[0:], 0xD935)
			binary.LittleEndian.PutUint16(extra[2:], uint16(2+pad))
			binary.LittleEndian.PutUint16(extra[4:], uint16(max(it.align, 1)))
			h.Extra = extra
		}
		h.CompressedSize64 = uint64(len(body))
		fw, err := w.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if comment != "" {
		if err := w.SetComment(comment); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func find(t *testing.T, members []Member, chain ...string) Member {
	t.Helper()
	for _, m := range members {
		if reflect.DeepEqual(m.Chain(), chain) {
			return m
		}
	}
	t.Fatalf("no member %q in %+v", chain, members)
	return Member{}
}

func plenty() *Allowance { return &Allowance{Remaining: 1 << 40} }

func TestStoredAlignedEntryIsARegion(t *testing.T) {
	data := payload(70000, 3)
	path := write(t, zipOf(t, "", item{name: "readme.txt", data: []byte("hello"), deflate: true},
		item{name: "models/tiny.gguf", data: data, align: 65536}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, members, "models/tiny.gguf")
	if m.Method != zip.Store || !m.InPlace || m.Offset%65536 != 0 || m.Alignment != 65536 {
		t.Fatalf("member %+v", m)
	}
	if m.Size != int64(len(data)) || m.Packed != m.Size || m.CRC32 != crc32.ChecksumIEEE(data) || m.Format != "gguf" || !m.Model() {
		t.Fatalf("member %+v", m)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw[m.Offset:m.Offset+m.Size], data) {
		t.Fatal("the region does not hold the entry's bytes")
	}
	if m.Digest != "" {
		t.Fatal("Members hashed an entry")
	}
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, hashed, "models/tiny.gguf").Digest; got != digestOf(data) {
		t.Fatalf("digest %s, the unpacked file's is %s", got, digestOf(data))
	}
	if got := find(t, hashed, "readme.txt").Digest; got != "" {
		t.Fatalf("a member that is no model was hashed: %s", got)
	}
}

func TestStoredAlignmentClasses(t *testing.T) {
	for _, tc := range []struct {
		align int64
		want  int64
	}{{4096, 4096}, {32, 32}} {
		data := payload(100, 1)
		path := write(t, zipOf(t, "", item{name: "a.txt", data: payload(5000, 9)}, item{name: "w.safetensors", data: data, align: tc.align}))
		members, err := Members(path)
		if err != nil {
			t.Fatal(err)
		}
		m := find(t, members, "w.safetensors")
		if m.Offset%tc.want != 0 || m.Alignment < tc.want || !m.InPlace {
			t.Fatalf("align %d: %+v", tc.align, m)
		}
	}
}

func TestStoredUnalignedEntry(t *testing.T) {
	data := payload(4096, 5)
	path := write(t, zipOf(t, "", item{name: "w.onnx", data: data, misalign: true}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, members, "w.onnx")
	if !m.InPlace || m.Offset%32 == 0 || m.Alignment != 1 || m.Format != "onnx" {
		t.Fatalf("member %+v", m)
	}
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, hashed, "w.onnx").Digest; got != digestOf(data) {
		t.Fatalf("digest %s", got)
	}
}

func TestDeflateEntryHashesByStreaming(t *testing.T) {
	data := bytes.Repeat([]byte("weights "), 20000)
	path := write(t, zipOf(t, "", item{name: "w.safetensors", data: data, deflate: true}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, members, "w.safetensors")
	if m.Method != zip.Deflate || m.InPlace || m.Size != int64(len(data)) || m.Packed >= m.Size {
		t.Fatalf("member %+v", m)
	}
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, hashed, "w.safetensors").Digest; got != digestOf(data) {
		t.Fatalf("digest %s", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("hashing wrote beside the container: %v", entries)
	}
}

func TestPickleIsRecordedAndRefusedAsModel(t *testing.T) {
	path := write(t, zipOf(t, "", item{name: "enc.pth", data: payload(64, 2)}, item{name: "pytorch_model.bin", data: payload(64, 4)},
		item{name: "old.pt", data: payload(64, 6), deflate: true}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"enc.pth", "pytorch_model.bin", "old.pt"} {
		m := find(t, members, name)
		if m.Format != "pickle" || m.Model() {
			t.Fatalf("%s: %+v", name, m)
		}
	}
}

func TestNestedDeflateZipIsReachedByStreaming(t *testing.T) {
	inner1 := payload(3000, 8)
	inner2 := bytes.Repeat([]byte("vae "), 5000)
	inner := zipOf(t, "", item{name: "diffusion.safetensors", data: inner1, align: 64}, item{name: "Wan2.2_VAE.pth", data: inner2, deflate: true},
		item{name: "vae/decoder.safetensors", data: inner2, deflate: true})
	path := write(t, zipOf(t, "", item{name: "version.ini", data: []byte("[x]")}, item{name: "model.zip", data: inner, deflate: true}))

	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	nested := find(t, members, "model.zip")
	if !nested.Nested() || nested.InPlace || nested.Format != "zip" {
		t.Fatalf("nested %+v", nested)
	}
	for _, m := range members {
		if len(m.Inside) > 0 {
			t.Fatalf("Members inflated a compressed nested zip: %+v", m)
		}
	}

	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	d := find(t, hashed, "model.zip", "diffusion.safetensors")
	if d.Digest != digestOf(inner1) || d.InPlace || d.Method != zip.Store || d.Offset%64 != 0 {
		t.Fatalf("inner %+v", d)
	}
	if v := find(t, hashed, "model.zip", "vae/decoder.safetensors"); v.Digest != digestOf(inner2) || v.Method != zip.Deflate {
		t.Fatalf("inner %+v", v)
	}
	if p := find(t, hashed, "model.zip", "Wan2.2_VAE.pth"); p.Format != "pickle" || p.Model() || p.Digest != digestOf(inner2) {
		t.Fatalf("pickle %+v", p)
	}
}

// TestDeflateMemberStreamIsLocatedInTheFile: a DEFLATE member at the top
// level and inside a STORED nested zip reports where its compressed stream
// starts in the container file, and that stream decodes to the member; a
// member of a compressed nested zip has no such place (LS-8).
func TestDeflateMemberStreamIsLocatedInTheFile(t *testing.T) {
	data := payload(9000, 5)
	inner := zipOf(t, "", item{name: "deep.safetensors", data: data, deflate: true})
	packed := zipOf(t, "", item{name: "hidden.safetensors", data: data, deflate: true})
	path := write(t, zipOf(t, "",
		item{name: "top.safetensors", data: data, deflate: true},
		item{name: "inner.zip", data: inner, align: 4096},
		item{name: "packed.zip", data: packed, deflate: true}))
	raw, _ := os.ReadFile(path)
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, chain := range [][]string{{"top.safetensors"}, {"inner.zip", "deep.safetensors"}} {
		m := find(t, members, chain...)
		if m.InPlace || m.FileOffset <= 0 {
			t.Fatalf("%v: %+v", chain, m)
		}
		got, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw[m.FileOffset : m.FileOffset+m.Packed])))
		if err != nil || !bytes.Equal(got, data) || crc32.ChecksumIEEE(got) != m.CRC32 {
			t.Fatalf("%v: the stream at %d does not decode to the member: %v", chain, m.FileOffset, err)
		}
	}
	if m := find(t, members, "inner.zip"); m.FileOffset != m.Offset || !m.InPlace {
		t.Fatalf("a STORED member's file offset is its offset: %+v", m)
	}
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	if m := find(t, hashed, "packed.zip", "hidden.safetensors"); m.FileOffset != -1 {
		t.Fatalf("a streamed member has a file offset: %+v", m)
	}
}

func TestNestedStoredZipIsListedInPlace(t *testing.T) {
	data := payload(5000, 11)
	inner := zipOf(t, "", item{name: "unet.gguf", data: data, align: 64})
	path := write(t, zipOf(t, "", item{name: "inner.zip", data: inner, align: 4096}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, members, "inner.zip", "unet.gguf")
	if !m.InPlace || m.Offset%64 != 0 {
		t.Fatalf("member %+v", m)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw[m.Offset:m.Offset+m.Size], data) {
		t.Fatal("the nested region's offset is not absolute in the container file")
	}
}

func TestAllowanceBoundsTheBytesHashed(t *testing.T) {
	a, b := payload(1000, 1), payload(3000, 2)
	path := write(t, zipOf(t, "", item{name: "a.gguf", data: a}, item{name: "b.gguf", data: b}))
	allow := &Allowance{Remaining: 2000}
	hashed, err := HashMembers(path, allow)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, hashed, "a.gguf").Digest != digestOf(a) || find(t, hashed, "b.gguf").Digest != "" {
		t.Fatalf("hashed %+v", hashed)
	}
	if !allow.Short || allow.Remaining != 1000 {
		t.Fatalf("allowance %+v", allow)
	}

	inner := zipOf(t, "", item{name: "w.gguf", data: b})
	nested := write(t, zipOf(t, "", item{name: "n.zip", data: inner, deflate: true}))
	allow = &Allowance{Remaining: int64(len(inner)) - 1}
	hashed, err = HashMembers(nested, allow)
	if err != nil {
		t.Fatal(err)
	}
	if !allow.Short || len(hashed) != 1 {
		t.Fatalf("a nested zip larger than the allowance was streamed: %+v %+v", allow, hashed)
	}
}

func TestCommentAndDataDescriptorInTheDirectory(t *testing.T) {
	data := payload(2048, 13)
	path := write(t, zipOf(t, "packaged by a tool, with a comment", item{name: "w.gguf", data: data, deflate: true, streamed: true}))
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, hashed, "w.gguf").Digest; got != digestOf(data) {
		t.Fatalf("digest %s", got)
	}
}

func TestDataDescriptorInAStreamedZipIsRefused(t *testing.T) {
	inner := zipOf(t, "", item{name: "w.gguf", data: payload(2048, 13), deflate: true, streamed: true})
	path := write(t, zipOf(t, "", item{name: "n.zip", data: inner, deflate: true}))
	_, err := HashMembers(path, plenty())
	if !errors.Is(err, ErrZipRefused) {
		t.Fatalf("err %v", err)
	}
}

// zip64Of hand-builds a one-entry STORED archive whose sizes and offsets are
// all carried by zip64 records.
func zip64Of(data []byte, name string) []byte {
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	crc := crc32.ChecksumIEEE(data)
	n := uint64(len(data))
	le(uint32(0x04034b50))
	le(uint16(45))
	le(uint16(0))
	le(uint16(0))
	le(uint32(0))
	le(crc)
	le(uint32(0xFFFFFFFF))
	le(uint32(0xFFFFFFFF))
	le(uint16(len(name)))
	le(uint16(20))
	b.WriteString(name)
	le(uint16(1))
	le(uint16(16))
	le(n)
	le(n)
	b.Write(data)
	cd := uint64(b.Len())
	le(uint32(0x02014b50))
	le(uint16(45))
	le(uint16(45))
	le(uint16(0))
	le(uint16(0))
	le(uint32(0))
	le(crc)
	le(uint32(0xFFFFFFFF))
	le(uint32(0xFFFFFFFF))
	le(uint16(len(name)))
	le(uint16(28))
	le(uint16(0))
	le(uint16(0))
	le(uint16(0))
	le(uint32(0))
	le(uint32(0xFFFFFFFF))
	b.WriteString(name)
	le(uint16(1))
	le(uint16(24))
	le(n)
	le(n)
	le(uint64(0))
	cdSize := uint64(b.Len()) - cd
	end64 := uint64(b.Len())
	le(uint32(0x06064b50))
	le(uint64(44))
	le(uint16(45))
	le(uint16(45))
	le(uint32(0))
	le(uint32(0))
	le(uint64(1))
	le(uint64(1))
	le(cdSize)
	le(cd)
	le(uint32(0x07064b50))
	le(uint32(0))
	le(end64)
	le(uint32(1))
	le(uint32(0x06054b50))
	le(uint16(0xFFFF))
	le(uint16(0xFFFF))
	le(uint16(0xFFFF))
	le(uint16(0xFFFF))
	le(uint32(0xFFFFFFFF))
	le(uint32(0xFFFFFFFF))
	le(uint16(0))
	return b.Bytes()
}

func TestZip64RecordsAreRead(t *testing.T) {
	data := payload(777, 21)
	path := write(t, zip64Of(data, "big.safetensors"))
	hashed, err := HashMembers(path, plenty())
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, hashed, "big.safetensors")
	if m.Size != 777 || !m.InPlace || m.Digest != digestOf(data) {
		t.Fatalf("member %+v", m)
	}
}

func TestMalformedContainersAreRefused(t *testing.T) {
	good := zipOf(t, "", item{name: "w.gguf", data: payload(4096, 1), align: 64})
	z64 := zip64Of(payload(100, 1), "w.gguf")
	badLocal := append([]byte(nil), good...)
	copy(badLocal[0:4], "XXXX")
	beyond := append([]byte(nil), good...)
	// The central directory's compressed size for the one entry, raised past the file's end.
	cd := bytes.LastIndex(beyond, []byte{0x50, 0x4b, 0x01, 0x02})
	binary.LittleEndian.PutUint32(beyond[cd+20:], 1<<30)
	binary.LittleEndian.PutUint32(beyond[cd+24:], 1<<30)
	for name, b := range map[string][]byte{
		"empty":              {},
		"not a zip":          []byte("this is not a zip file at all, only some text"),
		"truncated":          good[:len(good)-30],
		"bad local header":   badLocal,
		"entry beyond end":   beyond,
		"zip64 locator lost": append(append([]byte(nil), z64[:len(z64)-42]...), z64[len(z64)-22:]...),
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, b)
			if _, err := Members(path); !errors.Is(err, ErrZipRefused) {
				t.Fatalf("Members: %v", err)
			}
			if _, err := HashMembers(path, plenty()); !errors.Is(err, ErrZipRefused) {
				t.Fatalf("HashMembers: %v", err)
			}
		})
	}
}

func TestMalformedStreamedZipIsRefused(t *testing.T) {
	inner := zipOf(t, "", item{name: "w.gguf", data: payload(4096, 1)})
	copy(inner[0:4], "XXXX")
	path := write(t, zipOf(t, "", item{name: "n.zip", data: inner, deflate: true}))
	if _, err := HashMembers(path, plenty()); !errors.Is(err, ErrZipRefused) {
		t.Fatalf("err %v", err)
	}
}

func TestLocatorRoundTrip(t *testing.T) {
	loc := MemberLocator("asus/base.zip", "model.zip", "t5/umt5.safetensors")
	if loc != "asus/base.zip!/model.zip!/t5/umt5.safetensors" {
		t.Fatalf("locator %q", loc)
	}
	file, chain, ok := SplitMemberLocator(loc)
	if !ok || file != "asus/base.zip" || !reflect.DeepEqual(chain, []string{"model.zip", "t5/umt5.safetensors"}) {
		t.Fatalf("split %q %q %v", file, chain, ok)
	}
	for _, plain := range []string{"models/w.gguf", "odd!/w.gguf", "a.zip!/", "a.zip"} {
		if _, _, ok := SplitMemberLocator(plain); ok {
			t.Fatalf("%q split as a container locator", plain)
		}
	}
}

func TestUnnameableMembersAreSkipped(t *testing.T) {
	path := write(t, zipOf(t, "", item{name: "a!/b.gguf", data: payload(10, 1)}, item{name: "dir/", data: nil}, item{name: "ok.gguf", data: payload(10, 2)}))
	members, err := Members(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Name != "ok.gguf" {
		t.Fatalf("members %+v", members)
	}
}

func TestCopiesMatchLooseFilesByDigest(t *testing.T) {
	data := payload(5000, 17)
	other := payload(5000, 18)
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.zip")
	if err := os.WriteFile(path, zipOf(t, "", item{name: "w.safetensors", data: data, deflate: true}), 0o600); err != nil {
		t.Fatal(err)
	}
	same := filepath.Join(dir, "unpacked.safetensors")
	differs := filepath.Join(dir, "other.safetensors")
	os.WriteFile(same, data, 0o600)
	os.WriteFile(differs, other, 0o600)
	allow := plenty()
	members, err := HashMembers(path, allow)
	if err != nil {
		t.Fatal(err)
	}
	copies, err := Copies(members, []string{same, differs}, allow)
	if err != nil {
		t.Fatal(err)
	}
	want := []Copy{{Member: 0, File: same, Digest: digestOf(data)}}
	if !reflect.DeepEqual(copies, want) {
		t.Fatalf("copies %+v", copies)
	}
}
