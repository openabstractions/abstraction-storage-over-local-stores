# abstraction-storage-over-local-stores

*For provider implementers. An application reads what this provider reports
through the runtime's `abstraction.storage/inventory@1`.*

This provider lists the model files that Ollama, LM Studio, the Hugging Face
cache and other programs already hold on one machine. The full list is Ollama,
Hugging Face's hub cache, LM Studio, ComfyUI, FastFlowLM, Jan, Stability
Matrix, Pinokio, SwarmUI, operator-named directories and ASUS's AI model
folders (MuseTree's among them). For each it reports what the store holds and
which program's own index says depends on it. It reads
directory listings, sizes, modification times, each program's own index files
and the key-value header of each GGUF weight file, which is where a model
descriptor's architecture, size label, fine-tune and base come from. In an
operator-named root it reads each zip by its central directory and reports
each member as an object of its own. Its snapshots read no
tensor and hash nothing ([CONTRACT.md](CONTRACT.md)) except in ASUS's
model folders. There a file whose path and size match a pinned public release,
and that SYSTEM, Administrators or TrustedInstaller owns with no other
account able to change it, is hashed once per size, modification time and
file identity, in the background, and listed only when its SHA-256 is the
release's; the `inventoryd containers --hash` diagnostic hashes
container members within an allowance its caller names. It writes, moves or
deletes nothing in any store it reads. [Contract rules](#contract-rules) maps these
statements to their contract rules.

**What's new here:** One read-only inventory of the model files Ollama, LM
Studio, the Hugging Face cache, ComfyUI and other applications already hold,
read in place.

This layer is `abstraction-storage`'s [`abstraction.storage/inventory-source@1`](https://github.com/openabstractions/abstraction-storage/blob/main/content.thrift)
provider, one of five content-references services that repository's own
`README.md` names. The user runtime serves `Inventory` composed from every
accepted source, including this one; `Manifests`, `Holds` and
`ContentRemover` have generated codecs there and no server anywhere yet.
This provider never talks to applications. Its caller is that runtime
composition, which applications read under
`abstraction.storage/inventory.read`; its other consumers are its own
diagnostic command and its own tests.

## Layout

- **`go/adapters/`** — one package per store: `ollama`, `huggingface`,
  `lmstudio`, `comfyui`, `fastflowlm`, `jan`, `stabilitymatrix`, `pinokio`,
  `swarmui`, `extra` (operator-named directories from configuration and
  `ABSTRACTION_MODEL_ROOTS`), `asusai` (`%ProgramData%\ASUS\AICreator`,
  `LLMServer\models` and `WhisperRecorder\models`, read against a pin table of
  public releases, LS-12; Windows only, where the folders exist and names
  match ignoring case, so its matching tests skip elsewhere), and `tree`, the shared reader for stores whose
  directory tree is their only index (LM Studio, ComfyUI, Stability Matrix,
  Pinokio's drive, SwarmUI, Lemonade's extra models directory and operator
  roots). Each adapter's package doc names exactly what it reads — for
  example `huggingface.go`: "the Hugging Face hub cache and the two Lemonade
  indexes that name content in it," with the cache and index layout spelled
  out above the code. `go/adapters/tree/tree.go` and `go/adapters/jan/jan.go`
  apply `source.IsWeightFile` ([CONTRACT.md](CONTRACT.md) LS-1, LS-2); the
  Hugging Face adapter passes `.bin` to `source.IsWeights` as an extra
  extension directly, skipping the sibling-manifest and header checks the
  generic rule needs, and its own `matchBlobBySize`
  (`go/adapters/huggingface/huggingface.go`) implements LS-3 through LS-5.
- **`go/service/`** — `source.go`'s `Source` implements `InventorySource`
  over one machine, journaling changes between observations and paging
  snapshots; `host.go`'s `Host` serves it on the shared identity-bound native
  transport (a named pipe on Windows, an account-scoped socket elsewhere),
  admitting only callers of this account.
- **`go/source/`** — what every adapter shares: `Env` and `Config` (the
  operator configuration shape, `env.go`), the stat-only `Walk` (`walk.go`),
  the `Manifest`/`Hold`/`Dangling` constructors with the seeded roles,
  purposes and kinds (`manifest.go`), and the `abstraction.model/descriptor@1`
  fields an adapter fills from a store's own naming and a file's own header,
  cached by size and modification time (`descriptor.go`), and `Digests`, the
  record of LS-12's whole-file digests, hashed in the background and kept
  in a folder private to this account in OA's own cache
  (`digests.go`).
- **`go/container/`** — zip containers read without extraction: `Members`
  reads central directories (LS-8, LS-9, LS-11), `HashMembers` hashes weight
  members in place or by streaming decompression within an `Allowance`
  (LS-10), `Copies` finds loose files holding the same bytes, and `MemberLocator`
  spells a member's locator.
- **`go/gguf/`** — the GGUF key-value header reader: architecture,
  quantisation, base name, size label, fine-tune and base model, read without
  opening a tensor.
- **`go/cmd/inventoryd/`** — the command described below.
- **`go/snapshot/`** — `Take` runs every adapter over one `Env` and composes
  their observations into one `Result`: the contract records
  (`content.SourceDescription`, `content.SourcePage`) plus per-store
  `Summary` totals, in report order `ollama, huggingface, lmstudio, comfyui,
  fastflowlm, jan, stabilitymatrix, pinokio, swarmui, extra, asusai`
  ([`go/snapshot/snapshot.go`](go/snapshot/snapshot.go)).
- **`go/internal/`** — `fixture`, a synthetic machine used only by this
  module's own tests.

`inventoryd` is the one binary this layer produces; `snapshot` is its
diagnostic subcommand.

## inventoryd: the provider

`inventoryd` is a provider ([`go/cmd/inventoryd/main.go`](go/cmd/inventoryd/main.go)).
It serves `abstraction.storage/inventory-source@1` to the runtime. The
contract for that service states sources never talk to applications
([`abstraction-storage/content.thrift`](https://github.com/openabstractions/abstraction-storage/blob/main/content.thrift),
the `InventorySource` service doc).

```
inventoryd serve --endpoint NAME [--config FILE] [--home DIR]
inventoryd snapshot --json [--config FILE] [--home DIR]
inventoryd help
```

`serve` answers `Describe`, `Snapshot`, `Observe` and `Verify` on the named
local endpoint until interrupted; `Remove` always returns `unsupported`
because this provider lists no remove capability
([`go/service/source.go`](go/service/source.go)). Every call is
rechecked against the connecting peer: a caller of another account reads
`forbidden`, an identity the transport could not establish is the error code
`caller_unavailable`, and a caller below the platform's Program proof is
`identity_required` ([`go/service/host.go`](go/service/host.go)).
`--config FILE` (or `$INVENTORYD_CONFIG`) names an optional JSON file for
stores with no fixed location — `comfyui`, `swarmui`, `stability_matrix`,
`pinokio`, `fastflowlm` and `extra_roots`; `ABSTRACTION_MODEL_ROOTS` adds
extra roots the same way `abstraction-model`'s own inventory already honours
them. Running `inventoryd help` (or with no arguments) prints the current
option list and the configuration shape in full.

### Build and run from source

No tagged release exists, no `go get` line and no published repository (see
Today, below). Building means building the `go` module beside this file:

```sh
cd go
go build -o bin/ ./cmd/inventoryd
bin/inventoryd serve --endpoint inventoryd-v1
```

`go test ./...` covers every adapter, `service`, `snapshot` and `source`;
`cmd/inventoryd`'s own tests exercise `help`, `snapshot --json` and argument
handling.

### How the runtime accepts a source

`content.thrift`'s own doc for `InventorySource` states the design: the
runtime reads a source over the shared identity-bound transport, and accepts
it only under an `abstraction.storage/inventory.provide` rule naming the
bound program and its stores. The runtime does this through a declaration in
its provider registry, `abstraction.facade/registry@1` (the facade contract's
FAC-R2 and FAC-R3), and reads storage's own `inventory.provide` rule for each
store:

```
openabstractions provider add local-stores --program /abs/path/inventoryd \
  --provider-endpoint inventoryd-v1 --contract abstraction.storage/inventory-source@1 \
  --arg serve --arg --endpoint --arg {endpoint} --store ollama --store huggingface
```

The declaration is `providers/local-stores.json` in the runtime state, with
the resources `store:ollama` and `store:huggingface`. The command writes `inventory.provide` on `store:ollama` and `store:huggingface`
for that program, with why `provider add`. The runtime launches `inventoryd`
when it starts serving, restarts it after an exit, and calls endpoint@1
`Describe` and then the source's own `Describe` over a connection that
requires the declared program as the server. It accepts each described store
that the declaration names and the rule permits, and lists them as `accepted`
in `openabstractions provider list`. A source
nobody declared is never read, a process at the endpoint running another
program is `refused`, and a store the person denies is dropped at the next
reading. serve's `TestADeclaredInventorySourceIsListedAndAnUndeclaredOneIsRefused`
builds `inventoryd` and runs this against fixture homes.

The runtime serves `abstraction.storage/inventory@1` on its storage endpoint,
composed from every accepted source: stores on the first page, then
manifests, stray objects and dangling references, gated by
`abstraction.storage/inventory.read` and filtered per digest by the content
read policy (`abstraction-storage/CONTRACT.md`, "Composed inventory"). The
router's model catalogue carries the store names as `held_in` for each family
and alias. `Manifests`, `Holds` and `ContentRemover` have no server yet.
Nothing here installs `inventoryd` as a
persistent service, registers it with the platform (a scheduled task, a
`launchd` agent, a `systemd` user unit) or writes it a `FOOTPRINT.md` row.

## snapshot: a diagnostic

`go/snapshot` is a diagnostic. `inventoryd snapshot --json` prints, once, to
stdout, what `Describe` and a complete `Snapshot` would report: the source
description, the full `SourcePage` (every store, every manifest with its
observed holds, every stray object no manifest names, every dangling
reference), an `observed_at` timestamp, elapsed seconds, and per-store
`Summary` totals — object and byte counts, partial and unheld bytes, held
bytes by holder program, and error counts
([`go/snapshot/snapshot.go`](go/snapshot/snapshot.go)). An operator or a test
reads with it what the provider sees without standing up a listener.

## Requirements

**Go** 1.26 or later ([`go/go.mod`](go/go.mod)), plus
[`abstraction-identity`](https://github.com/openabstractions/abstraction-identity),
[`abstraction-storage/go`](https://github.com/openabstractions/abstraction-storage)
(the content records this layer composes into),
`abstraction-identity`'s `go/privatedir` (identity ID-D1, which keeps
LS-12's digest record private to this account) and `go/trustedpath`
(ID-D2, LS-12's owner and access check), `golang.org/x/sys` (file identity, delete-sharing reads and the
owner check on Windows) and `gopkg.in/yaml.v3` (read by the ComfyUI
adapter's `extra_model_paths.yaml`). The recorded evidence
(`research/feedback-archive-2026-09-24/content-definitions-and-inventoryd.md` in the parent project) covers
Windows tests, `go vet` for `GOOS=linux` and `GOOS=darwin`, and a mutation check
of all ten adapters. `inventoryd serve` as a standalone process, and the
`caller_unavailable` path over a real transport, are untested.

## Today

This layer publishes to
`github.com/openabstractions/abstraction-storage-over-local-stores`. Its first
tag, `go/v0.1.0`, is published, and
`go get github.com/openabstractions/abstraction-storage-over-local-stores/go@v0.2.0`
fetches it. The OpenAbstractions installer's "Local model stores" feature, on
by default, ships `inventoryd` and declares it to the runtime, which starts it
on demand. Nothing registers it as a persistent service.

## Contract rules

The opening section's statements are these [CONTRACT.md](CONTRACT.md) rules:
zip members as objects of their own, LS-8 and LS-9; snapshots read no tensor
and hash nothing, LS-6; the ASUS release hash, LS-12; the
`containers --hash` allowance, LS-10; nothing written, moved or deleted, LS-7.

## Licence

Apache-2.0. See [LICENSE](LICENSE), published beside this file at the
repository root.
