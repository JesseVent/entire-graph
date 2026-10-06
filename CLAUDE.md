# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

AGENTS.md (imported above) covers using the graph and the contract rules (schema, no-egress,
`IdentityRevision`). The notes below are for changing this repository.

## Commands

Needs Go 1.27, **cgo with a C compiler** (13 tree-sitter grammars compile from vendored C;
`CGO_ENABLED=0` fails with undefined `tree_sitter_*`), and Git ≥ 2.36.

```sh
mise run check      # what to run before a PR: fmt, vet, race tests, status-line suite, build
GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null XDG_CONFIG_HOME=/nonexistent \
  go test ./internal/cli -run TestRootHelpGroupsAndCommands -count=1     # one test
go test ./internal/sem -run TestProviderGoldenSnapshots -update           # regenerate golden NDJSON
sh scripts/gen-notices.sh --check   # CI-blocking; run without --check after adding a grammar or Go dep
mise run install    # build and install into the local `entire` CLI
```

- For bare `go test`, keep your own Git config away from the fixtures (the mise test tasks
  already do). Fixtures make real Git commits, so an inherited `commit.gpgsign` adds a signing
  delay to every one. Git also reads `~/.config/git/ignore` even under `GIT_CONFIG_GLOBAL=/dev/null`.
  If that lists `dist/` or `.env`, four search tests fail, which is why the command sets
  `XDG_CONFIG_HOME`.
- The full suite takes minutes on a cold cache. Run the package you touched first.
- `entire graph <verb>` execs this binary, so after `mise run build`, `./entire-graph <verb> --repo .`
  runs your working-tree build without installing it.
- CI also requires `gofmt -s -l .` to print nothing, builds on all three OSes, and runs a sharded
  Windows test job (`tools/ci-bench/windows-production/`).

## Architecture

Every verb follows one path: `cmd/entire-graph/main.go` → `cli.Run` (one `switch` in
`internal/cli/root.go`) → `runX` in `internal/cli/<verb>.go` → a `sem` entry point.

| Verbs | `internal/sem` entry point |
| --- | --- |
| `snapshot`, `symbols`, `edges` | `StreamSnapshot` (`provider.go`); durable cache in `records_cache.go` |
| `query` / `search` | `SearchRepository` (`search.go`), with ranking stages in `search_*.go` |
| `def`, `explain`, `neighbors`, `impact` | `LoadOrBuildProviderSnapshot` (`search_cache.go`), then in-memory lookups |
| `index` | `PreindexProviderSnapshot` (`search_cache.go`) |
| `diff` / `analyze`, `commit`, `checkpoint` | `AnalyzeGitRangeWithOptions` / `AnalyzeCheckpoint` (`analyze.go`) |
| `docs` | Two `LoadOrBuildProviderSnapshot` calls (HEAD, or with `--base` the merge base via `ProviderSnapshotOptions.Revision`, and the working tree), compared in `buildDocsResponse` (`internal/cli/docs.go`) over the `X-entire-graph:MENTIONS`/`LINKS_TO` edges from `markdown_relations.go`. `docs init` audits one working-tree snapshot with `sem.MarkdownReferenceProblems` and writes the embedded runner (`internal/cli/docs_init.go`) |
| `init-agents`, `agent-guide` | `internal/agentsetup` (writes `.entire/agent-guide.md` and the managed blocks) |

- **Languages:** `languageForPath` (`parser.go`) routes a file to `treeSitterLanguages`
  (semantic: symbols plus relations) or the `inventoryLanguage*` tables (file records only).
  Extraction for each language is in `internal/sem/<lang>.go`. Grammars missing from
  `smacker/go-tree-sitter` are vendored as `internal/sem/grammars/<lang>/` (parser.c + binding.go).
  `Capabilities()` must declare what each language emits; the `TestCapability*` tests check it.
- **Golden snapshots:** fixture repos live in `internal/sem/testdata/fixtures/`, listed in
  `goldenFixtures` (`golden_test.go`). Any change to symbols, relations or header stats shows
  up as a golden diff. To add a fixture, create the directory, list it, and run with `-update`.
- **Help:** each verb also needs a `commandDocs` entry (`internal/cli/help.go`).
  `help_test.go` fails if that registry and the `Run` switch drift apart.
- **Terminal safety:** repository-derived text (paths, git stderr) is untrusted and is written
  through `internal/termsafe` (`Line`, `NewJSONWriter`). The comment in `main.go` explains why.
- **Caches** are keyed on the git tree. The design and security boundary are in
  `docs/adr/0002`–`0004`.
- **Huge files:** `provider.go` is about 28.6k lines, `parser.go` about 10k and `search.go`
  about 7k. Use `rg -n` and read line ranges, not the whole file.

## Change hygiene

- User-visible changes get an entry in `CHANGELOG.md` under Unreleased. Commits use a
  Conventional Commits scope, for example `fix(sem):` or `feat(cli):`.
- Never quote a benchmark number that has no registered run behind it. See
  `bench/memory/README.md` and `docs/benchmarks.md`.
- In `CLAUDE.md` and `AGENTS.md`, `init-agents` owns only the `entire-agent:begin/end` block.
  Text outside it is preserved.

<!-- entire-agent:begin -->
<!-- Entire agent instructions are inherited through AGENTS.md. -->
<!-- entire-agent:end -->
