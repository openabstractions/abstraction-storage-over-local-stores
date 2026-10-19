package snapshot_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

var every = []string{"ollama", "hf", "lmstudio", "comfyui", "fastflowlm", "jan", "stabilitymatrix", "pinokio", "swarmui", "extra"}

func machine(t *testing.T) source.Env {
	home := fixture.Home(t, every...)
	env := fixture.Env(home, nil)
	env.Config.SwarmUI = []string{filepath.Join(home, "SwarmUI")}
	env.Config.ExtraRoots = []source.ExtraRoot{{Store: "nas", Path: filepath.Join(home, "NAS", "models")}}
	return env
}

func store(t *testing.T, r snapshot.Result, name string) (source.StoreObservation, snapshot.Summary) {
	t.Helper()
	for i, s := range r.Stores {
		if s.Name == name {
			return s, r.Summaries[i]
		}
	}
	var have []string
	for _, s := range r.Stores {
		have = append(have, s.Name)
	}
	t.Fatalf("no store %s; have %v", name, have)
	return source.StoreObservation{}, snapshot.Summary{}
}

func TestEveryStoreOnOneMachine(t *testing.T) {
	env := machine(t)
	r := snapshot.Take(env, snapshot.Adapters())

	var names []string
	for _, s := range r.Stores {
		if s.Present {
			names = append(names, s.Name)
		}
	}
	fixture.Equal(t, "present stores", names, []string{
		"ollama", "huggingface", "lemonade-extra", "lmstudio", "comfyui", "fastflowlm", "jan",
		"stabilitymatrix", "pinokio", "nas", "comfyui-extra:a111", "swarmui",
	})
	d := r.Description
	if d.Outcome != content.SourceDescriptionOutcomeDescribed || d.Name != "inventoryd" || len(d.Stores) != len(r.Stores) || !contains(d.Capabilities, "snapshot") || !contains(d.Capabilities, "observe") || !contains(d.Capabilities, "verify") || contains(d.Capabilities, "remove") {
		t.Fatalf("source description %+v", d)
	}
	for _, scheme := range []string{"ollama", "hf", "lmstudio", "comfyui", "flm", "jan"} {
		if !contains(d.Schemes, scheme) {
			t.Fatalf("scheme %s missing from %v", scheme, d.Schemes)
		}
	}
	for _, s := range r.Page.Stores {
		if s.Placement != content.PlacementLocal || s.Domain != "" {
			t.Fatalf("a store inventoryd reads is local with an empty domain: %+v", s)
		}
	}

	// SwarmUI's second ModelRoot is ComfyUI's models tree: its files gain a holder.
	comfy, comfySummary := store(t, r, "comfyui")
	fixture.Equal(t, "sd15 holders", fixture.Holders(fixture.MustManifest(t, comfy, "comfyui", "checkpoints/sd15.safetensors")), []string{"comfyui", "swarmui"})
	if comfySummary.UnheldObjects != 1 || comfySummary.UnheldBytes != 1500 || comfySummary.PartialObjects != 1 {
		t.Fatalf("comfyui summary %+v", comfySummary)
	}

	// extra_model_paths.yaml points outside ComfyUI: the A1111 tree is a store and ComfyUI holds what the yaml names.
	a1111, _ := store(t, r, "comfyui-extra:a111")
	if !fixture.SamePath(a1111.Locator, filepath.Join(env.Home, "stable-diffusion-webui")) {
		t.Fatalf("a1111 root %s", a1111.Locator)
	}
	fixture.Equal(t, "v15 holders", fixture.Holders(fixture.MustManifest(t, a1111, "comfyui", "models/Stable-diffusion/v1-5.safetensors")), []string{"comfyui"})
	fixture.Equal(t, "lora holders", fixture.Holders(fixture.MustManifest(t, a1111, "comfyui", "models/Lora/detail.safetensors")), []string{"comfyui"})
	// A pickle is listed and never a manifest (CONTRACT.md LS-1).
	if _, ok := fixture.Manifest(a1111, "comfyui", "models/ESRGAN/upscale.pth"); ok {
		t.Fatal("the ESRGAN pickle is offered as a model")
	}
	if _, ok := fixture.Object(a1111, "models/ESRGAN/upscale.pth"); !ok {
		t.Fatal("the ESRGAN pickle is not listed")
	}

	swarm, _ := store(t, r, "swarmui")
	fixture.Equal(t, "flux holders", fixture.Holders(fixture.MustManifest(t, swarm, "swarmui", "Stable-Diffusion/flux-dev.safetensors")), []string{"swarmui"})

	// Stability Matrix's ComfyUI package is a comfyui holder with its own instance.
	sm, _ := store(t, r, "stabilitymatrix")
	fixture.Equal(t, "juggernaut holders", fixture.Holders(fixture.MustManifest(t, sm, "stabilitymatrix", "StableDiffusion/juggernaut.safetensors")),
		[]string{"comfyui@stabilitymatrix:ComfyUI", "stabilitymatrix", "stable-diffusion-webui-forge@stabilitymatrix:stable-diffusion-webui-forge"})

	pk, _ := store(t, r, "pinokio")
	fixture.Equal(t, "drive holders", fixture.Holders(fixture.MustManifest(t, pk, "pinokio", "drives/peers/d17/checkpoints/shared.safetensors")),
		[]string{"comfy@pinokio:https://github.com/cocktailpeanut/comfy.git", "forge@pinokio:https://github.com/pinokiofactory/forge.git"})
	fixture.Equal(t, "unrouted holders", fixture.Holders(fixture.MustManifest(t, pk, "pinokio", "drives/peers/d17/loras/unrouted.safetensors")), []string(nil))

	_, hfSummary := store(t, r, "huggingface")
	if hfSummary.MultiHeld != 3 || hfSummary.HolderBytes["lemonade"] == 0 || hfSummary.HolderBytes["huggingface"] == 0 {
		t.Fatalf("hf summary %+v", hfSummary)
	}

	// Every index entry naming absent content, from stores and from claims.
	var dangling []string
	for _, x := range r.Page.Dangling {
		dangling = append(dangling, x.Holder.Program+" "+filepath.Base(filepath.FromSlash(x.Reference)))
	}
	sort.Strings(dangling)
	fixture.Equal(t, "dangling", dangling, []string{
		"comfyui LyCORIS", "comfyui VAE", "forge gone",
		"lemonade Tiny-GGUF:Q3_K_M", "ollama sha256:" + fixture.Label("ollama-missing", 64),
	})

	// Stray objects are the objects no manifest entry names.
	var strays []string
	for _, o := range r.Page.Objects {
		strays = append(strays, o.Store+" "+o.Object.Locator)
	}
	sort.Strings(strays)
	partial := fixture.Label("ollama-partial", 64)
	fixture.Equal(t, "strays", strays, []string{
		"comfyui checkpoints/SDXL/sd_xl_base_1.0.safetensors.part",
		// Pickles are listed and never manifests (CONTRACT.md LS-1).
		"comfyui-extra:a111 models/ESRGAN/upscale.pth",
		"comfyui-extra:a111 models/Stable-diffusion/unlisted-root.ckpt",
		"fastflowlm Half-NPU/model.q4nx.part",
		"huggingface models--acme--Partial-GGUF/blobs/" + fixture.Label("partial-etag", 64) + ".incomplete",
		"ollama blobs/sha256-" + fixture.Label("ollama-orphan", 64),
		"ollama blobs/sha256-" + partial + "-partial",
		"ollama blobs/sha256-" + partial + "-partial-0",
	})

	// Totals are the sum of the stores; every hold is observed with a basis
	// and nothing the runtime asserts.
	var objects int
	var bytes int64
	for i, s := range r.Stores {
		objects += r.Summaries[i].Objects
		bytes += r.Summaries[i].Bytes
		for _, m := range s.Manifests {
			for _, h := range m.Holds {
				if h.Target != m.Manifest.ID || h.Attestation != content.AttestationObserved || h.Lifetime != content.LifetimeWhilePresent || h.Holder.EstablishedBy != "" || h.Holder.Account != "" || h.Holder.Domain != "" || h.ID != "" || h.Basis == nil || h.Basis.Index == "" {
					t.Fatalf("hold %+v", h)
				}
			}
		}
	}
	if r.Totals.Objects != objects || r.Totals.Bytes != bytes || objects == 0 {
		t.Fatalf("totals %+v, stores %d objects %d bytes", r.Totals, objects, bytes)
	}
	if r.ObservedAt != "2026-09-16T17:13:21.000000Z" {
		t.Fatalf("observation time %q", r.ObservedAt)
	}
}

func TestNothingIsWritten(t *testing.T) {
	env := machine(t)
	before := fixture.State(t, env.Home)
	r := snapshot.Take(env, snapshot.Adapters())
	if len(r.Stores) == 0 {
		t.Fatalf("no stores")
	}
	if after := fixture.State(t, env.Home); after != before {
		t.Fatalf("snapshot changed the machine: %s", fixture.Changed(before, after))
	}
}

func TestTwoSnapshotsAgree(t *testing.T) {
	env := machine(t)
	a := snapshot.Take(env, snapshot.Adapters())
	b := snapshot.Take(env, snapshot.Adapters())
	a.Seconds, b.Seconds = 0, 0
	ja, _ := a.JSON()
	jb, _ := b.JSON()
	if len(a.Stores) == 0 || string(ja) != string(jb) {
		t.Fatalf("two snapshots of an unchanged tree differ")
	}
}

// Stat-only: a file nobody may read is still listed with its size.
func TestUnreadableFileIsListed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode 000 does not deny reads on Windows; the ACL variant belongs to the conformance corpus")
	}
	env := machine(t)
	p := filepath.Join(env.Home, "AppData", "Local", "AMD", "AI_Bundle", "ComfyUI", "ComfyUI", "models", "checkpoints", "sd15.safetensors")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o644)
	r := snapshot.Take(env, snapshot.Adapters())
	comfy, _ := store(t, r, "comfyui")
	o, ok := fixture.Object(comfy, "checkpoints/sd15.safetensors")
	if !ok || o.Size != 4000 {
		t.Fatalf("unreadable file %+v", o)
	}
}

// The diagnostic prints the contract's records in the contract's encoding:
// the page decodes with the generated codec and says the same thing.
func TestJSONCarriesTheContractRecords(t *testing.T) {
	r := snapshot.Take(machine(t), snapshot.Adapters())
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Source json.RawMessage `json:"source"`
		Page   json.RawMessage `json:"page"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	page, err := content.DecodeSourcePage(doc.Page)
	if err != nil {
		t.Fatalf("page does not decode: %v", err)
	}
	desc, err := content.DecodeSourceDescription(doc.Source)
	if err != nil {
		t.Fatalf("source does not decode: %v", err)
	}
	if string(content.EncodeSourcePage(page)) != string(content.EncodeSourcePage(&r.Page)) || desc.Name != "inventoryd" {
		t.Fatalf("decoded page differs from the observation")
	}
	for _, field := range []string{`"media_type"`, `"established_by"`, `"observed_at"`, `"attestation": "observed"`, `"lifetime": "while_present"`, `"evidence": "named"`, `"evidence": "none"`, `"addressing": "name"`, `"addressing": "content"`, `"placement": "local"`, `"basis"`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("snapshot JSON lacks %s", field)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
