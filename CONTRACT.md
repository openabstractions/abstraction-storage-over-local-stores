# storage-over-local-stores contract

Binds: none (records only)

This is the reading of `abstraction.storage/inventory-source@1`
(`abstraction-storage/content.thrift`), the service eleven local model stores —
Ollama, Hugging Face's hub cache, LM Studio, ComfyUI, FastFlowLM, Jan,
Stability Matrix, Pinokio, SwarmUI, operator-named directories and ASUS's
AI model folders — implement on one machine. It states what counts as a weight file, how a digest is
found, and what this reader promises never to do to a store it reads.
[README.md](README.md) names the adapter and package that carries each rule.

## Reading this page

The rule ids below, `LS-1` through `LS-12`, are what tests and refusals cite.
This page states one flow — classification, then digests, then containers,
then this reader's own promise — and its ids carry no topic letter. [HISTORY.md](HISTORY.md)
holds why the digest rules take this shape and what was measured.

The key words MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT,
RECOMMENDED, NOT RECOMMENDED, MAY and OPTIONAL in this page are to be
interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when,
they appear in all capitals, as shown here.

No word on this page waits on a later wire version: `store:<name>` and
`inventory` are unchanged wire names (research/vocabulary/DECISION.md D31,
D90), so this page carries no prose-to-wire table.

## Classification

- **[LS-1] Weight by extension.** The reader MUST accept `.gguf`,
  `.safetensors`, `.sft`, `.pth`, `.onnx`, `.pt` and `.ckpt` as naming a
  weight on their own. Extension alone MUST NOT be evidence for `.bin`:
  `.bin` also names PyTorch's own checkpoint format and vendored tooling a
  model host ships beside its store — Lemonade's cache carries ROCm
  benchmark matrices under it. OA never loads a pickle (VISION 2026-10-01,
  the third-party review rule). A weight named `.pth`, `.pt` or `.ckpt`, and
  a weight `.bin` whose header is neither GGUF's magic nor whisper.cpp's
  ggml magic (`lmgg`), is a PyTorch pickle: the
  reader MUST list it as an object and MUST NOT name it in a manifest, as
  weights or as any other entry, so no router offers it as a model. The
  model bridge's source refuses to bind one for any caller, by locator as
  well (abstraction-provider-modelbridge CONTRACT.md MODELBRIDGE-L20).
- **[LS-2] Three `.bin` conditions.** The reader MUST count a `.bin` file as
  a weight under one of three conditions: a manifest sits beside it in the
  same directory (`config.json`, `model.safetensors.index.json` or
  `pytorch_model.bin.index.json`); its path passes through a Hugging Face
  cache `snapshots/<rev>/` segment; its header opens as a format this reader
  recognises (GGUF's magic, whisper.cpp's ggml magic `lmgg`, or the zip or
  pickle signature a PyTorch checkpoint starts with). A ggml file holds
  tensors and a vocabulary and no code; it is a model format (ggml) and
  never a pickle. A `.bin` meeting none of these MUST NOT enter the
  inventory: not counted as an object, not named in a manifest.

## Digests

- **[LS-3] Digest is the blob's name.** Where a snapshot file is a symlink to
  `blobs/<sha256 or git-sha1 hex>`, the reader MUST read the linked file's
  digest from that blob's filename (`filepath.Base` on the link target), and
  the entry's `digest` (`Entry.digest`, `content.thrift:145`) MUST carry it:
  it names content, never a computed hash of the adapter's own.
- **[LS-4] Unlinked file matched by size.** Without the symlink privilege —
  Windows accounts without Developer Mode — huggingface_hub itself copies the
  blob's bytes into the snapshot instead of linking it, moving a new blob in
  with nothing left behind or copying one that already served another
  revision; the copy carries no name or metadata pointing back at its blob.
  The reader MUST match an unlinked snapshot file to the one blob of the same
  size in its repository and MUST carry that blob's digest and evidence as
  the snapshot file's own. A size collision between two blobs MUST resolve
  through the bounded read LS-6 allows, never a whole-file hash.
- **[LS-5] No blob means name addressing.** A repository with no `blobs/`
  directory at all leaves nothing for LS-4 to match against: this is
  huggingface_hub's own no-symlink fallback on the common case, a Windows
  account with no Developer Mode, and it is Lemonade's own downloader layout
  too, which writes a repository's files straight into `snapshots/<rev>/`
  without ever creating a blob (`.lemonade_registry.json` is one instance of
  it, carried by every repository in the owner's cache today). The reader
  MUST leave the entry's digest empty, with evidence none, and MUST fall
  back to name addressing, computing the manifest id from `names[0]`.

## Containers

`research/model-locators/DECISION.md` §7 rule 1 and VISION 2026-10-01 carry
the design; `go/container` implements these rules.

- **[LS-8] A member of a container is an object of its own.** In an
  operator-named root the reader MUST read each `.zip` file by its central
  directory and MUST report each member it can name as an object whose
  locator is the zip's own locator followed by `!/` and the member's name;
  a member of a nested zip appends its own `!/` and name. A member named
  `.gguf`, `.safetensors`, `.sft` or `.onnx` MUST carry a manifest of its
  own, named and described from the member's name exactly as a loose file
  of that name is. The reader MUST NOT inflate, extract or write any
  member for a snapshot. A nested zip stored uncompressed MUST be read in
  place by its own directory; a compressed nested zip MUST be reported as
  one object, its members reachable only by LS-10's streaming. The zip file
  itself counts among the store's other files. The reader reports where each
  member's stored bytes start in the container file, a DEFLATE member's
  compressed stream included, for every member inside STORED zips only; a
  member reached by streaming has no such place.
- **[LS-9] Pickles are not models.** A member named `.pth`, `.pt`, `.ckpt`,
  `.pkl`, `.pickle` or `.bin` MUST be reported as an object with no manifest: it occupies
  space and may be a copy, and OA never loads a pickle (LS-1). A `.bin`
  member is judged by name alone, since a snapshot reads no member's bytes. A member whose name is not
  valid UTF-8, contains NUL or the `!/` separator, or names a directory,
  and an encrypted member, MUST NOT be reported.
- **[LS-10] Hashing a container is explicit and bounded.** Only the
  `inventoryd containers --hash BYTES` diagnostic hashes, and it MUST charge
  every byte it passes through a hash or a decompressor to that one call's
  allowance. A member larger than what remains MUST be left without a
  digest, never hashed in part, and the call MUST report itself short. The
  digest MUST be the SHA-256 of the member's uncompressed bytes, which
  equals the digest of the file it unpacks to. A STORED member MUST be
  hashed in place; a DEFLATE member MUST be inflated in memory as it is
  hashed; a compressed nested zip MUST be streamed once, its local headers
  read in order, to list and hash its members. The CRC and size each
  directory or local header states MUST match what was read. A loose file
  whose size equals a hashed member's and whose digest equals it is the
  same object, reported as a copy.
- **[LS-11] A malformed container is refused whole.** The reader MUST
  refuse a zip whose end record, zip64 records or central directory cannot
  be read, whose directory names more than 65,536 members, whose local
  header disagrees with its directory entry, whose member's bytes lie past
  the end of the file, or whose STORED member states two different sizes;
  a refused zip in a snapshot is one store error of kind `other` naming the
  zip, and the rest of the store is still reported. A zip comment, zip64
  records and a data descriptor read through the central directory are
  accepted. A data descriptor inside a streamed nested zip MUST be refused,
  because a stream cannot locate a STORED member's end without sizes in
  its local header.

## Pinned public releases

- **[LS-12] A pinned-release store lists only verified public files.**
  ASUS's AI model folders under `%ProgramData%\ASUS` — `AICreator` (MuseTree's
  models), `LLMServer\models` and `WhisperRecorder\models`, one store each —
  hold public model files under ASUS's own names beside files ASUS made and
  PyTorch pickles, name none of them by digest, and let every account of the
  machine create files. For these stores:
  - A file is a candidate only when its path matches a pin's pattern and
    its size equals the pin's. A pin is a path pattern, an exact size and
    the SHA-256 of a public release, with that release's repository,
    revision, path, licence, role, model name and format (safetensors,
    GGUF or whisper.cpp's ggml), and applies to its own folder only. A pin
    MUST NOT name a pickle or an ASUS-made file. Every other file MUST be
    counted among the store's other files and MUST NOT be opened; LS-1's
    listing of pickles as objects does not apply here.
  - Before any byte of a candidate is read, the reader MUST refuse it
    unless identity's trusted-path rule accepts it with SYSTEM,
    Administrators and TrustedInstaller as the trusted owners
    (abstraction-identity CONTRACT.md ID-D2, `trustedpath.CheckFile`): they
    own the file and every folder above it, no other principal can change,
    delete or rename them, and no reparse point stands on the path. A
    refused candidate is a store error of kind `other` and is never
    opened.
  - A trusted candidate MUST be in the pin's format by LS-1's own rule
    (`source.IsPickle`, `source.ModelFormatOf`), read from at most its first
    four bytes; another format is a store error and is not listed.
  - The reader MUST hash a candidate in full at most once per size,
    modification time and file identity (volume serial number and file ID
    on Windows), in the background, through a handle that lets another
    program delete or rename the file meanwhile. A listing MUST NOT wait on
    a hash: a candidate whose hash has not ended is an object with no
    digest and no manifest (pending). The digest MUST be recorded with the
    size, time and identity in a folder private to this account outside
    every store, a record entry for a path the listing no longer finds MUST
    be dropped, and a record in a folder that is not private MUST be
    ignored.
  - A candidate whose digest differs from its pin, or that changes while
    it is read, MUST NOT be listed and MUST be a store error of kind `other`
    naming it.
  - A listed file's object and entry MUST carry `sha256:<hex>` with evidence
    `hashed`, so the composed inventory knows it as the same object any
    other store holds under that digest. Its manifest MUST be named
    `asus:<path under %ProgramData%\ASUS>` first and
    `hf:<repository>@<revision>:<path>` second, its entry's annotations MUST
    carry the release's repository, revision, path and licence and any use
    policy the release attaches, and a VAE's descriptor MUST name its
    transformer's family as its base (MODEL-C3).
  - An environment with no digest record lists nothing from these stores
    and counts their candidates among the other files, with no error.

## This reader's promise

- **[LS-6] Reads no tensor, hashes nothing.** The reader MUST NOT open a
  tensor and MUST NOT hash a file's bytes to produce a digest. The bound
  exceptions are LS-4's size collision, comparing two same-size candidate
  files' bytes in bounded chunks to tell them apart, never hashing a whole
  file and never opening a tensor to read it; LS-10's explicit
  `containers --hash` call, which never runs inside `Snapshot`, `Observe`
  or `Verify`; and LS-12's once-per-change hash of a pinned candidate, the
  only whole-file hash `Snapshot` and `Observe` take.
- **[LS-7] Writes, moves and deletes nothing.** The reader MUST NOT write,
  move or delete any file in any store it reads. `Remove` on
  `InventorySource` MUST answer `unsupported` for this provider. LS-12's
  digest record is the one file it writes, in OA's own per-user cache
  (`%LOCALAPPDATA%\OpenAbstractions\providers\inventoryd\digests.json`
  on Windows).

## Outcomes

| result | rule |
|---|---|
| counted as an object, weight file | LS-1, LS-2 |
| pickle weight (`.pth`, `.pt`, `.ckpt`, `.bin` neither GGUF nor ggml): object, no manifest | LS-1 |
| excluded from the inventory | LS-2 |
| `digest` carries the blob's name, evidence hashed by the link | LS-3 |
| `digest` carries the matched blob's name, evidence hashed by size | LS-4 |
| `digest` empty, evidence none, name-addressed from `names[0]` | LS-4, LS-5 |
| container member: object with a manifest, locator `zip!/member` | LS-8 |
| pickle or compressed nested zip member: object, no manifest | LS-8, LS-9 |
| member digest from `containers --hash`, or none when the allowance is short | LS-10 |
| malformed zip: store error `other` naming the zip | LS-11 |
| ASUS folder candidate, trusted, in the pin's format, hash pending: object, no digest, no manifest | LS-12 |
| ASUS folder candidate owned or changeable by another principal: store error `other`, never opened | LS-12 |
| ASUS folder file matching a pin by path, size and SHA-256: object and manifest, `sha256:` digest, evidence hashed, named by path and by release | LS-12 |
| ASUS folder file matching no pin, a pickle or an ASUS-made file: other file, never opened | LS-12 |
| ASUS folder candidate whose digest differs from its pin: store error `other`, not listed | LS-12 |
| `Remove` → `unsupported` | LS-7 |

## Bounds

A zip names at most 65,536 members, and STORED nested zips are read in place
to a depth of four (LS-11). `containers --hash` reads at most the bytes its
caller allows (LS-10). `InventorySource`'s
connection, paging and journal bounds belong to
[`abstraction-storage/CONTRACT.md`](https://github.com/openabstractions/abstraction-storage/blob/main/CONTRACT.md).

## Divergences

- **LS-6.** A content-addressed reader that verifies by hashing is the
  pattern the sibling `abstraction-storage` contract states (its writer
  hashes every accepted byte). This reader departs from it deliberately: it
  never opens a tensor or hashes a file, and stands on name addressing
  (LS-5) where no blob names a digest. LS-12 is the one store where it
  hashes: a folder that renames public files has no index to name their
  digest, and listing them without one would let a substituted file pass
  as the public release.

## Not built

Nothing here is provided by this reader beyond `Describe`, `Snapshot`,
`Observe` and `Verify`: `Remove` always answers `unsupported` (LS-7), and
this provider never talks to an application directly — its only caller is
the runtime's own `abstraction.storage/inventory@1` composition.
