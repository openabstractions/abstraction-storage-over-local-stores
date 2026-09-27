// Package swarmui reads SwarmUI's model roots from Data/Settings.fds.
package swarmui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/tree"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const program = "swarmui"

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (Adapter) Program() string { return program }

// Discover: SwarmUI has no fixed install location, so installations come from
// the operator. Paths.ModelRoot in <install>/Data/Settings.fds (default
// "Models") is a semicolon-separated list of folders, each relative to the
// install directory unless absolute. Every root is a claim by swarmui: a root
// inside another store (commonly ComfyUI's models folder) adds a holder there,
// and a root no store covers becomes the swarmui store.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	for _, dir := range env.Config.SwarmUI {
		install := env.Expand(dir)
		settings := filepath.Join(install, "Data", "Settings.fds")
		value, key := "Models", "Paths.ModelRoot default"
		var errs []content.StoreError
		b, err := os.ReadFile(settings)
		switch {
		case err == nil:
			if v, ok := fdsValue(string(b), "Paths", "ModelRoot"); ok {
				value, key = v, "Paths.ModelRoot"
			}
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, content.StoreError{Kind: source.ErrorUnreadableIndex, Locator: settings, Detail: err.Error()})
		}
		n := 0
		for _, root := range strings.Split(value, ";") {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			p := env.Expand(root)
			if !filepath.IsAbs(p) {
				p = filepath.Join(install, p)
			}
			d.Claims = append(d.Claims, source.Claim{
				Holder: source.Holder{Program: program}, Purpose: source.PurposeInstalled, Path: filepath.Clean(p),
				Store: program, Basis: content.Basis{Index: settings, Key: fmt.Sprintf("%s[%d]", key, n)}, Errors: errs,
			})
			n++
		}
	}
	return d
}

// fdsValue reads one key of one top-level section of SwarmUI's FDS format:
// "Section:" at column zero, "\tKey: value" indented beneath it, # comments.
func fdsValue(text, section, key string) (string, bool) {
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if !indented {
			in = strings.TrimSpace(k) == section && strings.TrimSpace(v) == ""
			continue
		}
		if in && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// Observe reads a claimed root as a tree; the composer attributes it.
func (Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	return tree.Observe(loc, env, tree.Options{Scheme: program})
}
