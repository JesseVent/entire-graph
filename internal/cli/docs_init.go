package cli

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

//go:embed docs_runner.json
var docsRunnerSkeleton string

//go:embed docs_runner_prompt.txt
var docsRunnerPrompt string

const docsRunnerPath = ".entire/runners/trail-docs.json"

// docsInitResponse is what `docs init` reports for a repository adopting doc
// drift tracking: the drift its docs already have, how much of the code they
// name, and the setup it did.
type docsInitResponse struct {
	Sections           int                   `json:"sections"`
	SectionsNamingCode int                   `json:"sections_naming_code"`
	Problems           []sem.MarkdownProblem `json:"problems"`
	UndocumentedDirs   []docsInitDir         `json:"undocumented_dirs"`
	Runner             docsInitRunner        `json:"runner"`
	AgentGuideHasDocs  bool                  `json:"agent_guide_has_docs_step"`
	// AffectingFailures are the Markdown files that failed to parse, whose
	// problems the audit cannot see.
	AffectingFailures []sem.PartialFailure `json:"affecting_failures"`
}

// docsInitDir is a code directory no doc names anything in, with the number of
// references into it from other directories, which ranks how much it matters.
type docsInitDir struct {
	Path       string `json:"path"`
	References int    `json:"references"`
}

type docsInitRunner struct {
	Path string `json:"path"`
	// Status is "written", "exists" (left as is), or "no_runners" (the
	// repository has no .entire/runners directory to add it to).
	Status string `json:"status"`
}

func runDocsInit(ctx context.Context, opts Options, args []string) error {
	flags, err := parseDocsFlags(args)
	if err != nil {
		return err
	}
	if flags.Base != "" {
		return errors.New("docs init audits the working tree and does not take --base")
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork: true,
		Worktree:  true,
		Profile:   sem.ProfileFull,
	}, resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir), false)
	if err != nil {
		return err
	}
	readLines, _, err := openSnapshotLineReader(ctx, snapshot, true)
	if err != nil {
		return err
	}
	// Without usable history no path can be shown to have existed, so none is
	// reported as stale.
	var removedPaths func([]string) map[string]bool
	if sem.EnsureGitMetadataSafeForSubprocess(repo) == nil {
		removedPaths = func(paths []string) map[string]bool {
			deleted, err := gitutil.DeletedPaths(ctx, repo, paths)
			if err != nil {
				return nil
			}
			return deleted
		}
	}
	response := buildDocsInitResponse(snapshot, readLines, removedPaths)
	guide, _ := readLines(".entire/agent-guide.md")
	response.AgentGuideHasDocs = strings.Contains(strings.Join(guide, "\n"), "entire graph docs")
	if response.Runner, err = writeDocsRunner(ctx, repo); err != nil {
		return err
	}
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "text":
		writeDocsInitText(opts.Stdout, response, flags.Limit)
		return nil
	default:
		return fmt.Errorf("docs init --format must be text or json, got %q", flags.Format)
	}
}

func buildDocsInitResponse(snapshot sem.ProviderSnapshot, readLines lineReader, removedPaths func([]string) map[string]bool) docsInitResponse {
	response := docsInitResponse{
		Problems:          sem.MarkdownReferenceProblems(snapshot, readLines, removedPaths),
		UndocumentedDirs:  []docsInitDir{},
		AffectingFailures: []sem.PartialFailure{},
	}
	if response.Problems == nil {
		response.Problems = []sem.MarkdownProblem{}
	}
	for _, failure := range snapshot.Header.PartialFailures {
		if failure.Language == "Markdown" || strings.HasSuffix(failure.FilePath, ".md") {
			response.AffectingFailures = append(response.AffectingFailures, failure)
		}
	}

	codeDir := map[string]string{} // code file or symbol ID -> its directory
	for _, file := range snapshot.Files {
		if file.Language != "Markdown" {
			codeDir[file.ID] = path.Dir(file.Path)
		}
	}
	sections := map[string]bool{}
	for _, symbol := range snapshot.Symbols {
		switch {
		case symbol.Language == "Markdown" && symbol.Kind == "section":
			sections[symbol.ID] = true
		case symbol.Language != "Markdown":
			codeDir[symbol.ID] = path.Dir(symbol.FilePath)
		}
	}
	response.Sections = len(sections)
	namingCode := map[string]bool{}
	named := map[string]bool{}
	references := map[string]int{}
	for _, relation := range snapshot.Relations {
		switch relation.Type {
		case sem.MarkdownMentionsRelation, sem.MarkdownLinksToRelation:
			if dir, ok := codeDir[relation.ToID]; ok {
				named[dir] = true
				if sections[relation.FromID] {
					namingCode[relation.FromID] = true
				}
			}
		case "FILE_CHANGES_WITH", "SIMILAR_TO":
			// History and likeness, not references: co-change and near-duplicate
			// code (vendored grammar headers) say nothing about what other code
			// depends on.
		default:
			// A reference between nested directories (a grammar and its own
			// tree_sitter/ headers) stays inside one component, so only
			// references from unrelated directories rank how much one matters.
			from, to := codeDir[relation.FromID], codeDir[relation.ToID]
			if from != "" && to != "" && !strings.HasPrefix(to+"/", from+"/") && !strings.HasPrefix(from+"/", to+"/") {
				references[to]++
			}
		}
	}
	response.SectionsNamingCode = len(namingCode)
	for dir, count := range references {
		if !named[dir] {
			response.UndocumentedDirs = append(response.UndocumentedDirs, docsInitDir{Path: dir, References: count})
		}
	}
	sort.Slice(response.UndocumentedDirs, func(i, j int) bool {
		left, right := response.UndocumentedDirs[i], response.UndocumentedDirs[j]
		if left.References != right.References {
			return left.References > right.References
		}
		return left.Path < right.Path
	})
	if len(response.UndocumentedDirs) > 10 {
		response.UndocumentedDirs = response.UndocumentedDirs[:10]
	}
	return response
}

// writeDocsRunner adds the Doc staleness trail runner to a repository that has
// trail runners, and never replaces one already there. A repository with no
// .entire/runners directory is left alone: creating one holding only this
// runner would stand in for the default set `entire runner setup` creates.
func writeDocsRunner(ctx context.Context, repo string) (docsInitRunner, error) {
	runner := docsInitRunner{Path: docsRunnerPath}
	if info, err := os.Stat(filepath.Join(repo, ".entire", "runners")); err != nil || !info.IsDir() {
		runner.Status = "no_runners"
		return runner, nil
	}
	target := filepath.Join(repo, filepath.FromSlash(docsRunnerPath))
	if _, err := os.Lstat(target); err == nil {
		runner.Status = "exists"
		return runner, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return runner, err
	}
	config, err := docsRunnerConfig()
	if err != nil {
		return runner, err
	}
	if err := writeOutputFile(ctx, repo, target, config, 0o644, false); err != nil {
		return runner, err
	}
	runner.Status = "written"
	return runner, nil
}

// docsRunnerConfig is the runner file: the embedded skeleton with the embedded
// prompt as its template, encoded without HTML escaping so the prompt's
// `<base-ref>` placeholders stay readable in the committed file.
func docsRunnerConfig() ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(strings.TrimSpace(docsRunnerPrompt)); err != nil {
		return nil, err
	}
	config := strings.Replace(docsRunnerSkeleton, `"template": ""`, `"template": `+strings.TrimSpace(encoded.String()), 1)
	if !json.Valid([]byte(config)) {
		return nil, errors.New("docs runner template is not valid JSON")
	}
	return []byte(config), nil
}

var docsProblemLabels = map[string]string{
	"broken_link":    "broken link",
	"missing_anchor": "missing heading",
	"stale_path":     "stale path",
	"stale_name":     "stale name",
}

func writeDocsInitText(out io.Writer, response docsInitResponse, limit int) {
	fmt.Fprintf(out, "Doc audit of the working tree: %d of %d doc sections name code the graph can follow.\n",
		response.SectionsNamingCode, response.Sections)
	if len(response.AffectingFailures) > 0 {
		fmt.Fprintf(out, "Incomplete: %d Markdown file%s failed to parse and were not audited; see --format json.\n",
			len(response.AffectingFailures), pluralSuffix(len(response.AffectingFailures)))
	}

	if len(response.Problems) == 0 {
		fmt.Fprintf(out, "\nNo existing drift found in doc links, repository paths or qualified names.\n")
	} else {
		fmt.Fprintf(out, "\nExisting drift (%d). Fix these first:\n", len(response.Problems))
		for i, problem := range response.Problems {
			if i == limit {
				fmt.Fprintf(out, "... %d more (raise --limit, or use --format json)\n", len(response.Problems)-i)
				break
			}
			target := termsafe.Line(problem.Target)
			if problem.Kind == "stale_path" || problem.Kind == "stale_name" {
				target = "`" + target + "`"
			}
			fmt.Fprintf(out, "%s:%d  %s %s: %s\n", termsafe.Line(problem.Path), problem.Line,
				docsProblemLabels[problem.Kind], target, termsafe.Line(problem.Detail))
		}
	}

	if len(response.UndocumentedDirs) > 0 {
		fmt.Fprintf(out, "\nCode no doc names anything in, most referenced first:\n")
		for _, dir := range response.UndocumentedDirs {
			fmt.Fprintf(out, "  %s  (%d reference%s from other directories)\n",
				termsafe.Line(dir.Path), dir.References, pluralSuffix(dir.References))
		}
	}

	fmt.Fprintf(out, "\nSetup:\n")
	switch response.Runner.Status {
	case "written":
		fmt.Fprintf(out, "- Wrote %s, a Doc staleness runner that scores each trail. Commit it to turn it on.\n", response.Runner.Path)
	case "exists":
		fmt.Fprintf(out, "- %s already exists; left as is.\n", response.Runner.Path)
	default:
		fmt.Fprintf(out, "- This repository has no trail runners yet. Run `entire runner setup`, then `entire graph docs init` again to add the Doc staleness runner.\n")
	}
	if response.AgentGuideHasDocs {
		fmt.Fprintf(out, "- The agent guide tells agents to run `entire graph docs`.\n")
	} else {
		fmt.Fprintf(out, "- The agent guide does not mention `entire graph docs`. Run `entire graph init-agents` to add it.\n")
	}
	fmt.Fprintf(out, "\nTo keep docs trackable: name code in backticks (`pkg.Func`, `internal/x/file.go`), link sections with anchors ([text](docs/x.md#heading)), and run `entire graph docs` after each change.\n")
}
