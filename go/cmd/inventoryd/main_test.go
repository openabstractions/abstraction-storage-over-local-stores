package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

func noEnv(string) string { return "" }

func TestHelpIsTheDefault(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, noEnv); code != 0 {
			t.Fatalf("%v exit %d", args, code)
		}
		// The top-level help names every command and documents --config.
		for _, want := range []string{"inventoryd snapshot --json", "inventoryd serve --endpoint", "--config FILE", "INVENTORYD_CONFIG", `"extra_roots"`, "stability_matrix", "ABSTRACTION_MODEL_ROOTS"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%v help lacks %q:\n%s", args, want, out.String())
			}
		}
	}
	for _, cmd := range []string{"snapshot", "serve"} {
		var out, errOut bytes.Buffer
		if code := run([]string{cmd, "--help"}, &out, &errOut, noEnv); code != 0 || !strings.Contains(out.String(), "--config") || !strings.Contains(out.String(), `"comfyui"`) {
			t.Fatalf("%s help %d %q", cmd, code, out.String())
		}
		if cmd == "serve" && !strings.Contains(out.String(), "--ignore-environment-roots") {
			t.Fatalf("serve help lacks its explicit root-boundary option: %q", out.String())
		}
		if cmd == "snapshot" && strings.Contains(out.String(), "--ignore-environment-roots") {
			t.Fatalf("snapshot help exposes a serve-only option: %q", out.String())
		}
	}
}

func TestServeEnvironmentCanOmitAmbientRootsWithoutChangingOtherCommands(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string {
		if k == "HOME" || k == "USERPROFILE" {
			return home
		}
		return ""
	}
	serve, err := serveEnvironment("", home, getenv, true)
	if err != nil {
		t.Fatal(err)
	}
	if !serve.IgnoreEnvironmentRoots {
		t.Fatal("serve did not preserve the explicit environment-root boundary")
	}
	snapshotEnv, err := environment("", home, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotEnv.IgnoreEnvironmentRoots {
		t.Fatal("legacy snapshot behavior silently changed")
	}
	// Both read GGUF headers, so a descriptor names the architecture and
	// the trained context.
	if serve.Headers == nil || snapshotEnv.Headers == nil {
		t.Fatal("inventoryd reads no GGUF header")
	}
}

func TestConfigRootsOnlySkipsDefaultAndAmbientSources(t *testing.T) {
	home := fixture.Home(t, "ollama", "extra")
	chosen := filepath.Join(home, "chosen")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	env := fixture.Env(home, map[string]string{
		"OLLAMA_MODELS":           filepath.Join(home, "AmbientOllama"),
		"HF_HOME":                 filepath.Join(home, "AmbientHuggingFace"),
		"ABSTRACTION_MODEL_ROOTS": "ambient=" + filepath.Join(home, "AmbientExtra"),
	})
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "chosen", Path: chosen}}
	env.IgnoreEnvironmentRoots = true

	result := snapshot.Take(env, serveAdapters(true))
	if len(result.Page.Stores) != 1 || result.Page.Stores[0].Name != "chosen" {
		t.Fatalf("config-roots-only discovered more than the approved folder: %+v", result.Page.Stores)
	}
	if len(result.Claims) != 0 {
		t.Fatalf("config-roots-only followed unrelated program claims: %+v", result.Claims)
	}
}

func TestUnknownCommandAndMissingFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"register"}, &out, &errOut, noEnv); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if code := run([]string{"snapshot"}, &out, &errOut, noEnv); code != 2 || !strings.Contains(errOut.String(), "--json") {
		t.Fatalf("snapshot without --json: exit %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"serve"}, &out, &errOut, noEnv); code != 2 || !strings.Contains(errOut.String(), "--endpoint") {
		t.Fatalf("serve without --endpoint: exit %d %q", code, errOut.String())
	}
}

func TestSnapshotJSONWithConfig(t *testing.T) {
	home := fixture.Home(t, "ollama", "extra")
	cfg := filepath.Join(t.TempDir(), "inventoryd.json")
	body := `{"extra_roots": [{"store": "nas", "path": "` + filepath.ToSlash(filepath.Join(home, "NAS", "models")) + `"}]}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "USERPROFILE", "HOME":
			return home
		}
		return ""
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"snapshot", "--json", "--config", cfg, "--home", home}, &out, &errOut, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc struct {
		Source json.RawMessage `json:"source"`
		Page   json.RawMessage `json:"page"`
		Stores []struct {
			Name string `json:"name"`
		} `json:"stores"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	desc, err := content.DecodeSourceDescription(doc.Source)
	if err != nil {
		t.Fatalf("source is not a SourceDescription: %v", err)
	}
	page, err := content.DecodeSourcePage(doc.Page)
	if err != nil {
		t.Fatalf("page is not a SourcePage: %v", err)
	}
	present := map[string]bool{}
	for _, s := range page.Stores {
		present[s.Name] = s.Present
	}
	if desc.Name != "inventoryd" || !present["ollama"] || !present["nas"] || len(doc.Stores) != len(page.Stores) || !page.Complete {
		t.Fatalf("snapshot %+v %+v", desc, present)
	}

	var bad bytes.Buffer
	if code := run([]string{"snapshot", "--json", "--config", filepath.Join(home, "missing.json")}, &out, &bad, getenv); code != 2 {
		t.Fatalf("missing config exit %d", code)
	}
}

// Naming is exercised through a real owned socket/pipe; no provider engine runs.
func TestLeafEndpointNamingAndReceiving(t *testing.T) {
	parent, err := os.MkdirTemp("", "st5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Setenv("TMPDIR", parent)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	name := fmt.Sprintf("st5-leaf-%d", os.Getpid())
	if address, err := serveEndpoint(name); address != "" || !errors.Is(err, bootstrap.ErrNoTrustedInstallation) {
		t.Fatalf("default name escaped test refusal: %q %v", address, err)
	}
	address := filepath.Join(parent, name+".sock")
	if runtime.GOOS == "windows" {
		address = `\\.\pipe\oa-test-` + name
	}

	if actual, err := serveEndpoint(address); err != nil || actual != address {
		t.Fatalf("explicit address changed: %q %v", actual, err)
	}
	for _, explicit := range []string{`/owned/path with spaces/name.sock`, `C:\owned\path with spaces\name.sock`, `\\.\pipe\owned.Name_test`} {
		if actual, err := serveEndpoint(explicit); err != nil || actual != explicit {
			t.Fatalf("explicit %q changed to %q: %v", explicit, actual, err)
		}
	}
	listener, err := listen.Listen(address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopListener := context.AfterFunc(ctx, func() { listener.Close() })
	defer stopListener()
	answered := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			answered <- err
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		var request [1]byte
		if _, err = io.ReadFull(conn, request[:]); err == nil {
			if request[0] != 'q' {
				err = fmt.Errorf("unexpected request %q", request[0])
			} else {
				_, err = conn.Write([]byte{'r'})
			}
		}
		answered <- err
	}()
	conn, err := listen.Dial(address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopConn()
	if _, err = conn.Write([]byte{'q'}); err != nil {
		t.Fatal(err)
	}
	var reply [1]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[0] != 'r' {
		t.Fatalf("reply %q: %v", reply, err)
	}
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
}
func TestLeafEndpointRefusesBeforeProviderWork(t *testing.T) {
	for _, name := range []string{"model.v1", "model_v1", "Model", "model name"} {
		var output, diagnostics bytes.Buffer
		code := run([]string{"serve", "--endpoint", name}, &output, &diagnostics, func(string) string { return "" })
		if code != 2 || !strings.Contains(diagnostics.String(), "[a-z0-9-]+") || !strings.Contains(diagnostics.String(), "explicit absolute endpoint path") || output.Len() != 0 {
			t.Fatalf("%q: code %d, output %q, diagnostics %q", name, code, output.String(), diagnostics.String())
		}
	}
	if runtime.GOOS != "windows" {
		// A short name resolves in the runtime directory below; test binaries
		// need the opt-in to resolve installed endpoints at all.
		t.Setenv("OA_TEST_ALLOW_INSTALLED_RUNTIME", "1")
		dir := t.TempDir()
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", dir)
		var output, diagnostics bytes.Buffer
		code := run([]string{"serve", "--endpoint", "leaf-v1"}, &output, &diagnostics, func(string) string { return "" })
		if code != 2 || !strings.Contains(diagnostics.String(), "private") || output.Len() != 0 {
			t.Fatalf("namespace: code %d, output %q, diagnostics %q", code, output.String(), diagnostics.String())
		}
	}
}
