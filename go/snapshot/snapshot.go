// Package snapshot runs every adapter over one machine and composes what they
// observe into the records abstraction.storage/inventory-source@1 reports:
// claims one program's configuration makes on another store's files become
// holds there, and claimed directories no store covers become stores.
package snapshot

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/comfyui"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/extra"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/fastflowlm"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/huggingface"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/jan"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/lmstudio"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/ollama"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/pinokio"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/stabilitymatrix"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/swarmui"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// SourceName is the provider's name in its SourceDescription.
const SourceName = "inventoryd"

// Capabilities are the inventory-source calls inventoryd serves beyond Describe.
var Capabilities = []string{"snapshot", "observe", "verify"}

// Summary is one store's totals, or the machine's. It is a provider
// diagnostic and never travels on the contract.
type Summary struct {
	Objects        int              `json:"objects"`
	Bytes          int64            `json:"bytes"`
	Manifests      int              `json:"manifests"`
	PartialObjects int              `json:"partial_objects"`
	PartialBytes   int64            `json:"partial_bytes"`
	UnheldObjects  int              `json:"unheld_objects"`
	UnheldBytes    int64            `json:"unheld_bytes"`
	HeldBytes      int64            `json:"held_bytes"`
	MultiHeld      int              `json:"manifests_with_two_or_more_holders"`
	HolderBytes    map[string]int64 `json:"holder_bytes"`
	Errors         int              `json:"errors"`
	OtherFiles     int64            `json:"other_files"`
	OtherBytes     int64            `json:"other_bytes"`
}

// Result is one observation of the machine: the contract records the source
// reports, and the observations and totals behind them.
type Result struct {
	// Description is Describe's answer for this observation.
	Description content.SourceDescription
	// Page is the whole observation as one complete Snapshot page: every
	// store, every manifest with its observed holds in store then id order,
	// every stray object and every dangling reference.
	Page       content.SourcePage
	ObservedAt string
	Seconds    float64
	// Stores are the per-store observations, including every object, in the
	// order of Page.Stores.
	Stores    []source.StoreObservation
	Summaries []Summary
	Claims    []source.Claim
	Totals    Summary
}

// Adapters is every adapter, in report order.
func Adapters() []source.Adapter {
	return []source.Adapter{
		ollama.New(), huggingface.New(), lmstudio.New(), comfyui.New(), fastflowlm.New(),
		jan.New(), stabilitymatrix.New(), pinokio.New(), swarmui.New(), extra.New(),
	}
}

// Schemes every adapter names manifests in.
var schemes = []string{"ollama", "hf", "lmstudio", "comfyui", "flm", "jan", "lemonade", "stabilitymatrix", "pinokio", "swarmui"}

// Take observes every store the adapters discover. It hashes, writes, moves and
// deletes nothing.
func Take(env source.Env, adapters []source.Adapter) Result {
	start := time.Now()
	at := env.At()
	env.Now = func() time.Time { return at }
	stamp := source.Stamp(at)
	r := Result{ObservedAt: stamp, Claims: []source.Claim{}}

	used := map[string]bool{}
	unique := func(name string) string {
		n, i := name, 2
		for used[n] {
			n = fmt.Sprintf("%s#%d", name, i)
			i++
		}
		used[n] = true
		return n
	}

	var observed []source.StoreObservation
	// claimedBy[i] is the adapter whose discovery made r.Claims[i]; it observes
	// a store its claim opens.
	var claimedBy []source.Adapter
	var dangling []content.Dangling
	for _, a := range adapters {
		d := a.Discover(env)
		r.Claims = append(r.Claims, d.Claims...)
		for range d.Claims {
			claimedBy = append(claimedBy, a)
		}
		for _, s := range d.Stores {
			s.Name = unique(s.Name)
			observed = append(observed, a.Observe(s, env))
		}
		if len(d.Errors) > 0 {
			// Configuration failures that name no store travel on a Store
			// record of their own, so the page carries them.
			s := content.Store{
				Name: unique(a.Program() + ":configuration"), Program: a.Program(), Locator: d.Errors[0].Locator,
				Rule: "configuration the discovery rules read", Errors: append([]content.StoreError{}, d.Errors...),
				Placement: content.PlacementLocal,
			}
			empty := source.Begin(s)
			observed = append(observed, empty.Finish())
		}
	}

	// Claimed directories no present store covers become tree stores, rooted
	// at the claim's base when it has one; nested candidates fold into the
	// outermost.
	type candidate struct {
		root  string
		claim source.Claim
		by    source.Adapter
	}
	var candidates []candidate
	for ci, c := range r.Claims {
		if !exists(c.Path) {
			dangling = append(dangling, source.NewDangling(c.Holder, c.Basis, c.Path, "", 0, stamp))
			if len(c.Errors) > 0 {
				s := content.Store{
					Name: unique(c.Holder.Program + ":configuration"), Program: c.Holder.Program, Locator: c.Basis.Index,
					Rule: "configuration the discovery rules read", Errors: append([]content.StoreError{}, c.Errors...),
					Placement: content.PlacementLocal,
				}
				empty := source.Begin(s)
				observed = append(observed, empty.Finish())
			}
			continue
		}
		if covering(observed, c.Path) >= 0 {
			continue
		}
		root := c.Path
		if c.Base != "" && source.IsDir(c.Base) {
			root = c.Base
		} else if !source.IsDir(root) {
			root = filepath.Dir(root)
		}
		candidates = append(candidates, candidate{root: filepath.Clean(root), claim: c, by: claimedBy[ci]})
	}
	for i, c := range candidates {
		folded := false
		for j, other := range candidates {
			if i == j {
				continue
			}
			if rel, in := source.Within(other.root, c.root); in && (rel != "" || j < i) {
				folded = true
				break
			}
		}
		if folded {
			continue
		}
		store := c.claim.Store
		if store == "" {
			store = c.claim.Holder.Program
		}
		loc := source.NewStore(unique(store), c.claim.Holder.Program, c.root, "claim by "+c.claim.Holder.Program+": "+c.claim.Basis.Index+" "+c.claim.Basis.Key)
		observed = append(observed, c.by.Observe(loc, env))
	}

	for _, c := range r.Claims {
		if !exists(c.Path) {
			continue
		}
		i := covering(observed, c.Path)
		if i < 0 {
			continue
		}
		o := &observed[i]
		o.Errors = append(o.Errors, c.Errors...)
		rel, _ := source.Within(o.Locator, c.Path)
		for m := range o.Manifests {
			for _, l := range source.EntryLocators(o.Manifests[m].Manifest) {
				if rel == "" || l == rel || strings.HasPrefix(l, rel+"/") {
					purpose := c.Purpose
					if purpose == "" {
						purpose = source.PurposeInstalled
					}
					o.HoldBy(m, c.Holder, purpose, c.Basis, stamp)
					break
				}
			}
		}
	}

	seenScheme := map[string]bool{}
	for _, s := range schemes {
		seenScheme[s] = true
	}
	r.Page = content.SourcePage{
		Outcome:   content.SourcePageOutcomePage,
		Stores:    []content.Store{},
		Manifests: []content.ManifestHolders{},
		Objects:   []content.ObjectHolders{},
		Dangling:  []content.Dangling{},
		Changes:   []content.SourceChange{},
		Complete:  true,
	}
	var total Summary
	total.HolderBytes = map[string]int64{}
	for i := range observed {
		o := observed[i].Finish()
		s := summarize(o)
		r.Stores = append(r.Stores, o)
		r.Summaries = append(r.Summaries, s)
		r.Page.Stores = append(r.Page.Stores, o.Store)
		r.Page.Manifests = append(r.Page.Manifests, o.Manifests...)
		r.Page.Objects = append(r.Page.Objects, Strays(o)...)
		r.Page.Dangling = append(r.Page.Dangling, o.Dangling...)
		for _, m := range o.Manifests {
			for _, n := range m.Manifest.Names {
				seenScheme[n.Scheme] = true
			}
		}
		total.Objects += s.Objects
		total.Bytes += s.Bytes
		total.Manifests += s.Manifests
		total.PartialObjects += s.PartialObjects
		total.PartialBytes += s.PartialBytes
		total.UnheldObjects += s.UnheldObjects
		total.UnheldBytes += s.UnheldBytes
		total.HeldBytes += s.HeldBytes
		total.MultiHeld += s.MultiHeld
		total.Errors += s.Errors
		total.OtherFiles += s.OtherFiles
		total.OtherBytes += s.OtherBytes
		for p, b := range s.HolderBytes {
			total.HolderBytes[p] += b
		}
	}
	sort.SliceStable(dangling, func(i, j int) bool { return source.DanglingKey(dangling[i]) < source.DanglingKey(dangling[j]) })
	r.Page.Dangling = append(r.Page.Dangling, dangling...)
	r.Totals = total

	r.Description = content.SourceDescription{
		Outcome:      content.SourceDescriptionOutcomeDescribed,
		Name:         SourceName,
		Stores:       r.Page.Stores,
		Schemes:      []string{},
		Capabilities: append([]string{}, Capabilities...),
	}
	for s := range seenScheme {
		r.Description.Schemes = append(r.Description.Schemes, s)
	}
	sort.Strings(r.Description.Schemes)
	r.Seconds = math.Round(time.Since(start).Seconds()*100) / 100
	return r
}

// Strays are the objects of one store no manifest entry names.
func Strays(o source.StoreObservation) []content.ObjectHolders {
	named := map[string]bool{}
	for _, m := range o.Manifests {
		for _, l := range source.EntryLocators(m.Manifest) {
			named[l] = true
		}
	}
	out := []content.ObjectHolders{}
	for _, obj := range o.Objects {
		if !named[obj.Locator] {
			out = append(out, content.ObjectHolders{Object: obj, Store: o.Name, Holds: []content.Hold{}})
		}
	}
	return out
}

// JSON is the provider diagnostic `inventoryd snapshot --json` prints: what
// Describe and a complete Snapshot would report, in the contract's own
// encoding, beside per-store totals and the claims behind the holds.
func (r Result) JSON() ([]byte, error) {
	type store struct {
		Name    string  `json:"name"`
		Summary Summary `json:"summary"`
	}
	type claim struct {
		Program  string `json:"program"`
		Instance string `json:"instance"`
		Purpose  string `json:"purpose"`
		Path     string `json:"path"`
		Base     string `json:"base"`
		Store    string `json:"store"`
		Index    string `json:"index"`
		Key      string `json:"key"`
		Role     string `json:"role"`
	}
	doc := struct {
		Source     json.RawMessage `json:"source"`
		Page       json.RawMessage `json:"page"`
		ObservedAt string          `json:"observed_at"`
		Seconds    float64         `json:"seconds"`
		Stores     []store         `json:"stores"`
		Claims     []claim         `json:"claims"`
		Totals     Summary         `json:"totals"`
	}{
		Source:     content.EncodeSourceDescription(&r.Description),
		Page:       content.EncodeSourcePage(&r.Page),
		ObservedAt: r.ObservedAt,
		Seconds:    r.Seconds,
		Stores:     []store{},
		Claims:     []claim{},
		Totals:     r.Totals,
	}
	for i, o := range r.Stores {
		doc.Stores = append(doc.Stores, store{Name: o.Name, Summary: r.Summaries[i]})
	}
	for _, c := range r.Claims {
		doc.Claims = append(doc.Claims, claim{
			Program: c.Holder.Program, Instance: c.Holder.Instance, Purpose: c.Purpose, Path: c.Path, Base: c.Base,
			Store: c.Store, Index: c.Basis.Index, Key: c.Basis.Key, Role: c.Basis.Role,
		})
	}
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// covering is the present store with the deepest root containing p.
func covering(stores []source.StoreObservation, p string) int {
	best, depth := -1, -1
	for i, s := range stores {
		if !s.Present {
			continue
		}
		if _, in := source.Within(s.Locator, p); in {
			if d := len(filepath.Clean(s.Locator)); d > depth {
				best, depth = i, d
			}
		}
	}
	return best
}

func summarize(o source.StoreObservation) Summary {
	s := Summary{HolderBytes: map[string]int64{}, Manifests: len(o.Manifests), Errors: len(o.Errors), OtherFiles: o.OtherFiles, OtherBytes: o.OtherBytes}
	holders := map[string]map[string]bool{}
	for _, m := range o.Manifests {
		progs := map[string]bool{}
		for _, h := range m.Holds {
			progs[h.Holder.Program] = true
		}
		if len(progs) >= 2 {
			s.MultiHeld++
		}
		for _, l := range source.EntryLocators(m.Manifest) {
			if holders[l] == nil {
				holders[l] = map[string]bool{}
			}
			for p := range progs {
				holders[l][p] = true
			}
		}
	}
	for _, obj := range o.Objects {
		s.Objects++
		s.Bytes += obj.Size
		if obj.Partial {
			s.PartialObjects++
			s.PartialBytes += obj.Size
		}
		if len(holders[obj.Locator]) == 0 {
			s.UnheldObjects++
			s.UnheldBytes += obj.Size
			continue
		}
		s.HeldBytes += obj.Size
		for p := range holders[obj.Locator] {
			s.HolderBytes[p] += obj.Size
		}
	}
	return s
}
