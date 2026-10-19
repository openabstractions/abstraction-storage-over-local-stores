// Package asusai reads the model folders ASUS's AI applications keep under
// %ProgramData%\ASUS and lists only the files in them that are byte for
// byte a pinned public release (CONTRACT.md LS-12).
//
// Three folders hold models: AICreator, which ASUS's AI framework service
// unpacks MuseTree's Store packages into; LLMServer\models, ASUS's
// llama.cpp server; and WhisperRecorder\models. They hold public model files
// under ASUS's own names beside files ASUS made (a text-to-video
// transformer, NF4 and 4-bit text encoders, a Llama 3.1 8B fine-tune) and
// PyTorch pickles, and %ProgramData%\ASUS lets every account of the machine
// create files. A file is listed only when its path matches a pin's
// pattern, its size equals the pin's, SYSTEM, Administrators or
// TrustedInstaller owns it and no other principal can change it, its first
// bytes are the pin's model format, and its SHA-256 equals the public
// release's; nothing else in a folder is opened. Each listed file carries
// its public identity: the digest every other store names the same bytes
// by, and the release's repository, revision, path and licence.
//
// Inventory: research/model-locators/ASUS-INVENTORY.md (2026-10-01).
package asusai

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-identity/go/trustedpath"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	identity "github.com/openabstractions/abstraction-identity"
)

const (
	program = "asus"
	// scheme names a listed file by its path under %ProgramData%\ASUS;
	// publicScheme names it by the release it is a copy of, in the hf
	// scheme as <repository>@<full revision>:<path in the repository>. The
	// Hugging Face adapter's own names spell a revision and an artifact
	// differently; both stores share the digest.
	scheme       = "asus"
	publicScheme = "hf"
)

// folder is one ASUS model folder: a store of its own, held by the ASUS
// program that uses it.
type folder struct {
	store string
	// sub is the folder under %ProgramData%\ASUS, slash-separated.
	sub    string
	holder string
}

var folders = []folder{
	{store: "asus-aicreator", sub: "AICreator", holder: "musetree"},
	{store: "asus-llmserver", sub: "LLMServer/models", holder: "asus-llm-server"},
	{store: "asus-whisperrecorder", sub: "WhisperRecorder/models", holder: "asus-whisper-recorder"},
}

// pin is one public release file a folder may hold under an ASUS path.
type pin struct {
	store string
	// pattern matches the slash-separated path in the folder, compared in
	// lower case with path.Match: each * stands for part of one segment.
	pattern string
	size    int64
	sha256  string
	// format is the file's model format (source.ModelFormatOf):
	// safetensors, gguf or ggml. A pickle is never pinned (LS-1).
	format string
	// model is the release's model name; a weights file's family and a
	// VAE's base are read from it, so a VAE folds into its transformer's
	// family (MODEL-C3).
	model      string
	repository string
	revision   string
	file       string
	license    string
	// usePolicy names terms beside the licence that a user of the file
	// accepts; empty when the release states none.
	usePolicy string
	role      string
}

// hummingbirdPolicy is the AMD Responsible AI Use Policy, Attachment A of
// the Open RAIL-MS notices in Hummingbird-XT's source repository at the
// pinned commit; ASUS's GAI terms link to the same notices.
const hummingbirdPolicy = "github.com/AMD-AGI/HummingbirdXT@929e90a26c2d023c89f4428aa43869c88817a55e:NOTICES Attachment A"

// pins are the files ASUS-INVENTORY.md found byte-identical to a public
// release and that OA can load. The public pickles in AICreator
// (Wan2.2_VAE.pth, the umT5 .pth, BLIP's pytorch_model.bin, SAM,
// Real-ESRGAN) are never pinned (LS-1), and neither is any ASUS-made file.
// Sizes and digests are the Hugging Face LFS object ids at the named
// revisions, read 2026-10-01.
var pins = []pin{
	{
		store: "asus-aicreator", pattern: "flux_2/checkpoints/*/transformer/diffusion_pytorch_model.safetensors", size: 7751109744,
		sha256: "9f29f9edcfdae452a653ffb51a534ca4decd389952c225724ff3b94042612a6e", format: "safetensors", model: "FLUX.2-klein-4B",
		repository: "black-forest-labs/FLUX.2-klein-4B", revision: "e7b7dc27f91deacad38e78976d1f2b499d76a294",
		file: "transformer/diffusion_pytorch_model.safetensors", license: "Apache-2.0", role: source.RoleWeights,
	},
	{
		store: "asus-aicreator", pattern: "flux_2/checkpoints/*/vae/diffusion_pytorch_model.safetensors", size: 168120878,
		sha256: "ca70d2202afe6415bdbcb8793ba8cd99fd159cfe6192381504d6c4d3036e0f04", format: "safetensors", model: "FLUX.2-klein-4B",
		repository: "black-forest-labs/FLUX.2-klein-4B", revision: "e7b7dc27f91deacad38e78976d1f2b499d76a294",
		file: "vae/diffusion_pytorch_model.safetensors", license: "Apache-2.0", role: source.RoleVAE,
	},
	{
		store: "asus-aicreator", pattern: "hummingbird/*/transformer_*/diffusion_pytorch_model_i2v.safetensors", size: 9999659672,
		sha256: "6937e10a78a130d5f3a4d44705db476d98af5932b3cad1e011180294ca2b8291", format: "safetensors", model: "HummingbirdXT",
		repository: "amd/HummingbirdXT", revision: "4d3bd2e3a8c96a189ed158ab74b575ec6a424d2c",
		file: "hm_2.2_5B/diffusion_pytorch_model.safetensors", license: "Apache-2.0", usePolicy: hummingbirdPolicy,
		role: source.RoleWeights,
	},
	{
		store: "asus-aicreator", pattern: "hummingbird/vae_decoder/*/diffusion_pytorch_model.safetensors", size: 152049060,
		sha256: "04be976c369c5ce0d48cbe6685244fdb40fc42a23594d7c3c4470c5610b5d09f", format: "safetensors", model: "HummingbirdXT",
		repository: "amd/HummingbirdXT", revision: "4d3bd2e3a8c96a189ed158ab74b575ec6a424d2c",
		file: "vae/wan22_v1_tiling_16_12/diffusion_pytorch_model.safetensors", license: "Apache-2.0", usePolicy: hummingbirdPolicy,
		role: source.RoleVAE,
	},
	{
		// ASUS names the file emb_<major>_<minor> with no extension.
		store: "asus-llmserver", pattern: "emb_*", size: 437778496,
		sha256: "5fe3a00f9e92cc07f9745c69d25e17e234e1a7f8cde6e7ebe930dc7516e9accf", format: "gguf", model: "bge-m3-Q4_K_M",
		repository: "doof-ferb/bge-m3-gguf", revision: "7a0937bc7478e150e8a33f96bb09647c365e9ef1",
		file: "model-q4_k_m.gguf", license: "MIT (BAAI/bge-m3, GGUF general.license); CC-BY-4.0 (conversion card)",
		role: source.RoleWeights,
	},
	{
		store: "asus-whisperrecorder", pattern: "ggml-medium.bin", size: 1533763059,
		sha256: "6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208", format: "ggml", model: "whisper-medium",
		repository: "ggerganov/whisper.cpp", revision: "5359861c739e955e79d9a303bcbc70fb988958b1",
		file: "ggml-medium.bin", license: "MIT", role: source.RoleWeights,
	},
}

// Adapter reads the ASUS model folders against a pin table.
type Adapter struct{ pins []pin }

// trust is the owner and access rule a candidate passes before any byte of
// it is read (trustedFile); tests replace it to judge fixture files, which
// this account owns.
var trust = trustedFile

// trustedFile is identity's rule for a path only trusted principals can
// change (ID-D2): SYSTEM, Administrators or TrustedInstaller own the file
// and every folder above it, no other principal can change, delete or
// rename them, and no reparse point stands on the path. ASUS's services and
// installers run as those principals, and %ProgramData%\ASUS lets every
// other account of the machine create files.
func trustedFile(path string) error {
	return trustedpath.CheckFile(path, trustedpath.MachineOwners())
}

// New reads the folders against the pins of ASUS-INVENTORY.md.
func New() Adapter { return Adapter{pins: pins} }

func (Adapter) Program() string { return program }

// Discover: each folder sits under %ProgramData%\ASUS, where ASUS's
// services unpack or download their models. The folders exist only on
// Windows; an environment without ProgramData names no store, so nothing
// outside the environment's own variables is read.
func (Adapter) Discover(env source.Env) source.Discovery {
	var d source.Discovery
	if env.GOOS != "windows" {
		return d
	}
	data := env.Var("ProgramData")
	if data == "" {
		return d
	}
	for _, f := range folders {
		d.Stores = append(d.Stores, source.NewStore(f.store, program,
			filepath.Join(data, "ASUS", filepath.FromSlash(f.sub)),
			`ASUS model folder %ProgramData%\ASUS\`+strings.ReplaceAll(f.sub, "/", `\`)))
	}
	return d
}

// Observe lists one folder by lstat. A file whose path and size match one
// of that folder's pins is a candidate: its owner and access are read, then
// its first bytes, and its digest comes from env.Digests, which hashes it
// once in the background. A candidate waiting for its hash is an object
// with no digest and no manifest; a candidate whose digest is the pin's is
// listed. Every other file is counted among the store's other files and
// never opened (LS-12).
func (a Adapter) Observe(loc content.Store, env source.Env) source.StoreObservation {
	o := source.Begin(loc)
	if !loc.Present {
		return o.Finish()
	}
	var where folder
	for _, f := range folders {
		if f.store == storeBase(loc.Name) {
			where = f
		}
	}
	if where.store == "" {
		return o.Finish()
	}
	files, errs := source.Walk(loc.Locator)
	o.Errors = append(o.Errors, errs...)
	at := source.Stamp(env.At())
	seen := map[string]bool{}
	other := func(f source.File) {
		o.OtherFiles++
		if !f.Link {
			o.OtherBytes += f.Size
		}
	}
	for _, f := range files {
		full := filepath.Join(loc.Locator, filepath.FromSlash(f.Rel))
		seen[full] = true
		p, ok := a.match(where.store, f)
		if !ok || env.Digests == nil {
			other(f)
			continue
		}
		if err := trust(full); err != nil {
			o.Fail(content.StoreErrorKindOther, f.Rel, fmt.Errorf("not opened: %w", err))
			other(f)
			continue
		}
		if source.IsPickle(full, f.Rel) || source.ModelFormatOf(full, f.Rel) != p.format {
			o.Fail(content.StoreErrorKindOther, f.Rel, fmt.Errorf("not %s, the format of %s@%s %s; not listed", p.format, p.repository, short(p.revision), p.file))
			other(f)
			continue
		}
		sum, err := env.Digests.SHA256(full, f.Size, f.ModTime)
		if errors.Is(err, source.ErrPending) {
			o.AddObject(f.Rel, f, "", content.EvidenceNone)
			continue
		}
		if err == nil && sum != p.sha256 {
			err = fmt.Errorf("SHA-256 %s is not %s@%s %s; not listed", sum, p.repository, short(p.revision), p.file)
		}
		if err != nil {
			o.Fail(content.StoreErrorKindOther, f.Rel, err)
			other(f)
			continue
		}
		digest := "sha256:" + sum
		o.AddObject(f.Rel, f, digest, content.EvidenceHashed)
		entry := source.FileEntry(p.role, f.Rel, f.Size)
		entry.MediaType = source.MediaTypeOf(p.file)
		entry.Digest, entry.Evidence = digest, content.EvidenceHashed
		entry.Annotations = map[string]string{
			"release.repository": p.repository,
			"release.revision":   p.revision,
			"release.path":       p.file,
			"release.license":    p.license,
		}
		if p.usePolicy != "" {
			entry.Annotations["release.use_policy"] = p.usePolicy
		}
		public := p.repository + "@" + p.revision + ":" + p.file
		m := source.NewManifest(loc.Name, source.KindModel, []content.Entry{entry},
			[]content.Name{{Scheme: scheme, Name: where.sub + "/" + f.Rel}, {Scheme: publicScheme, Name: public}}, descriptor(p).Map())
		i := o.AddManifest(m)
		o.Hold(i, where.holder, source.PurposeInstalled, `%ProgramData%\ASUS\`+strings.ReplaceAll(where.sub, "/", `\`), path.Dir(f.Rel), at)
	}
	if len(errs) == 0 {
		env.Digests.Keep(loc.Locator, seen)
	}
	return o.Finish()
}

// descriptor is a pin's model descriptor. A weights file's family is its
// release's model name; a VAE is a component (MODEL-C3) whose base is that
// same family, so it folds into the transformer it decodes for.
func descriptor(p pin) source.Model {
	model := source.Named(p.model, p.format, program)
	if p.role != source.RoleVAE {
		return model
	}
	vae := source.Named(p.repository+":"+p.file, p.format, program).WithComponent(p.repository+":"+p.file, p.role)
	vae.Base = model.Family
	return vae
}

// match is the pin of this store a walked file may be: a regular file whose
// path matches the pin's pattern as the file system compares names (both
// keyed by identity.PathNameKey, a fixed name's rule) and whose size is the
// pin's. Nothing is read to decide it.
func (a Adapter) match(store string, f source.File) (pin, bool) {
	if f.Link {
		return pin{}, false
	}
	rel := identity.PathNameKey(f.Rel)
	for _, p := range a.pins {
		if p.store != store || f.Size != p.size {
			continue
		}
		if ok, err := path.Match(identity.PathNameKey(p.pattern), rel); err == nil && ok {
			return p, true
		}
	}
	return pin{}, false
}

// storeBase strips the "#n" suffix snapshot.Take adds to a repeated store name.
func storeBase(name string) string {
	if i := strings.IndexByte(name, '#'); i >= 0 {
		return name[:i]
	}
	return name
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
