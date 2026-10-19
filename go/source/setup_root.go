package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

// SetupInventoryRoot pins one Setup-approved local directory to a reserved
// inventory store. Store/provider registration and collision checks remain
// with the caller that owns the runtime declaration.
type SetupInventoryRoot struct {
	Store           string
	Path            string
	ExpectedFileKey string
}

type setupInventoryConfig struct {
	ExtraRoots []setupInventoryRootEntry `json:"extra_roots"`
}

type setupInventoryRootEntry struct {
	Store           string `json:"store"`
	Path            string `json:"path"`
	ExpectedFileKey string `json:"expected_file_key"`
}

// NewSetupInventoryRoot accepts only the fixed catalogue store or the
// identity-derived ModelHost chat-store namespace. It resolves the selected
// directory and records its filesystem identity for later validation.
func NewSetupInventoryRoot(path, store string) (SetupInventoryRoot, error) {
	if !setupInventoryStore(store) || strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return SetupInventoryRoot{}, errors.New("choose an absolute local folder and a reserved Setup inventory store")
	}
	if err := identity.LocalPath(path); err != nil {
		return SetupInventoryRoot{}, errors.New("choose a folder on a local drive, not a network or device path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return SetupInventoryRoot{}, errors.New("that folder cannot be opened")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !filepath.IsAbs(resolved) {
		return SetupInventoryRoot{}, errors.New("that folder does not resolve to an absolute local path")
	}
	if err := identity.LocalPath(resolved); err != nil {
		return SetupInventoryRoot{}, errors.New("that folder resolves to a network or device path")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return SetupInventoryRoot{}, errors.New("choose an existing folder")
	}
	fileKey, err := identity.FileKey(resolved)
	if err != nil {
		return SetupInventoryRoot{}, errors.New("that folder's local identity cannot be read")
	}
	if strings.HasPrefix(store, "oa-chat-") && "oa-chat-"+setupInventoryID(fileKey) != store {
		return SetupInventoryRoot{}, errors.New("the selected folder does not match its identity-derived store")
	}
	return SetupInventoryRoot{Store: store, Path: resolved, ExpectedFileKey: fileKey}, nil
}

// SetupInventoryRootConfig emits an inventoryd config for exactly one
// selected root. It refuses a directory replacement between approval and
// configuration write and never inherits ambient roots.
func SetupInventoryRootConfig(root SetupInventoryRoot) ([]byte, error) {
	if !setupInventoryStore(root.Store) || root.Path == "" || !filepath.IsAbs(root.Path) || root.ExpectedFileKey == "" {
		return nil, errors.New("invalid Setup inventory root")
	}
	current, err := NewSetupInventoryRoot(root.Path, root.Store)
	if err != nil {
		if strings.HasPrefix(root.Store, "oa-chat-") && strings.Contains(err.Error(), "identity-derived store") {
			return nil, errors.New("the selected folder identity changed; select it again")
		}
		return nil, err
	}
	if current.Path != root.Path || current.ExpectedFileKey != root.ExpectedFileKey {
		return nil, errors.New("the selected folder identity changed; select it again")
	}
	config := setupInventoryConfig{ExtraRoots: []setupInventoryRootEntry{{Store: root.Store, Path: root.Path, ExpectedFileKey: root.ExpectedFileKey}}}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode inventory setup: %w", err)
	}
	return append(data, '\n'), nil
}

func setupInventoryStore(store string) bool {
	if store == "oa-catalogue" {
		return true
	}
	if !strings.HasPrefix(store, "oa-chat-") {
		return false
	}
	value := strings.TrimPrefix(store, "oa-chat-")
	if len(value) != 24 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func setupInventoryID(fileKey string) string {
	sum := sha256.Sum256([]byte("directory-file:" + fileKey))
	return hex.EncodeToString(sum[:12])
}
