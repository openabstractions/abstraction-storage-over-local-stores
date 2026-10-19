package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/container"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const containersUsage = `Usage: inventoryd containers --json [--hash BYTES] [--loose DIR]... ZIP...

Reads each ZIP's central directory without extracting anything and prints
every member it names: method, data offset, sizes, CRC, the alignment of the
data offset (65536, 4096, 32 or 1), whether the bytes lie in the file as-is
(in_place), and the weight format the name states. A pickle (.pth, .pt,
.ckpt, .bin) is reported with model false. A nested zip stored uncompressed
is read in place; a compressed one is listed as one member.

  --json        print JSON (the only output format; required)
  --hash BYTES  also hash, reading at most BYTES (suffix K, M, G or T) through
                a hash or a decompressor in this call: each weight member is
                hashed in place when STORED and by streaming decompression
                when DEFLATE, and each compressed nested zip is streamed once
                to list and hash its members. Nothing is written to disk. A
                member larger than what remains is left without a digest and
                "short" is true. Default 0: list only.
  --loose DIR   with --hash, also hash each file under DIR whose size equals a
                hashed member's, and report it under "copies" when its digest
                is that member's: the same object, once packed, once unpacked.

A provider diagnostic, never an application interface. Exit 0 when every
container was read, 1 when one was refused (its "error" says why), 2 on a
usage error.
`

type memberJSON struct {
	Name      string   `json:"name"`
	Inside    []string `json:"inside,omitempty"`
	Method    string   `json:"method"`
	Offset    int64    `json:"offset"`
	Packed    int64    `json:"packed"`
	Size      int64    `json:"size"`
	CRC32     string   `json:"crc32"`
	Alignment int64    `json:"alignment"`
	InPlace   bool     `json:"in_place"`
	Format    string   `json:"format,omitempty"`
	Model     bool     `json:"model"`
	Digest    string   `json:"digest,omitempty"`
}

type containerJSON struct {
	Path    string       `json:"path"`
	Error   string       `json:"error,omitempty"`
	Members []memberJSON `json:"members"`
}

type copyJSON struct {
	Member string `json:"member"`
	File   string `json:"file"`
	Digest string `json:"digest"`
}

func methodName(m uint16) string {
	if m == zip.Store {
		return "stored"
	}
	return "deflate"
}

// byteCount reads a decimal count of bytes with an optional K, M, G or T
// suffix (powers of 1024).
func byteCount(s string) (int64, error) {
	mult := int64(1)
	if n := len(s); n > 0 {
		switch strings.ToUpper(s[n-1:]) {
		case "K":
			mult = 1 << 10
		case "M":
			mult = 1 << 20
		case "G":
			mult = 1 << 30
		case "T":
			mult = 1 << 40
		}
		if mult > 1 {
			s = s[:n-1]
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 || v > (1<<62)/mult {
		return 0, fmt.Errorf("--hash wants a count of bytes, such as 40G")
	}
	return v * mult, nil
}

type dirList []string

func (d *dirList) String() string     { return strings.Join(*d, ",") }
func (d *dirList) Set(s string) error { *d = append(*d, s); return nil }

func runContainers(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || wantsHelp(args) {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stdout, containersUsage)
		return 0
	}
	fs := flag.NewFlagSet("containers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, containersUsage) }
	asJSON := fs.Bool("json", false, "")
	hashArg := fs.String("hash", "0", "")
	var loose dirList
	fs.Var(&loose, "loose", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	limit, err := byteCount(*hashArg)
	switch {
	case !*asJSON:
		err = fmt.Errorf("--json is required")
	case fs.NArg() == 0 && err == nil:
		err = fmt.Errorf("name at least one zip")
	}
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd containers: %v\n\n%s", err, containersUsage)
		return 2
	}

	allow := &container.Allowance{Remaining: limit}
	doc := struct {
		Containers []containerJSON `json:"containers"`
		Copies     []copyJSON      `json:"copies"`
		Hashed     int64           `json:"hashed"`
		Short      bool            `json:"short"`
	}{Containers: []containerJSON{}, Copies: []copyJSON{}}
	code := 0
	var hashed []container.Member
	var owners []string
	for _, path := range fs.Args() {
		var members []container.Member
		if limit > 0 {
			members, err = container.HashMembers(path, allow)
		} else {
			members, err = container.Members(path)
		}
		c := containerJSON{Path: path, Members: []memberJSON{}}
		if err != nil {
			c.Error = err.Error()
			code = 1
		}
		for _, m := range members {
			c.Members = append(c.Members, memberJSON{Name: m.Name, Inside: m.Inside, Method: methodName(m.Method),
				Offset: m.Offset, Packed: m.Packed, Size: m.Size, CRC32: fmt.Sprintf("%08x", m.CRC32),
				Alignment: m.Alignment, InPlace: m.InPlace, Format: m.Format, Model: m.Model(), Digest: m.Digest})
			hashed = append(hashed, m)
			owners = append(owners, path)
		}
		doc.Containers = append(doc.Containers, c)
	}
	if limit > 0 && len(loose) > 0 {
		var files []string
		for _, dir := range loose {
			walked, errs := source.Walk(dir)
			for _, e := range errs {
				//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
				fmt.Fprintf(stderr, "inventoryd containers: %s: %s: %s\n", dir, e.Locator, e.Detail)
			}
			for _, f := range walked {
				if !f.Link {
					files = append(files, filepath.Join(dir, filepath.FromSlash(f.Rel)))
				}
			}
		}
		copies, err := container.Copies(hashed, files, allow)
		if err != nil {
			//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
			fmt.Fprintf(stderr, "inventoryd containers: %v\n", err)
			code = 1
		}
		for _, c := range copies {
			doc.Copies = append(doc.Copies, copyJSON{Member: container.MemberLocator(owners[c.Member], hashed[c.Member].Chain()...), File: c.File, Digest: c.Digest})
		}
	}
	doc.Hashed = limit - allow.Remaining
	doc.Short = allow.Short
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd containers: %v\n", err)
		return 1
	}
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	stdout.Write(append(b, '\n'))
	return code
}
