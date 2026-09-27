// Package source holds what every store adapter of inventoryd shares: the
// observation record an adapter returns, the environment discovery reads, the
// stat-only walk, and the manifest and hold constructors.
//
// Every record an adapter reports is a generated contract record of
// abstraction.storage content (content.thrift): Store for a store a discovery
// rule produced, StoreError for a typed failure inside it, Object for a file
// read by stat, Manifest and Hold with its Basis, and Dangling for an index
// entry naming content the store does not hold. Claims and the counts of other
// files are inventoryd's own bookkeeping and never leave the provider.
package source

import (
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// Store errors an adapter reports.
const (
	// ErrorMalformedIndex: a program's index file exists and does not parse
	// as that program writes it.
	ErrorMalformedIndex = content.StoreErrorKindMalformedIndex
	// ErrorUnreadableIndex: a program's index file exists and cannot be read.
	ErrorUnreadableIndex = content.StoreErrorKindUnreadableIndex
	// ErrorUnreadableTree: a directory inside the store cannot be listed.
	ErrorUnreadableTree = content.StoreErrorKindUnreadableTree
)

// NewStore is the Store record for a root a discovery rule produced, with
// Present read from the file system. inventoryd reads the store where it is,
// so the locator is the root directory and placement is local.
func NewStore(name, program, root, rule string) content.Store {
	root = cleanRoot(root)
	return content.Store{
		Name:      name,
		Program:   program,
		Locator:   root,
		Rule:      rule,
		Present:   IsDir(root),
		Errors:    []content.StoreError{},
		Placement: content.PlacementLocal,
	}
}

// Holder names a program that holds content, in the source's words. Instance
// distinguishes installations of one program where the source knows them (a
// Stability Matrix package, a Pinokio app).
type Holder struct {
	Program  string
	Instance string
}

// Claim says a program uses the files under Path, read from that program's
// configuration. The composer attaches the hold to manifests of whichever store
// holds those files, and opens a tree store at Base (or Path) when none does.
type Claim struct {
	Holder  Holder
	Purpose string
	Path    string
	Base    string
	// Store names the store opened when no discovered store holds Path.
	Store string
	Basis content.Basis
	// Errors are failures reading the configuration behind the claim; they
	// are reported in the store the claim lands in.
	Errors []content.StoreError
}

// Discovery is what one adapter's discovery rules found.
type Discovery struct {
	Stores []content.Store
	Claims []Claim
	// Errors are configuration failures that belong to no store, such as a
	// malformed configuration file naming where a store would be.
	Errors []content.StoreError
}

// StoreObservation is everything one adapter read from one store: the Store
// record with its errors, every object by stat, the manifests with the holds
// their programs' indexes prove, and the index entries naming absent content.
type StoreObservation struct {
	content.Store
	Objects    []content.Object
	Manifests  []content.ManifestHolders
	Dangling   []content.Dangling
	OtherFiles int64
	OtherBytes int64
}

// Adapter reads one program's stores.
type Adapter interface {
	// Program is the adapter's name and the holder name its own index proves.
	Program() string
	// Discover applies the program's discovery rules. It reads configuration
	// files and lists nothing inside a store.
	Discover(env Env) Discovery
	// Observe reads one store: directory entries by lstat, the program's own
	// index files, and the key-value header of a GGUF weight file, which is
	// what a model descriptor's architecture, size label, fine-tune and base
	// come from. It reads no tensor, hashes nothing, and writes, moves or
	// deletes nothing.
	Observe(store content.Store, env Env) StoreObservation
}
