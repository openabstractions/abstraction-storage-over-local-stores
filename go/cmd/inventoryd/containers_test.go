package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, name string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

type containersDoc struct {
	Containers []struct {
		Path    string `json:"path"`
		Error   string `json:"error"`
		Members []struct {
			Name      string   `json:"name"`
			Inside    []string `json:"inside"`
			Method    string   `json:"method"`
			Size      int64    `json:"size"`
			Alignment int64    `json:"alignment"`
			InPlace   bool     `json:"in_place"`
			Format    string   `json:"format"`
			Model     bool     `json:"model"`
			Digest    string   `json:"digest"`
		} `json:"members"`
	} `json:"containers"`
	Copies []struct {
		Member string `json:"member"`
		File   string `json:"file"`
		Digest string `json:"digest"`
	} `json:"copies"`
	Hashed int64 `json:"hashed"`
	Short  bool  `json:"short"`
}

func TestContainersListsHashesAndFindsCopies(t *testing.T) {
	dir := t.TempDir()
	data := bytes.Repeat([]byte("bf16 "), 1000)
	pkg := filepath.Join(dir, "pkg.zip")
	writeZip(t, pkg, "diffusion.safetensors", data)
	loose := filepath.Join(dir, "AICreator")
	if err := os.MkdirAll(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loose, "diffusion.safetensors"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.zip")
	if err := os.WriteFile(broken, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"containers", "--json", pkg}, &out, &errOut, noEnv); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc containersDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	m := doc.Containers[0].Members[0]
	if m.Name != "diffusion.safetensors" || m.Method != "deflate" || m.Format != "safetensors" || !m.Model || m.Digest != "" || doc.Hashed != 0 {
		t.Fatalf("listing without --hash: %s", out.String())
	}

	out.Reset()
	if code := run([]string{"containers", "--json", "--hash", "1M", "--loose", loose, pkg, broken}, &out, &errOut, noEnv); code != 1 {
		t.Fatalf("a refused container must exit 1, got %d: %s", code, errOut.String())
	}
	doc = containersDoc{}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	sum := sha256.Sum256(data)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := doc.Containers[0].Members[0].Digest; got != want {
		t.Fatalf("digest %s want %s", got, want)
	}
	if !strings.Contains(doc.Containers[1].Error, "refused") {
		t.Fatalf("broken container %+v", doc.Containers[1])
	}
	if len(doc.Copies) != 1 || doc.Copies[0].Member != pkg+"!/diffusion.safetensors" || doc.Copies[0].Digest != want ||
		!strings.HasSuffix(doc.Copies[0].File, "diffusion.safetensors") {
		t.Fatalf("copies %+v", doc.Copies)
	}
	if doc.Hashed != 2*int64(len(data)) || doc.Short {
		t.Fatalf("hashed %d short %v", doc.Hashed, doc.Short)
	}

	out.Reset()
	if code := run([]string{"containers", "--json", "--hash", "10", pkg}, &out, &errOut, noEnv); code != 0 {
		t.Fatalf("exit %d", code)
	}
	doc = containersDoc{}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || !doc.Short || doc.Hashed != 0 {
		t.Fatalf("an allowance smaller than the member: %v %s", err, out.String())
	}
}

func TestContainersUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"containers", "--help"}, &out, &errOut, noEnv); code != 0 || !strings.Contains(out.String(), "--hash BYTES") {
		t.Fatalf("help %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"containers"}, &out, &errOut, noEnv); code != 0 || !strings.Contains(out.String(), "--loose DIR") {
		t.Fatalf("no arguments prints help: %d %q", code, out.String())
	}
	for _, args := range [][]string{{"containers", "x.zip"}, {"containers", "--json"}, {"containers", "--json", "--hash", "lots", "x.zip"}} {
		errOut.Reset()
		if code := run(args, &out, &errOut, noEnv); code != 2 {
			t.Fatalf("%v exit %d", args, code)
		}
	}
	out.Reset()
	if code := run(nil, &out, &errOut, noEnv); code != 0 || !strings.Contains(out.String(), "inventoryd containers --json") {
		t.Fatalf("top-level help lacks containers: %q", out.String())
	}
}
