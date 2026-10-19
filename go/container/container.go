// Package container reads model files that live inside zip containers
// without extracting them (research/model-locators/DECISION.md §7 rule 1,
// storage-over-local-stores CONTRACT LS-8..LS-11).
//
// Members reads a zip's central directory and reports every entry it can name
// as a Member: its method, where its stored bytes begin, its sizes, CRC and
// the alignment of its data offset. A nested zip stored uncompressed is read
// in place the same way; a compressed nested zip is reported as one member.
// Members reads no entry's data.
//
// HashMembers additionally hashes each weight-format member, in place for a
// STORED member and by streaming decompression for a DEFLATE one, and streams
// each compressed nested zip to reach and hash its members. Nothing is ever
// written to disk. Every byte a hash or a decompressor reads is charged to an
// Allowance, so one call reads at most what its caller allows.
package container

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

// ErrZipRefused wraps every reason a container is not read: a malformed or
// truncated directory, a local header that disagrees with it, an entry whose
// bytes lie beyond the file, a CRC or size mismatch while hashing, and a data
// descriptor inside a streamed nested zip, whose sizes a stream cannot know.
var ErrZipRefused = errors.New("container refused")

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrZipRefused, fmt.Sprintf(format, args...))
}

const (
	// maxMembers bounds the members one zip may name; a directory naming
	// more is refused before any member is reported.
	maxMembers = 65536
	// maxDepth bounds how deep STORED nested zips are read in place.
	maxDepth = 4
	// separator joins a container's locator to the member names inside it.
	separator = "!/"
)

// Member is one entry of a zip container.
type Member struct {
	// Name is the entry's name inside its own zip.
	Name string
	// Inside names the nested zips that hold this member, outermost first;
	// it is empty for a member of the container file itself.
	Inside []string
	// Method is zip.Store or zip.Deflate.
	Method uint16
	// Offset is where the member's stored bytes begin. For an InPlace member
	// it is an offset in the container file; for a member of a streamed
	// nested zip it is an offset inside that nested zip's own bytes.
	Offset int64
	// FileOffset is where the member's stored bytes begin in the container
	// file, for every member whose zips are all STORED: equal to Offset for
	// an InPlace member, and the start of its compressed stream for a
	// DEFLATE one. It is -1 for a member of a streamed nested zip, whose
	// stored bytes exist only inside a decompressed stream.
	FileOffset int64
	// Packed is the stored (compressed) size; Size the uncompressed size.
	Packed, Size int64
	CRC32        uint32
	// Alignment is the largest of 65536, 4096 and 32 that divides Offset,
	// and 1 when none does.
	Alignment int64
	// InPlace says bytes [Offset, Offset+Size) of the container file are
	// exactly this member's uncompressed bytes: it is STORED, unencrypted,
	// and every zip around it is STORED too.
	InPlace bool
	// Format is the weight format the name states: gguf, safetensors, onnx,
	// pickle (.pth, .pt, .ckpt, .bin, .pkl, .pickle) or zip for a nested container; empty
	// for every other file.
	Format string
	// Digest is "sha256:" and the hex SHA-256 of the uncompressed bytes,
	// equal to the digest of the file the member would unpack to. Only
	// HashMembers fills it, and only for a weight-format member it hashed.
	Digest string
}

// Chain is the member's names from the container file inward.
func (m Member) Chain() []string { return append(slices.Clone(m.Inside), m.Name) }

// Model reports a format OA loads as a model. A pickle is never one.
func (m Member) Model() bool {
	return m.Format == "gguf" || m.Format == "safetensors" || m.Format == "onnx"
}

// Nested reports a member that is itself a zip container.
func (m Member) Nested() bool { return m.Format == "zip" }

// hashable is a member whose digest identifies a copy of weights: every
// weight format, pickles included, since a refused pickle still occupies a
// second copy on disk.
func (m Member) hashable() bool { return m.Format != "" && m.Format != "zip" }

func formatOf(name string) string {
	lower := strings.ToLower(name)
	for _, f := range []struct{ ext, format string }{
		{".gguf", "gguf"}, {".safetensors", "safetensors"}, {".sft", "safetensors"}, {".onnx", "onnx"},
		{".pth", "pickle"}, {".pt", "pickle"}, {".ckpt", "pickle"}, {".bin", "pickle"},
		{".pkl", "pickle"}, {".pickle", "pickle"}, {".zip", "zip"},
	} {
		if strings.HasSuffix(lower, f.ext) {
			return f.format
		}
	}
	return ""
}

func alignment(offset int64) int64 {
	for _, a := range []int64{65536, 4096, 32} {
		if offset%a == 0 {
			return a
		}
	}
	return 1
}

// nameable reports an entry name a locator can carry: valid UTF-8, no NUL,
// not a directory, and free of the separator MemberLocator uses.
func nameable(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsRune(name, 0) &&
		!strings.HasSuffix(name, "/") && !strings.Contains(name, separator)
}

// Allowance is what one call may read through a hash or a decompressor, in
// uncompressed bytes. A member or nested zip larger than what remains is
// skipped whole and Short is set; nothing is ever hashed in part.
type Allowance struct {
	Remaining int64
	Short     bool
}

func (a *Allowance) take(n int64) bool {
	if a == nil || n > a.Remaining {
		if a != nil {
			a.Short = true
		}
		return false
	}
	a.Remaining -= n
	return true
}

// visitFunc sees one member of a zip read through its directory, with the
// archive/zip file that opens it; listed says its own members follow.
type visitFunc func(m Member, f *zip.File, listed bool) error

// walk reads the directory of the zip in ra and visits each nameable member.
// base is the zip's own offset in the container file, and inPlace says every
// zip around it is STORED.
func walk(ra io.ReaderAt, size, base int64, within []string, inPlace bool, depth int, visit visitFunc) error {
	where := strings.Join(within, separator)
	r, err := zip.NewReader(ra, size)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && r != nil) {
		return refused("%s directory: %v", whereOr(where), err)
	}
	if len(r.File) > maxMembers {
		return refused("%s names %d members; at most %d are read", whereOr(where), len(r.File), maxMembers)
	}
	for _, f := range r.File {
		if !nameable(f.Name) || f.Flags&0x1 != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) {
			// A directory, an encrypted entry, a name no locator can carry
			// and an entry no reader here can open are not reported.
			continue
		}
		off, err := f.DataOffset()
		if err != nil {
			return refused("%s: %s local header: %v", whereOr(where), f.Name, err)
		}
		packed, unpacked := int64(f.CompressedSize64), int64(f.UncompressedSize64)
		if off < 0 || packed < 0 || unpacked < 0 || off > size || packed > size-off {
			return refused("%s: %s lies beyond the end of its zip", whereOr(where), f.Name)
		}
		if f.Method == zip.Store && packed != unpacked {
			return refused("%s: %s is stored with two different sizes", whereOr(where), f.Name)
		}
		here := inPlace && f.Method == zip.Store
		m := Member{Name: f.Name, Inside: slices.Clone(within), Method: f.Method, Offset: off, FileOffset: base + off,
			Packed: packed, Size: unpacked, CRC32: f.CRC32, InPlace: here, Format: formatOf(f.Name)}
		if here {
			m.Offset = base + off
		}
		m.Alignment = alignment(m.Offset)
		listed := m.Nested() && here && depth < maxDepth
		if err := visit(m, f, listed); err != nil {
			return err
		}
		if listed {
			if err := walk(io.NewSectionReader(ra, off, packed), packed, base+off, m.Chain(), true, depth+1, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func whereOr(where string) string {
	if where == "" {
		return "zip"
	}
	return where
}

func open(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		//unchecked: a read-only handle on a failed open path; the Stat error is what the caller is told
		f.Close()
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		//unchecked: a read-only handle on a refused path; the refusal is what the caller is told
		f.Close()
		return nil, 0, refused("%s is not a regular file", path)
	}
	return f, st.Size(), nil
}

// Members reads the central directory of the zip at path, and of each STORED
// nested zip in place, and reports every member it can name. It reads no
// member's data and inflates nothing.
func Members(path string) ([]Member, error) {
	f, size, err := open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Member
	err = walk(f, size, 0, nil, true, 0, func(m Member, _ *zip.File, _ bool) error {
		out = append(out, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HashMembers reports what Members reports, with the digest of every
// weight-format member the allowance covers, and with the members of each
// compressed nested zip, reached by streaming its decompressed bytes once.
// A STORED member is hashed in place; a DEFLATE member is inflated in memory
// and hashed as it streams; nothing is written. The members read before a
// refusal are returned with it.
func HashMembers(path string, allow *Allowance) ([]Member, error) {
	f, size, err := open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Member
	err = walk(f, size, 0, nil, true, 0, func(m Member, zf *zip.File, listed bool) error {
		switch {
		case m.Nested() && !listed:
			out = append(out, m)
			if !allow.take(m.Size) {
				return nil
			}
			rc, err := zf.Open()
			if err != nil {
				return refused("%s: %v", strings.Join(m.Chain(), separator), err)
			}
			defer rc.Close()
			return stream(rc, m.Size, m.Chain(), func(inner Member, body io.Reader) error {
				if inner.hashable() && allow.take(inner.Size) {
					d, err := digest(body, inner.Size)
					if err != nil {
						return refused("%s: %v", strings.Join(inner.Chain(), separator), err)
					}
					inner.Digest = d
				}
				out = append(out, inner)
				return nil
			})
		case m.hashable() && allow.take(m.Size):
			rc, err := zf.Open()
			if err != nil {
				return refused("%s: %v", strings.Join(m.Chain(), separator), err)
			}
			d, err := digest(rc, m.Size)
			//unchecked: closing a read-only member reader; digest already read it to its end and checked its CRC
			rc.Close()
			if err != nil {
				return refused("%s: %v", strings.Join(m.Chain(), separator), err)
			}
			m.Digest = d
		}
		out = append(out, m)
		return nil
	})
	return out, err
}

// digest hashes exactly size bytes from r and reads to its end, so a reader
// that checks a CRC at its end (archive/zip's, and stream's) checks it.
func digest(r io.Reader, size int64) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, size+1))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("read %d bytes of a member of %d", n, size)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// counted counts the bytes read through it, which is the stream offset.
type counted struct {
	r *bufio.Reader
	n int64
}

func (c *counted) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// checked yields exactly size bytes and verifies the CRC at their end.
type checked struct {
	r    io.Reader
	left int64
	want uint32
	crc  hash.Hash32
}

func (c *checked) Read(p []byte) (int, error) {
	if c.left == 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, fmt.Errorf("member is longer than its header says")
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		if c.crc.Sum32() != c.want {
			return 0, fmt.Errorf("CRC mismatch")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	//unchecked: hash.Hash32's Write never returns an error
	c.crc.Write(p[:n])
	c.left -= int64(n)
	if err == io.EOF && c.left > 0 {
		return n, io.ErrUnexpectedEOF
	}
	if err == io.EOF {
		err = nil
	}
	return n, err
}

// stream walks the local headers of a nested zip arriving as a stream of
// size bytes, and gives each nameable member's uncompressed bytes to visit,
// which may read them or not. Sizes come from each local header, so a data
// descriptor, which a stream cannot locate for a STORED member, is refused.
// The stream ends at the zip's central directory.
func stream(r io.Reader, size int64, within []string, visit func(Member, io.Reader) error) error {
	where := strings.Join(within, separator)
	in := &counted{r: bufio.NewReaderSize(io.LimitReader(r, size), 64<<10)}
	le16 := binary.LittleEndian.Uint16
	le32 := binary.LittleEndian.Uint32
	for count := 0; ; count++ {
		var sig [4]byte
		if _, err := io.ReadFull(in, sig[:]); err != nil {
			return refused("%s: no central directory: %v", where, err)
		}
		switch le32(sig[:]) {
		case 0x04034b50:
		case 0x02014b50, 0x06054b50, 0x06064b50:
			return nil
		default:
			return refused("%s: no local header at offset %d", where, in.n-4)
		}
		if count >= maxMembers {
			return refused("%s names more than %d members", where, maxMembers)
		}
		var h [26]byte
		if _, err := io.ReadFull(in, h[:]); err != nil {
			return refused("%s: truncated local header: %v", where, err)
		}
		flags, method := le16(h[2:]), le16(h[4:])
		crc, packed, unpacked := le32(h[10:]), int64(le32(h[14:])), int64(le32(h[18:]))
		name := make([]byte, le16(h[22:]))
		extra := make([]byte, le16(h[24:]))
		if _, err := io.ReadFull(in, name); err != nil {
			return refused("%s: truncated local header: %v", where, err)
		}
		if _, err := io.ReadFull(in, extra); err != nil {
			return refused("%s: truncated local header: %v", where, err)
		}
		if flags&0x8 != 0 {
			return refused("%s: %s carries a data descriptor, whose sizes a stream cannot read", where, name)
		}
		if packed == 0xFFFFFFFF || unpacked == 0xFFFFFFFF {
			var ok bool
			if unpacked, packed, ok = zip64Sizes(extra, unpacked, packed); !ok {
				return refused("%s: %s has no zip64 sizes", where, name)
			}
		}
		if packed < 0 || unpacked < 0 || packed > size-in.n {
			return refused("%s: %s lies beyond the end of its zip", where, name)
		}
		raw := &io.LimitedReader{R: in, N: packed}
		m := Member{Name: string(name), Inside: slices.Clone(within), Method: method, Offset: in.n, FileOffset: -1,
			Packed: packed, Size: unpacked, CRC32: crc, Format: formatOf(string(name))}
		m.Alignment = alignment(m.Offset)
		var body io.Reader
		switch {
		case !nameable(m.Name) || flags&0x1 != 0:
		case method == zip.Store:
			if packed != unpacked {
				return refused("%s: %s is stored with two different sizes", where, name)
			}
			body = &checked{r: raw, left: unpacked, want: crc, crc: crc32.NewIEEE()}
		case method == zip.Deflate:
			// flate's Close only repeats a read error the visitor already saw.
			body = &checked{r: flate.NewReader(raw), left: unpacked, want: crc, crc: crc32.NewIEEE()}
		}
		if body != nil {
			if err := visit(m, body); err != nil {
				return err
			}
		}
		// The rest of the member's stored bytes are skipped unread by any
		// decompressor: the local header gave their length.
		if _, err := io.Copy(io.Discard, raw); err != nil {
			return refused("%s: %s: %v", where, name, err)
		}
		if raw.N != 0 {
			return refused("%s: %s is truncated", where, name)
		}
	}
}

// zip64Sizes reads the zip64 extended information field of a local header,
// which holds the uncompressed then the compressed size for each field the
// header marked 0xFFFFFFFF.
func zip64Sizes(extra []byte, unpacked, packed int64) (int64, int64, bool) {
	for len(extra) >= 4 {
		id, n := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
		if 4+n > len(extra) {
			return 0, 0, false
		}
		field := extra[4 : 4+n]
		extra = extra[4+n:]
		if id != 1 {
			continue
		}
		next := func(v *int64) bool {
			if len(field) < 8 {
				return false
			}
			*v = int64(binary.LittleEndian.Uint64(field))
			field = field[8:]
			return *v >= 0
		}
		if unpacked == 0xFFFFFFFF && !next(&unpacked) {
			return 0, 0, false
		}
		if packed == 0xFFFFFFFF && !next(&packed) {
			return 0, 0, false
		}
		return unpacked, packed, true
	}
	return 0, 0, false
}

// Copy is a loose file whose bytes are a hashed member's: the same object
// held twice, once packed and once unpacked.
type Copy struct {
	// Member is the index of the matched member in the slice Copies was given.
	Member int
	// File is the loose file's path.
	File   string
	Digest string
}

// Copies hashes each of files whose size equals a hashed member's, within
// allow, and reports each one whose digest equals that member's. A file of
// no matching size is not opened.
func Copies(members []Member, files []string, allow *Allowance) ([]Copy, error) {
	bySize := map[int64][]int{}
	for i, m := range members {
		if m.Digest != "" {
			bySize[m.Size] = append(bySize[m.Size], i)
		}
	}
	var out []Copy
	for _, file := range files {
		st, err := os.Stat(file)
		if err != nil || !st.Mode().IsRegular() || len(bySize[st.Size()]) == 0 || !allow.take(st.Size()) {
			continue
		}
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		d, err := digest(f, st.Size())
		//unchecked: closing a read-only file digest already read to its end
		f.Close()
		if err != nil {
			return out, fmt.Errorf("%s: %w", file, err)
		}
		for _, i := range bySize[st.Size()] {
			if members[i].Digest == d {
				out = append(out, Copy{Member: i, File: file, Digest: d})
			}
		}
	}
	return out, nil
}

// MemberLocator names a member inside a container for an inventory object: the
// container file's own locator in its store, then each member name from the
// container inward, joined by "!/" as a Java jar URL joins them.
func MemberLocator(file string, chain ...string) string {
	return strings.Join(append([]string{file}, chain...), separator)
}

// SplitMemberLocator is MemberLocator's inverse. It answers false for a locator naming
// no member: one without the separator, one whose container part does not
// name a .zip file, and one with an empty name in its chain. A store whose
// loose file's path has the same spelling is told apart by the caller, who
// checks whether the container part is a regular file.
func SplitMemberLocator(locator string) (string, []string, bool) {
	parts := strings.Split(locator, separator)
	if len(parts) < 2 || !strings.HasSuffix(strings.ToLower(parts[0]), ".zip") {
		return "", nil, false
	}
	if slices.Contains(parts, "") {
		return "", nil, false
	}
	return parts[0], parts[1:], true
}
