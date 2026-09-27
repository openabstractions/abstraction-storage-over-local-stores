// Command inventoryd is the first abstraction.storage/inventory-source@1
// provider. It reads every model store it knows on this machine and reports
// what each holds and which program's own index says it uses each thing.
// `inventoryd serve` answers Describe, Snapshot, Observe and Verify on the
// shared identity-bound transport for the runtime service; `inventoryd
// snapshot --json` prints the same records as a provider diagnostic.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"

	"github.com/openabstractions/abstraction-identity/listen"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/service"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

const configHelp = `Configuration (--config FILE, or the file $INVENTORYD_CONFIG names) is JSON
naming stores that have no fixed location. Every key is optional and unknown
keys are refused:
  {
    "comfyui":          ["C:/ComfyUI"],
    "swarmui":          ["D:/SwarmUI"],
    "stability_matrix": ["D:/StabilityMatrix/Data"],
    "pinokio":          ["C:/pinokio"],
    "fastflowlm":       ["D:/flm/models"],
    "extra_roots":      [{"store": "tank", "path": "E:/models", "holder": ""}]
  }
  comfyui           ComfyUI installation directories (holding main.py and models/)
  swarmui           SwarmUI installation directories (holding Data/Settings.fds)
  stability_matrix  Stability Matrix portable library directories (holding settings.json)
  pinokio           Pinokio home directories beside the one ~/.pinokio names
  fastflowlm        FastFlowLM model directories chosen at installation
  extra_roots       directories on this machine the operator names as stores;
                    holder, when set, is the program the operator says uses one
ABSTRACTION_MODEL_ROOTS ("store=dir" entries separated by the platform list
separator) adds roots as well. A network share or another machine's store is
never a store: the source runs where the store is.
`

const usage = `inventoryd reads every model store it knows on this machine and reports what
it holds and which program's own index says it uses each thing. It reads
directory listings, sizes, modification times, the programs' index files, and
the key-value header of each GGUF weight file, which names the model the
weights are. It reads no tensor, hashes nothing, and writes, moves or deletes
nothing in any store.

Usage:
  inventoryd serve --endpoint NAME [--config FILE] [--home DIR]
  inventoryd snapshot --json [--config FILE] [--home DIR]
  inventoryd help

Commands:
  serve      answer abstraction.storage/inventory-source@1 (Describe, Snapshot,
             Observe, Verify) on the identity-bound local endpoint NAME, for
             callers of this account, until interrupted
  snapshot   print what the source would report, once, as JSON on stdout: a
             provider diagnostic, never an application interface
  help       print this help (also -h, --help, or no arguments)

Options:
  --endpoint NAME  local endpoint name; the platform endpoint is
                   \\.\pipe\openabstractions-NAME on Windows and a socket in a
                   per-account directory elsewhere
  --config FILE    operator configuration, JSON; default $INVENTORYD_CONFIG
  --home DIR       read this directory as the user's home instead of the real one
  --json           print JSON (snapshot's only output format; required)

Stores: Ollama, Hugging Face hub cache (with Lemonade's registry and
user_models.json), LM Studio, ComfyUI (with extra_model_paths.yaml),
FastFlowLM, Jan, Stability Matrix, Pinokio, SwarmUI and operator-named roots.

` + configHelp

const snapshotUsage = `Usage: inventoryd snapshot --json [--config FILE] [--home DIR]

  --json          print JSON (the only output format; required)
  --config FILE   operator configuration, JSON; default $INVENTORYD_CONFIG
  --home DIR      read this directory as the user's home instead of the real one

The output holds "source" (Describe's SourceDescription) and "page" (one
complete Snapshot SourcePage) in the contract's encoding, with per-store
totals and the configuration claims behind cross-program holds.

` + configHelp

const serveUsage = `Usage: inventoryd serve --endpoint NAME [--config FILE] [--home DIR]

  --endpoint NAME  local endpoint name (required)
  --config FILE    operator configuration, JSON; default $INVENTORYD_CONFIG
  --home DIR       read this directory as the user's home instead of the real one

Serves abstraction.storage/inventory-source@1 for callers of this account
until interrupted. Registration with the platform and designation by the
runtime's rights are separate steps this command does not perform.

` + configHelp

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stdout, usage)
		return 0
	case "snapshot":
		return runSnapshot(args[1:], stdout, stderr, getenv)
	case "serve":
		return runServe(args[1:], stdout, stderr, getenv)
	}
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	fmt.Fprintf(stderr, "inventoryd: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// environment reads the configuration and home every command shares.
func environment(config, home string, getenv func(string) string) (source.Env, error) {
	cfg, err := source.LoadConfig(config)
	if err != nil {
		return source.Env{}, fmt.Errorf("configuration: %w", err)
	}
	env := source.Env{GOOS: runtime.GOOS, Getenv: getenv, Config: cfg, Home: home}
	if env.Home == "" {
		env.Home = userHome(getenv)
	}
	if env.Home == "" {
		return source.Env{}, fmt.Errorf("no home directory; pass --home")
	}
	return env, nil
}

func runSnapshot(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if wantsHelp(args) {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stdout, snapshotUsage)
		return 0
	}
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, snapshotUsage) }
	asJSON := fs.Bool("json", false, "")
	config := fs.String("config", getenv("INVENTORYD_CONFIG"), "")
	home := fs.String("home", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*asJSON {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stderr, "inventoryd snapshot: --json is required\n\n"+snapshotUsage)
		return 2
	}
	env, err := environment(*config, *home, getenv)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd snapshot: %v\n", err)
		return 2
	}
	b, err := snapshot.Take(env, snapshot.Adapters()).JSON()
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd snapshot: %v\n", err)
		return 1
	}
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	stdout.Write(b)
	return 0
}

func runServe(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if wantsHelp(args) {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stdout, serveUsage)
		return 0
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, serveUsage) }
	endpoint := fs.String("endpoint", "", "")
	config := fs.String("config", getenv("INVENTORYD_CONFIG"), "")
	home := fs.String("home", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *endpoint == "" {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprint(stderr, "inventoryd serve: --endpoint is required\n\n"+serveUsage)
		return 2
	}
	env, err := environment(*config, *home, getenv)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd serve: %v\n", err)
		return 2
	}
	src := service.New(func() snapshot.Result { return snapshot.Take(env, snapshot.Adapters()) })
	host, err := service.Listen(listen.Endpoint(*endpoint), src)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd serve: %v\n", err)
		return 1
	}
	host.OnError = func(err error) { fmt.Fprintf(stderr, "inventoryd serve: call: %v\n", err) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	fmt.Fprintf(stdout, "inventoryd serving abstraction.storage/inventory-source@1 on %s\n", listen.Endpoint(*endpoint))
	if err := host.Serve(ctx); err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "inventoryd serve: %v\n", err)
		return 1
	}
	return 0
}

func userHome(getenv func(string) string) string {
	if runtime.GOOS == "windows" {
		if h := getenv("USERPROFILE"); h != "" {
			return h
		}
	} else if h := getenv("HOME"); h != "" {
		return h
	}
	//unchecked: a failure just leaves h empty, which the caller (environment) already checks and reports as "no home directory"
	h, _ := os.UserHomeDir()
	return h
}
