# Changelog

All notable changes to Entire Graph are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe user-visible behavior. Nightly prereleases
(`vX.Y.Z-nightly.*`) are snapshots of the development branch and are not listed
here. Releases before 0.5.0 have no hand-written entry: their contents are the
auto-generated notes on
[the releases page](https://github.com/entireio/entire-graph/releases).

## [0.5.0] - unreleased

### Added

- Added an experimental SCIP snapshot export, and versioned persisted diff and analyze output with schema-major enforcement, cache-validity binding, and a wire-shape regression freeze.
- Added a `graph health` command with per-source-file health accounting, and made `graph query` the canonical search command with support for trailing free-text queries.
- Added a persistent strict agent guidance mode, with Graph and Brain agent instructions coordinated to stay consistent.
- Added nightly LoCoMo benchmark CI for the entire-graph arm, authenticated via OIDC with no stored credentials.
- Added a `NOTICES` file to every release archive, covering the third-party parser sources and Go modules statically linked into the binary.
- Added experimental `X-entire-graph:MENTIONS` and `X-entire-graph:LINKS_TO` relations (full profile): a Markdown section links to the code symbols and repository files it names in inline code, and to the files and sections it links to, so a change can be traced to the docs that describe it. `neighbors` and `snapshot-query` now match relation types case-insensitively, which extension types need.
- Added `graph docs`, which lists the Markdown sections a working-tree change probably made stale (they name a changed or removed symbol, link to a changed section, or usually change with a changed file), and told agents in the generated guide to run it and update those sections in the same change.

### Changed

- Upgraded the Go toolchain to 1.27.
- Improved snapshot, search, and analyze performance and substantially reduced memory allocation, and made `stats` return a fast, single-line answer instead of a full rescan.
- Statusline now shows only the savings estimate by default and labels it clearly as an estimate rather than an exact count.
- Aligned Graph's trail runners with Brain's and hardened CI with Windows test sharding and safer nightly prerelease publishing.

### Fixed

- Fixed seven wrong-answer defects in the provider and type scanners and stopped bare type names resolving across language boundaries.
- Fixed language-scanner defect classes across C, C++, F#, and Julia, including declarator-based function naming, data-member and in-class method extraction, module-path and qualifier resolution, a forward-pipe precision bug, and bare-call scoping, and declared the type and data-flow relations all ten supported languages actually emit.
- Fixed six defects in default-export extraction, GraphQL fragment spreads, compact snapshots, and command-table search, a nested JS/TS function-expression scoping bug, and a prose-query ranking miss in search.
- Fixed nine CLI and gitutil navigation defects that previously answered wrongly instead of not answering at all, and stopped `explain` buffering its input.
- Fixed parameter-clause parsing to read every clause, let `--force` reach derived snapshots, and fixed the compact-tree walker to list only regular files.
- Fixed verification and search to stop advertising commands that cannot run, and stopped reporting a failed verification as a pass.
- Fixed pipeline workers to observe shared context correctly, closed a map-race condition, and fixed the semantic diff to report pure file renames instead of hiding them.
- Fixed the doctor handshake, git-metadata error reporting, and repository agent activation so failures explain themselves and activation survives initializers.
- Fixed statusline to prefer the managed install over a stray developer build, and fixed the LoCoMo benchmark reproduction kit to run off the author's machine with stronger scoring and redaction guards.
- Fixed Markdown extraction to track code fences, so a `# comment` inside a fence is no longer reported as a section and a closing fence is no longer a second code fence. This re-keys later `code_fence_N` symbols (identity revision 4).

### Security

- Confined worktree reads, cache writes, `init-agents` writes, and report and baseline writes to the repository or cache root, and closed an outside-path TOCTOU along with platform-specific alias-resolver rules.
- Refused to write through hard-linked targets and stopped indexing a git directory whatever its name or depth.
- Classified symlinked tree entries instead of parsing their contents.
- Bounded resource use against untrusted repository input by capping AST walk recursion, root ignore-file size and rule count, git blob read sizes, and instruction-file reads, and guarded cache-key ignore-file reads against non-regular files.
- Rejected option-shaped git revisions and refs before they reached git's argv or fetch, and escaped terminal control bytes in fatal error reports.
- Excluded credential stores such as `.env` and `.npmrc` from every graph corpus.
- Surfaced repository-controlled ignore exclusions in search instead of silently hiding matches, stopped a repository's own exclusions from vanishing silently, and honored `.graphignore` and secret-redaction rules in semantic diff output.
- Quarantined record-shaped lines found inside quoted source to block a payload and record-forgery vector.

### Documentation

- Rewrote README launch copy with a new Setup section and a LoCoMo benchmark summary, and qualified the supermemory and cmm LoCoMo comparison rows wherever they appear.
- Documented the direct release-archive download path, the Go and cgo prerequisites for building from source, and added contributor guidance.
- Updated the graphify link and version references in the README.
- Documented adopting the session before committing from a scratch worktree.

[0.5.0]: https://github.com/entireio/entire-graph/compare/v0.4.0...HEAD
