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
