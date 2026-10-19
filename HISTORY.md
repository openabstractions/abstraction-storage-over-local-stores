# History

Design records and prior narrative for
[`CONTRACT.md`](CONTRACT.md), moved out of the rule pages per
`research/vocabulary/DECISION.md` S7.

## Why LS-5 and LS-6 take this shape

The alternative LS-5 and LS-6 decline is hashing the file. Reading a cold
20 GB cache of un-blobbed files on every `Observe` to produce a digest the
manifest's name-addressed identity already provides is the cost the
blob-and-symlink layout exists to avoid, against this reader's own design of
hashing nothing ([README.md](README.md)). Naming the identity from
`refs/main`'s commit plus the entry's path adds nothing over the name
already in `names[0]`: `revisionName` folds the commit into that name for a
revision no ref names, and the path is already the entry's locator.

The router's `held_in` composition (`abstraction-router/go/held.go`) matches
a store's holdings to a family through that same name; it reaches for an
object's digest only when a store names the object nothing, which the
Hugging Face adapter never does. The model bridge's `Resolve`
(`abstraction-provider-modelbridge/go/bridge/object.go`) matches a lend
request against the manifest id, its names or an entry's locator, never its
digest. Both readers already work off the name-addressed layout today.

## Measured

Without the symlink privilege, `huggingface_hub` copies the blob's bytes
into the snapshot instead of linking it — measured on the owner's own
machine, a Windows account without Developer Mode
(`research/model-bridge/EXPERIMENT-2026-09-22.md`). Every repository in the
owner's cache carried a `.lemonade_registry.json` and no `blobs/` directory
at all, confirming LS-5's common case from the same evidence.

Lemonade's own downloader writes a repository's files straight into
`snapshots/<rev>/` without ever creating a blob to link or copy from
(`lemonade-sdk/lemonade`, `src/cpp/server/model_manager.cpp`: "Lemonade's
own downloader writes real files without blobs").

## 2026-10-31: LS-13, a hash on request

A person's coding model already sat in their hub cache as a snapshot file
with no blob, so this reader listed it with no digest and nothing could
find it by content; the setup step offered a 23 GB download of it. The
first cut hashed every such weights file in every known store in the
background and let the manifest become content-addressed when the hash
ended. Independent review rejected both parts: a FIFO queue at normal IO
priority could put the one wanted file behind hundreds of gigabytes, which
is the cost "Why LS-5 and LS-6 take this shape" declines; and a manifest
whose id changed when its hash ended stopped resolving for a host or lend
bound to the earlier id. LS-13 therefore hashes only a manifest a caller's
`Verify` names, on local volumes, at background IO priority, and records
the digest on the entry and object while the manifest keeps its id and
name addressing. Listings still list every known store's models; listing
needs no hash.

## 2026-09-23: shape review

`research/reviews/providers-2026-09-23.md` F14 found this contract's whole
document stated its rules as prose naming Go symbols and paths
(`source.IsWeightFile`, `go/adapters/tree/tree.go`, `matchBlobBySize`)
rather than declared rule ids, and never named the wire service it
implements. F15 found the digest sentence naming a field
`abstraction.model/descriptor@1` does not have (the field is `Entry.digest`,
`content.thrift:145`). F16 found the "no-blobs" layout attributed to
Lemonade when it is `huggingface_hub`'s own no-symlink fallback, with
Lemonade's own no-blobs downloader as one further instance of the same
shape. F17 found a size-collision byte comparison stated against a page
that also promises "reads no tensor and hashes nothing." `CONTRACT.md`
carries the ids, wire name and corrected attributions this review asked
for; this page carries the narrative it moved out of the rules.

## 2026-10-01: where a member's stream starts

The inference provider kit fills a model into a memory section from a
DEFLATE member's stream (inference INF-MS1). The model bridge had
recomputed the stream's place in the file from the nested zips around it;
the review of that slice asked the reader to report it.

| id | before | now |
| --- | --- | --- |
| LS-8 | a member's `Offset` was in the container file for an in-place member and inside its own zip otherwise | `container.Member.FileOffset` adds the place of every member's stored bytes in the container file for members inside STORED zips only, and -1 for a member reached by streaming |

## 2026-10-01: ASUS's model folders, listed by pinned digest

The owner asked OA to run MuseTree's own models faster than MuseTree and to
lend them to other programs, without copying them
(`research/video-demo/MUSETREE-OWN-MODELS.md`). The inventory of
`%ProgramData%\ASUS\AICreator` (`research/model-locators/ASUS-INVENTORY.md`)
found ten of fifteen weight files byte-identical to public releases, three
ASUS-made, and several public files stored as pickles. Its section 11 found
two more public files in ASUS's other AI folders: `LLMServer\models\emb_2_3`
(bge-m3 Q4_K_M GGUF) and `WhisperRecorder\models\ggml-medium.bin`
(whisper.cpp medium); `LLMServer\models\llm_2_16` is ASUS's Llama 3.1 8B
fine-tune. The coordinator asked for one adapter over the three folders.

| id | before | now |
| --- | --- | --- |
| LS-12 | no rule; no adapter read the folders | a pin table of six public files: four safetensors in AICreator (FLUX.2 [klein] 4B transformer and VAE, Hummingbird-XT i2v transformer and TurboVAED decoder), bge-m3 Q4_K_M GGUF in LLMServer and whisper.cpp's ggml medium in WhisperRecorder; a file is listed only when path, size and SHA-256 match a pin, and carries `sha256:` evidence `hashed` and its release's identity |
| LS-6, LS-7 | snapshots hash nothing and write nothing | LS-12's hash of a pinned candidate is the one whole-file hash a snapshot takes; its record is the one file the reader writes, outside every store |

Why hash here and nowhere else: the folder renames public files and has no
index naming a digest, so name addressing (LS-5) would list ASUS's
text-to-video transformer, whose size, names and shapes resemble the public
image-to-video file, as readily as the public file itself. Size and path
alone choose the candidates; nothing else in the folder is opened.

Why a record: the six pinned files total 20.04 GB. Measured 2026-10-01 on
the owner's machine with `inventoryd snapshot --json` and the record in an
ignored test folder: the first snapshot over the three folders took 22.5 s
and matched all six pins, and the second took 0.1 s, answered from the
record without reading a file. The other 41 files in the folders (39.5 GB)
were counted and never opened. A
changed size or modification time hashes the file again, the same trigger
`Headers` uses for GGUF headers. A file rewritten with the same size and
modification time keeps its recorded digest until either changes. The
inventory answers what a store holds; an engine launch proves the bytes it
loads itself (the provider host's `enginefiles` proof under the sd.cpp video
addendum's condition 2).

### Review of the first version (2026-10-01)

| id | before | now |
| --- | --- | --- |
| LS-12 | a digest was recorded for a path, size and modification time; another account could plant a file at a wildcard pin path (`%ProgramData%\ASUS` grants BUILTIN\Users create and write), let it be hashed with public bytes, then rewrite it under the old time | a candidate is read only when SYSTEM, Administrators or TrustedInstaller owns it and no other principal can write, delete or rename it (owner and DACLs read, no content); the record binds the file identity (volume serial number and file ID) too, so a same-size replacement renamed in with the old time is hashed again; entries for vanished paths are dropped; the record lives in a folder private to this account (abstraction-identity's `go/privatedir`, ID-D1) |
| LS-12 | a listing hashed a new candidate before it answered; the file was opened without delete sharing | hashing runs in the background and a listing reports a candidate as pending until it ends; the hash handle shares delete, so ASUS's updater can replace the file |
| LS-12 | the adapter judged pickles by its own extension list | it calls `source.IsPickle` and `source.ModelFormatOf`, LS-1's owner, after a pin matches and the owner check passes |
| LS-1, LS-2 | a `.bin` whose header was not GGUF's was a pickle | whisper.cpp's ggml magic (`lmgg`) is a model format, `ggml`, and never a pickle |
| LS-12 | no digest record made candidates store errors | they are other files, silently |

The owner and access check then moved to abstraction-identity as
`go/trustedpath` (ID-D2), which also checks every folder up to the volume
root and refuses reparse points, so the video provider shares one owner of
the rule; the adapter calls `trustedpath.CheckFile` with
`trustedpath.MachineOwners()`.

Measured by the review on the owner's machine, with the six pinned files
outside the OS file cache: the first hash of the 20.04 GB took 153.5 s.
The 22.5 s above had the files in the cache, read by the inventory
shortly before.

The public pickles in AICreator (`Wan2.2_VAE.pth`, the umT5 `.pth`, BLIP's
`pytorch_model.bin`, SAM and Real-ESRGAN) are not pinned: LS-1 lists a
pickle as an object so a router never offers it, and in this store listing
it would only advertise a file no OA engine loads.

## 2026-10-20: current Ollama, Lemonade and Jan stores (AD-06)

The 0.8.0 plan's AD-06 asks this reader to read Ollama's `manifests-v2`
layout and the current Lemonade and Jan stores, with Lemonade as its own
adapter, so P-J1's library reads today's programs.

| id | before | now |
| --- | --- | --- |
| LS-3 | Ollama's manifests came from `manifests/` only | `manifests-v2/` first, then the legacy tree, one name and one hold per model; a manifest list yields one manifest per runner-specific child, a reference child through its blob and an inline child in place; every manifest read through a `manifests-v2` name carries that name's own blob as an `ollama/manifest` entry |
| LS-4 | no rule for Ollama | a Windows copy of a `manifests-v2` name is matched to the one present blob of its size and bytes that no manifest entry names except an `ollama/manifest` entry, at most 1 MiB compared and nothing hashed |
| LS-3 | Jan's `model_sha256` was not read | it is the model file's digest with evidence `named` when `model_path` names a file; a value that is not 64 hex is `malformed_index`, and the model stays listed and held by Jan, name-addressed |

Read from source: Ollama v0.40.6 `manifest/manifest.go` (`Manifest`,
`MarshalJSON`, `isReference`, `NewManifestReference`, `ChildManifestDigest`,
`parseManifest`, `referencedBlobDigestsForData`), `manifest/manifest_test.go`
(`createManifestForTest`, `TestSelectManifestUsesRunnerPreference`) and
`manifest/paths.go` (`encodeV2ManifestHost`, `decodeV2ManifestHost`). A
reference child is `{mediaType, digest, runner, format}` with `runner` and
`format` top-level and no size; an inline child is a full manifest with no
digest; a nested list is refused; a `host:port` registry is spelled
`host%3Aport` in `manifests-v2` and `host:port` in the legacy tree, which
Windows skips. Ollama counts the named manifest's own blob, the list's blob,
each child's blob and an inline child's computed digest as referenced. This
reader holds the first three and computes no digest for an inline child,
which is no file.

Lemonade's selections in the shared Hugging Face cache become claims on the
huggingface adapter's manifests. A claim lands on the store whose manifests
name its path when two stores share a root (`snapshot.covering`), so adapter
order does not decide it. A selection with no revision applies to every
revision a ref names, or to every revision when no ref does, so a blob
several revisions share is held through each of them; the reader before
AD-06 held only the selected revision.

Inferred and still to check live:

| item | inferred from | live check that closes it |
| --- | --- | --- |
| Lemonade's service state directories `/var/lib/lemonade/.config/lemonade` and `/Library/Application Support/lemonade/.config/lemonade` | the per-user `.config/lemonade` layout placed under the service account's home; no upstream source cited, and no test, since `source.IsDir` is called on a fixed absolute path | Lemonade v2026.42.1 embeddable run as a system service on Linux and macOS, its configuration directory compared |
| how a Lemonade selection spells a ModelScope repository (`<org>/<repo>` against `modelscope--models--<org>--<repo>`) | the cache directory name Lemonade writes | Lemonade v2026.42.1 embeddable pulling one ModelScope model, `user_models.json` and the registry read back |
| Jan's MLX `model_path` naming the model's folder | the AD-06 implementer's reading of Jan's data-folder page; no `model.yml` written by Jan's MLX path was seen | a Jan v0.8.6 install on the owner's Mac downloading one MLX model |
| the list child's runner field | answered from source: top-level `runner` and `format` (Ollama v0.40.6 `MarshalJSON`) | `ollama pull` with v0.40.6 of a model with runner-specific children, compared with `ollama list` |
| the named manifest's own blob on Windows being a byte-equal copy | Ollama v0.40.6 `manifest/paths.go`'s link resolution | `ollama pull` with v0.40.6 on Windows and the snapshot's unheld bytes compared with `ollama list` |
