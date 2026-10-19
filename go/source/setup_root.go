package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
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
	// Files are the catalogue files a reserved Setup store holds, with the
	// digests Setup verified when it staged them (ExtraRoot.Files).
	Files []PinnedFile
	// Models are the catalogue's models by the files they are made of
	// (ExtraRoot.Models).
	Models []PinnedModel
}

// SetupPictureStore is the reserved store of OA's own picture model folder,
// where Setup stages every picture model it places.
const SetupPictureStore = "oa-picture"

type setupInventoryConfig struct {
	ExtraRoots []setupInventoryRootEntry `json:"extra_roots"`
}

type setupInventoryRootEntry struct {
	Store           string        `json:"store"`
	Path            string        `json:"path"`
	ExpectedFileKey string        `json:"expected_file_key"`
	Files           []PinnedFile  `json:"files,omitempty"`
	Models          []PinnedModel `json:"models,omitempty"`
}

// NewSetupInventoryRoot accepts only the fixed catalogue store, the fixed
// picture store or the identity-derived ModelHost chat-store namespace. It resolves the selected
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
	if err := validPinnedFiles(root.Files); err != nil {
		return nil, err
	}
	if err := validPinnedModels(root.Files, root.Models); err != nil {
		return nil, err
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
	config := setupInventoryConfig{ExtraRoots: []setupInventoryRootEntry{{Store: root.Store, Path: root.Path, ExpectedFileKey: root.ExpectedFileKey, Files: root.Files, Models: root.Models}}}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode inventory setup: %w", err)
	}
	return append(data, '\n'), nil
}

// validPinnedFiles refuses a pinned file that is not one clean relative
// path below the root with a size and a lowercase SHA-256, and a path named
// twice.
func validPinnedFiles(files []PinnedFile) error {
	seen := map[string]bool{}
	for _, f := range files {
		if f.Path == "" || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Path == ".." || strings.HasPrefix(f.Path, "../") ||
			strings.ContainsAny(f.Path, `\:`) || seen[f.Path] || f.Size < 1 || len(f.SHA256) != 64 ||
			strings.Trim(f.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("the Setup store's pinned file %q is not one clean relative path with a size and SHA-256", f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

// validPinnedModels refuses a model with no name, a name given twice, no
// files or a file the catalogue does not pin.
func validPinnedModels(files []PinnedFile, models []PinnedModel) error {
	pinned := NewPinned(files, nil)
	seen := map[string]bool{}
	for _, m := range models {
		if strings.TrimSpace(m.Name) == "" || len(m.Name) > 256 || seen[m.Name] || len(m.Files) == 0 {
			return fmt.Errorf("the Setup store's model %q is not one named model with files", m.Name)
		}
		seen[m.Name] = true
		for _, p := range m.Files {
			if _, ok := pinned.Files[p]; !ok {
				return fmt.Errorf("the Setup store's model %s names %q, which it does not pin", m.Name, p)
			}
		}
	}
	return nil
}

func setupInventoryStore(store string) bool {
	if store == "oa-catalogue" || store == SetupPictureStore {
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
