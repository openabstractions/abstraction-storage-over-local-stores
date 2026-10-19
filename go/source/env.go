package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Env is what discovery reads about the machine. Tests build one over a
// synthetic home; FromOS builds the real one.
type Env struct {
	GOOS   string
	Home   string
	Getenv func(string) string
	Config Config
	// Now stamps observed_at; zero means time.Now.
	Now func() time.Time
	// Headers caches the GGUF headers read for descriptors (descriptor.go).
	// Nil reads no header, and every descriptor is then what a store's own
	// name says.
	Headers *Headers
	// Digests records the whole-file digests LS-12 takes of pinned
	// candidates (digests.go). Nil hashes nothing, and a store read under
	// LS-12 then lists nothing.
	Digests *Digests
	// IgnoreEnvironmentRoots makes the extra-root adapter omit only
	// ABSTRACTION_MODEL_ROOTS. By itself it does not change which adapters
	// inventoryd runs; serve's --config-roots-only selects just extra-root.
	IgnoreEnvironmentRoots bool
}

// Config is the operator's configuration for stores with no fixed location.
type Config struct {
	// ComfyUI lists ComfyUI installation directories (the directory holding
	// main.py and models/).
	ComfyUI []string `json:"comfyui"`
	// SwarmUI lists SwarmUI installation directories (holding Data/Settings.fds).
	SwarmUI []string `json:"swarmui"`
	// StabilityMatrix lists library directories of portable installations
	// (holding settings.json), beside the one library.json names.
	StabilityMatrix []string `json:"stability_matrix"`
	// Pinokio lists Pinokio home directories beside the one ~/.pinokio names.
	Pinokio []string `json:"pinokio"`
	// FastFlowLM lists model directories chosen at installation.
	FastFlowLM []string `json:"fastflowlm"`
	// ExtraRoots are directories the operator names as stores.
	ExtraRoots []ExtraRoot `json:"extra_roots"`
}

// ExtraRoot is one operator-named store. Holder, when set, is the program the
// operator says uses it; it is reported with the operator as the basis.
type ExtraRoot struct {
	Store  string `json:"store"`
	Path   string `json:"path"`
	Holder string `json:"holder"`
	// ExpectedFileKey, when present, requires Path to keep naming this exact
	// directory. The extra adapter reports an error instead of reading a
	// replacement directory.
	ExpectedFileKey string `json:"expected_file_key,omitempty"`
}

// LoadConfig reads an operator configuration file. An empty path is no
// configuration.
func LoadConfig(path string) (Config, error) {
	var c Config
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// FromOS is the environment of the running process.
func FromOS(c Config) Env {
	//unchecked: a failure just leaves Home empty, which callers already treat as "no home directory"
	home, _ := os.UserHomeDir()
	e := Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv, Config: c, Headers: NewHeaders()}
	e.Digests = NewDigests(e.DigestRecordPath())
	return e
}

func (e Env) env(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(e.Getenv(k))
}

// Var reads one environment variable.
func (e Env) Var(k string) string { return e.env(k) }

// At is the observation time.
func (e Env) At() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// RoamingConfig is where desktop applications keep per-user configuration:
// %APPDATA% on Windows, ~/Library/Application Support on macOS, and
// $XDG_CONFIG_HOME or ~/.config elsewhere (.NET's ApplicationData).
func (e Env) RoamingConfig() string {
	switch e.GOOS {
	case "windows":
		if v := e.env("APPDATA"); v != "" {
			return v
		}
		return filepath.Join(e.Home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(e.Home, "Library", "Application Support")
	default:
		if v := e.env("XDG_CONFIG_HOME"); v != "" {
			return v
		}
		return filepath.Join(e.Home, ".config")
	}
}

// DotnetAppData is .NET's Environment.SpecialFolder.ApplicationData, which is
// ~/.config (or $XDG_CONFIG_HOME) on macOS as well as Linux.
func (e Env) DotnetAppData() string {
	if e.GOOS == "darwin" {
		if v := e.env("XDG_CONFIG_HOME"); v != "" {
			return v
		}
		return filepath.Join(e.Home, ".config")
	}
	return e.RoamingConfig()
}

// LocalData is %LOCALAPPDATA% on Windows, ~/Library/Application Support on
// macOS and $XDG_DATA_HOME or ~/.local/share elsewhere.
func (e Env) LocalData() string {
	switch e.GOOS {
	case "windows":
		if v := e.env("LOCALAPPDATA"); v != "" {
			return v
		}
		return filepath.Join(e.Home, "AppData", "Local")
	case "darwin":
		return filepath.Join(e.Home, "Library", "Application Support")
	default:
		if v := e.env("XDG_DATA_HOME"); v != "" {
			return v
		}
		return filepath.Join(e.Home, ".local", "share")
	}
}

// Expand resolves ~ and environment references the way a program's own
// configuration loader would.
func (e Env) Expand(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		return e.Home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(e.Home, p[2:])
	}
	return os.Expand(p, func(k string) string {
		if strings.EqualFold(k, "HOME") || strings.EqualFold(k, "USERPROFILE") {
			return e.Home
		}
		return e.env(k)
	})
}

// IsDir reports whether p is an existing directory.
func IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// IsFile reports whether p is an existing regular file.
func IsFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func cleanRoot(root string) string { return filepath.Clean(root) }
