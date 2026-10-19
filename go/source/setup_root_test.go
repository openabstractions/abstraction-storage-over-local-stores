package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupInventoryRootConfigReservesCatalogueAndPinsDirectory(t *testing.T) {
	base := t.TempDir()
	selected := filepath.Join(base, "models selected for setup")
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := NewSetupInventoryRoot(selected, "oa-catalogue")
	if err != nil {
		t.Fatal(err)
	}
	data, err := SetupInventoryRootConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ExtraRoots []struct {
			Store           string `json:"store"`
			Path            string `json:"path"`
			ExpectedFileKey string `json:"expected_file_key"`
		} `json:"extra_roots"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ExtraRoots) != 1 || config.ExtraRoots[0].Store != "oa-catalogue" ||
		config.ExtraRoots[0].Path != root.Path || config.ExtraRoots[0].ExpectedFileKey != root.ExpectedFileKey ||
		strings.Contains(string(data), "ABSTRACTION_MODEL_ROOTS") {
		t.Fatalf("Setup inventory config does not pin only the catalogue root: %s", data)
	}
	if _, err := NewSetupInventoryRoot(selected, "oa-user-chosen"); err == nil {
		t.Fatal("an unreserved store name was accepted")
	}
}

func TestSetupInventoryRootConfigRefusesDirectoryReplacement(t *testing.T) {
	base := t.TempDir()
	selected := filepath.Join(base, "models")
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := NewSetupInventoryRoot(selected, "oa-catalogue")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(selected, selected+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SetupInventoryRootConfig(root); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("replacement folder kept the saved identity: %v", err)
	}
}
