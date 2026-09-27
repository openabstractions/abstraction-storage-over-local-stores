package source

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-model/identity"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/gguf"
)

// Model is the abstraction.model/descriptor@1 field set: what the weights are,
// in the words the model contract defines, whatever store the file sits in.
// A store adapter fills what its own index and the file's own header say and
// leaves the rest empty; an empty descriptor is a manifest that names no model
// identity, and the router falls back to a host's alias for those.
type Model struct {
	Family       string
	Quant        string
	Format       string
	Architecture string
	SizeLabel    string
	Finetune     string
	// Base names what this weights file was fine-tuned from, read from a
	// GGUF header's general.base_model. For a component (Role is set to
	// something other than weights, MODEL-C3) it instead names the family
	// the component belongs to, when the component's own name derives one —
	// WithComponent sets Base to this second meaning and never both.
	Base string
	// Source names where the weights came from: the registry or the program
	// whose store this is.
	Source string
	// Role is RoleOf's answer for the file this descriptor names, seeded
	// empty for a plain weights file. A non-empty, non-weights Role marks a
	// component: MODEL-C3 says a router folds it into its Base family and
	// never lists it as a family of its own.
	Role string
}

// Map is the descriptor a manifest carries. Empty fields are absent, and a
// descriptor with nothing in it is nil, so a manifest that names no identity
// keeps the id it had before descriptors were read.
func (m Model) Map() map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{
		"family": m.Family, "quant": m.Quant, "format": m.Format,
		"architecture": m.Architecture, "size_label": m.SizeLabel,
		"finetune": m.Finetune, "base": m.Base, "source": m.Source,
		"role": m.Role,
	} {
		if v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WithComponent marks this descriptor as a component rather than a weights
// file: role is RoleOf's answer for the file (RoleProjector or RoleVAE,
// MODEL-C3). name is the store's own name for the file, the same name
// Named parsed; when its tokens state exactly one role, identity.Component
// reads the family the component belongs to and Base is set to it. A name
// stating none or more than one leaves Base as Named or WithHeader left it —
// nothing here could add a family a reader could trust.
func (m Model) WithComponent(name, role string) Model {
	m.Role = role
	if base, tok := identity.Component(name); tok != "" {
		m.Base = base
	}
	return m
}

// Named is what a store's own name for an artifact says about it: the family
// and the quantisation, parsed once by the model contract's identity rules
// rather than by a rule of this package's own, plus the format the file
// extension states and the source the adapter names.
func Named(name, format, from string) Model {
	n := identity.Parse(name)
	return Model{Family: n.Family, Quant: n.Quant, Format: format, Source: from}
}

// WithHeader adds what a GGUF header states to what the name said. The name
// wins for family and quantisation: a host serves the file under the store's
// name, and the alias a host reports is the name, not the converter's. The
// header is the only source of architecture, size label, fine-tune and base,
// and it names the family when the store's name yields none.
func (m Model) WithHeader(g gguf.General) Model {
	m.Architecture = first(m.Architecture, g.Architecture)
	m.SizeLabel = first(m.SizeLabel, g.SizeLabel)
	m.Finetune = first(m.Finetune, g.Finetune)
	m.Base = first(m.Base, g.Base)
	// The quantisation is spelled as the identity rules spell it, so a
	// descriptor read from a header and one read from a name are comparable.
	m.Quant = first(m.Quant, strings.ToLower(g.Quant))
	if m.Family == "" {
		m.Family = identity.Family(headerName(g))
	}
	return m
}

// headerName is the name a converter recorded: the base name with its size
// label and fine-tune, or general.name when it wrote no parts.
func headerName(g gguf.General) string {
	if g.BaseName == "" {
		return g.Name
	}
	parts := []string{g.BaseName}
	if g.SizeLabel != "" {
		parts = append(parts, g.SizeLabel)
	}
	if g.Finetune != "" {
		parts = append(parts, g.Finetune)
	}
	return strings.Join(parts, "-")
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Descriptor is the descriptor for one weight file: what its name says, and
// what its header says when the file is a GGUF and the environment reads
// headers. path is the file on this machine; name is the store's own name for
// the artifact; from names the store's program or registry.
//
// RoleOf(name) also classifies the file; a projector or VAE role marks the
// descriptor as a component through WithComponent (MODEL-C3), naming the
// family it belongs to in Base when name derives one.
//
// A file whose header cannot be read keeps the name's descriptor. Reading a
// header opens the file's first bytes and nothing else: no tensor is read and
// nothing is hashed.
func (e Env) Descriptor(path, name, from string) map[string]string {
	m := Named(name, FormatOf(path), from)
	role := RoleOf(name)
	if m.Format == "gguf" {
		if g, ok := e.Header(path); ok {
			m = m.WithHeader(g)
		}
	}
	if role == RoleProjector || role == RoleVAE {
		m = m.WithComponent(name, role)
	}
	return m.Map()
}

// Headers caches one GGUF header read per path, keyed by the size and
// modification time the walk already read (model-bridge decision §4 rule 1):
// a file that has not changed is not opened twice, and a file that has is read
// again. A nil Headers reads no header at all, which is what a test asserting
// on names alone wants.
type Headers struct {
	mu sync.Mutex
	at map[string]headerEntry
}

type headerEntry struct {
	size int64
	mod  time.Time
	g    gguf.General
	ok   bool
}

// NewHeaders is an empty header cache, for one run of a source.
func NewHeaders() *Headers { return &Headers{at: map[string]headerEntry{}} }

// Header reads the GGUF header of the file at path, through the cache. It
// answers false for a file that is not a readable GGUF, for a path that is not
// a regular file, and for an environment that reads no headers.
func (e Env) Header(path string) (gguf.General, bool) {
	if e.Headers == nil {
		return gguf.General{}, false
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return gguf.General{}, false
	}
	e.Headers.mu.Lock()
	defer e.Headers.mu.Unlock()
	if e.Headers.at == nil {
		e.Headers.at = map[string]headerEntry{}
	}
	if c, seen := e.Headers.at[path]; seen && c.size == st.Size() && c.mod.Equal(st.ModTime()) {
		return c.g, c.ok
	}
	g, err := gguf.Read(path)
	entry := headerEntry{size: st.Size(), mod: st.ModTime(), g: g, ok: err == nil}
	e.Headers.at[path] = entry
	return entry.g, entry.ok
}
