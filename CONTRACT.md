# storage-over-local-stores contract

Binds: none (records only)

This is the reading of `abstraction.storage/inventory-source@1`
(`abstraction-storage/content.thrift`), the service ten local model stores —
Ollama, Hugging Face's hub cache, LM Studio, ComfyUI, FastFlowLM, Jan,
Stability Matrix, Pinokio, SwarmUI and operator-named directories — implement
on one machine. It states what counts as a weight file, how a digest is
found, and what this reader promises never to do to a store it reads.
[README.md](README.md) names the adapter and package that carries each rule.

## Reading this page

The rule ids below, `LS-1` through `LS-7`, are what tests and refusals cite.
This page states one flow — classification, then digests, then this reader's
own promise — and its ids carry no topic letter. [HISTORY.md](HISTORY.md)
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
  benchmark matrices under it.
- **[LS-2] Three `.bin` conditions.** The reader MUST count a `.bin` file as
  a weight under one of three conditions: a manifest sits beside it in the
  same directory (`config.json`, `model.safetensors.index.json` or
  `pytorch_model.bin.index.json`); its path passes through a Hugging Face
  cache `snapshots/<rev>/` segment; its header opens as a format this reader
  recognises (GGUF's magic, or the zip or pickle signature a PyTorch
  checkpoint starts with). A `.bin` meeting none of these MUST NOT enter the
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

## This reader's promise

- **[LS-6] Reads no tensor, hashes nothing.** The reader MUST NOT open a
  tensor and MUST NOT hash a file's bytes to produce a digest. The one bound
  exception is LS-4's size collision: comparing two same-size candidate
  files' bytes in bounded chunks to tell them apart, never hashing a whole
  file and never opening a tensor to read it.
- **[LS-7] Writes, moves and deletes nothing.** The reader MUST NOT write,
  move or delete any file in any store it reads. `Remove` on
  `InventorySource` MUST answer `unsupported` for this provider.

## Outcomes

| result | rule |
|---|---|
| counted as an object, weight file | LS-1, LS-2 |
| excluded from the inventory | LS-2 |
| `digest` carries the blob's name, evidence hashed by the link | LS-3 |
| `digest` carries the matched blob's name, evidence hashed by size | LS-4 |
| `digest` empty, evidence none, name-addressed from `names[0]` | LS-4, LS-5 |
| `Remove` → `unsupported` | LS-7 |

## Bounds

This reader states no numeric bound of its own. `InventorySource`'s
connection, paging and journal bounds belong to
[`abstraction-storage/CONTRACT.md`](https://github.com/openabstractions/abstraction-storage/blob/main/CONTRACT.md).

## Divergences

- **LS-6.** A content-addressed reader that verifies by hashing is the
  pattern the sibling `abstraction-storage` contract states (its writer
  hashes every accepted byte). This reader departs from it deliberately: it
  never opens a tensor or hashes a file, and stands on name addressing
  (LS-5) where no blob names a digest.

## Not built

Nothing here is provided by this reader beyond `Describe`, `Snapshot`,
`Observe` and `Verify`: `Remove` always answers `unsupported` (LS-7), and
this provider never talks to an application directly — its only caller is
the runtime's own `abstraction.storage/inventory@1` composition.
