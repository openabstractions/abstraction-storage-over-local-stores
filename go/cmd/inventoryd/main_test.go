package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
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
