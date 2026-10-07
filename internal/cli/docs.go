package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

const defaultDocsLimit = 50

type docsFlags struct {
	Repo     string
	Format   string
	Limit    int
	CacheDir string
	// Base compares the merge base of this ref and HEAD, instead of HEAD, with
	// the working tree, so a branch's commits are covered as well.
	Base string
}

// docsResponse is the worklist `docs` prints: the doc sections an uncommitted
// change probably made stale, one hop from what changed. It is a pointer for an
// agent to read and update, not a verdict; the reasons say why each is listed.
type docsResponse struct {
	// BaseRef is --base when given; BaseCommit is then its merge base with HEAD.
	BaseRef        string     `json:"base_ref,omitempty"`
	BaseCommit     string     `json:"base_commit"`
	ChangedFiles   int        `json:"changed_files"`
	ChangedSymbols int        `json:"changed_symbols"`
	Items          []docsItem `json:"items"`
	AlreadyUpdated int        `json:"already_updated"`
	Truncated      int        `json:"truncated,omitempty"`
	// Warnings and PartialFailures merge both snapshots' diagnostics, and Stats
	// is the working tree's with the worse completeness level of the two. A file
	// that failed to parse contributes no symbols, mentions or links, so docs
	// pointing at it can be missing from Items.
	Warnings        []sem.ProviderWarning `json:"warnings"`
	PartialFailures []sem.PartialFailure  `json:"partial_failures"`
	Stats           sem.ProviderStats     `json:"stats"`
	// AffectingFailures are the PartialFailures that can hide a doc from Items:
	// those in a changed file or a Markdown file. A failure elsewhere (a vendored
	// header, a minified JSON file) cannot, so text output reports only these.
	AffectingFailures []sem.PartialFailure `json:"affecting_failures"`
}

type docsItem struct {
	Path      string       `json:"path"`
	Line      int          `json:"line"`
	SectionID string       `json:"section_id,omitempty"`
	Heading   string       `json:"heading,omitempty"`
	Reasons   []docsReason `json:"reasons"`
}

type docsReason struct {
	// Kind is "mentions" (the section names the target in inline code), "links"
	// (it links to the target), or "co_changes" (the file usually changes with
	// the target in git history).
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Path   string `json:"path,omitempty"`
	// Change is "changed" or "removed" for mentions and links, and "changed" for
	// co-changes.
	Change string `json:"change"`
}

func runDocs(ctx context.Context, opts Options, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		return runDocsInit(ctx, opts, args[1:])
	}
	flags, err := parseDocsFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	load := func(worktree bool, revision string) (sem.ProviderSnapshot, error) {
		snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
			NoNetwork: true,
			Worktree:  worktree,
			Revision:  revision,
			Profile:   sem.ProfileFull,
		}, cacheDir, false)
		return snapshot, err
	}
	baseRevision := ""
	if flags.Base != "" {
		if err := sem.EnsureGitMetadataSafeForSubprocess(repo); err != nil {
			return err
		}
		if baseRevision, err = gitutil.MergeBase(ctx, repo, flags.Base, "HEAD"); err != nil {
			return err
		}
	}
	base, err := load(false, baseRevision)
	if err != nil {
		return err
	}
	if base.Header.Commit == "" {
		return errors.New("docs compares the working tree with HEAD, and this repository has no commits yet")
	}
	head, err := load(true, "")
	if err != nil {
		return err
	}
	readBase, closeBase, err := openSnapshotLineReader(ctx, base, false)
	if err != nil {
		return err
	}
	if closeBase != nil {
		defer closeBase()
	}
	readHead, _, err := openSnapshotLineReader(ctx, head, true)
	if err != nil {
		return err
	}
	response := buildDocsResponse(base, head, readBase, readHead, flags.Limit)
	response.BaseRef = flags.Base
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "text":
		writeDocsText(opts.Stdout, response)
		return nil
	default:
		return fmt.Errorf("docs --format must be text or json, got %q", flags.Format)
	}
}

func parseDocsFlags(args []string) (docsFlags, error) {
	flags := docsFlags{Format: "text", Limit: defaultDocsLimit}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		var err error
		switch arg {
		case "--repo":
			flags.Repo, err = value()
		case "--format":
			flags.Format, err = value()
		case "--cache-dir":
			flags.CacheDir, err = value()
		case "--base":
			flags.Base, err = value()
		case "--limit":
			var raw string
			if raw, err = value(); err == nil {
				flags.Limit, err = strconv.Atoi(raw)
				if err == nil && flags.Limit < 1 {
					err = errors.New("--limit must be at least 1")
				}
			}
		default:
			return flags, fmt.Errorf("docs received unexpected argument %q", arg)
		}
		if err != nil {
			return flags, err
		}
	}
	return flags, nil
}

// buildDocsResponse lists the doc sections that point, one hop, at something
// the working tree changed relative to the base snapshot (HEAD, or the --base merge base):
//
//   - a section that MENTIONS a changed or removed code symbol, or a removed file;
//   - a section that LINKS_TO a changed or removed section, or a removed file;
//   - a Markdown file that usually changes with a changed code file
//     (FILE_CHANGES_WITH) but was not touched.
//
// Mentions and links are read from both snapshots: the HEAD one is what still
// records a mention of a symbol the change removed or renamed. A section the
// change already edited is counted as updated rather than listed.
func buildDocsResponse(base, head sem.ProviderSnapshot, readBase, readHead lineReader, limit int) docsResponse {
	response := docsResponse{
		BaseCommit:      base.Header.Commit,
		Items:           []docsItem{},
		Warnings:        mergeDiagnostics(head.Header.Warnings, base.Header.Warnings),
		PartialFailures: mergeDiagnostics(head.Header.PartialFailures, base.Header.PartialFailures),
		Stats:           head.Header.Stats,
	}
	if level := base.Header.Stats.CompletenessLevel; level != "" && level != "ok" &&
		(response.Stats.CompletenessLevel == "" || response.Stats.CompletenessLevel == "ok") {
		response.Stats.CompletenessLevel = level
	}

	baseFiles := map[string]sem.FileRecord{}
	for _, file := range base.Files {
		baseFiles[file.Path] = file
	}
	headFiles := map[string]sem.FileRecord{}
	headFilePaths := map[string]string{}
	for _, file := range head.Files {
		headFiles[file.Path] = file
		headFilePaths[file.ID] = file.Path
	}
	changedFiles := map[string]bool{}
	for path, file := range headFiles {
		if before, ok := baseFiles[path]; !ok || before.Blob != file.Blob {
			changedFiles[path] = true
		}
	}
	for path := range baseFiles {
		if _, ok := headFiles[path]; !ok {
			changedFiles[path] = true
		}
	}
	response.ChangedFiles = len(changedFiles)
	response.AffectingFailures = []sem.PartialFailure{}
	for _, failure := range response.PartialFailures {
		if changedFiles[failure.FilePath] || failure.Language == "Markdown" || strings.HasSuffix(failure.FilePath, ".md") {
			response.AffectingFailures = append(response.AffectingFailures, failure)
		}
	}
	if len(changedFiles) == 0 {
		return response
	}
	changedFileIDs := map[string]string{}
	// A doc that names or links a file by path goes stale when the path stops
	// existing, not whenever the file's content changes: a section naming
	// `CLAUDE.md` describes the file's role, not its text. Content changes are
	// caught precisely through symbol mentions and anchored section links.
	removedFileIDs := map[string]bool{}
	for path := range changedFiles {
		if file, ok := headFiles[path]; ok {
			changedFileIDs[file.ID] = path
		} else {
			changedFileIDs[baseFiles[path].ID] = path
			removedFileIDs[baseFiles[path].ID] = true
		}
	}

	headSymbols := map[string]sem.SymbolRecord{}
	for _, symbol := range head.Symbols {
		headSymbols[symbol.ID] = symbol
	}
	baseSymbols := map[string]sem.SymbolRecord{}
	for _, symbol := range base.Symbols {
		baseSymbols[symbol.ID] = symbol
	}

	// Code symbols whose body changed or that the change removed. Added symbols
	// are left out: no existing doc can describe them yet.
	codeChanges := map[string]string{}
	for id, before := range baseSymbols {
		if !changedFiles[before.FilePath] || before.Language == "Markdown" {
			continue
		}
		if after, ok := headSymbols[id]; !ok {
			codeChanges[id] = "removed"
		} else if after.BodyHash != before.BodyHash {
			codeChanges[id] = "changed"
		}
	}
	response.ChangedSymbols = len(codeChanges)

	sectionChanges := changedDocSections(base, head, changedFiles, baseFiles, headFiles, readBase, readHead)

	items := map[string]*docsItem{}
	alreadyUpdated := map[string]bool{}
	addReason := func(fromID string, reason docsReason) {
		if sectionChanges[fromID] != "" {
			alreadyUpdated[fromID] = true
			return
		}
		item := items[fromID]
		if item == nil {
			var ok bool
			// An archived doc or a changelog records the past; it is not
			// expected to follow the code.
			if item, ok = docsItemFor(fromID, headSymbols, headFilePaths); !ok || sem.MarkdownHistoricalDoc(item.Path) {
				return
			}
			items[fromID] = item
		}
		for _, existing := range item.Reasons {
			if existing == reason {
				return
			}
		}
		item.Reasons = append(item.Reasons, reason)
	}
	targetOf := func(id string) (target, path string) {
		if path, ok := changedFileIDs[id]; ok {
			return path, path
		}
		symbol, ok := headSymbols[id]
		if !ok {
			symbol = baseSymbols[id]
		}
		name := symbol.QualifiedName
		if name == "" {
			name = symbol.Name
		}
		if symbol.Kind == "section" {
			name = symbol.FilePath + "#" + docsHeading(symbol)
		}
		return name, symbol.FilePath
	}

	for _, relations := range [][]sem.RelationRecord{head.Relations, base.Relations} {
		for _, relation := range relations {
			switch relation.Type {
			case sem.MarkdownMentionsRelation:
				change := codeChanges[relation.ToID]
				if change == "" && removedFileIDs[relation.ToID] {
					change = "removed"
				}
				if change != "" {
					target, path := targetOf(relation.ToID)
					addReason(relation.FromID, docsReason{Kind: "mentions", Target: target, Path: path, Change: change})
				}
			case sem.MarkdownLinksToRelation:
				// sectionChanges also keys a document's preamble by its file ID, so
				// only a section target reads it; a file target follows the
				// removed-path rule above.
				change := ""
				if relation.TargetKind == "symbol" {
					change = sectionChanges[relation.ToID]
				} else if removedFileIDs[relation.ToID] {
					change = "removed"
				}
				if change != "" {
					target, path := targetOf(relation.ToID)
					addReason(relation.FromID, docsReason{Kind: "links", Target: target, Path: path, Change: change})
				}
			}
		}
	}
	// Co-change pairs need both files present, so a pair with a removed file
	// survives only in the HEAD snapshot. Only a code change uses them: a doc
	// edit reaches other docs through links and mentions, and a hub such as
	// README.md co-changes with most docs, so it would list them all.
	docPartners := map[string]map[string]string{} // changed code path -> doc ID -> doc path
	for _, relation := range append(append([]sem.RelationRecord{}, head.Relations...), base.Relations...) {
		if relation.Type != "FILE_CHANGES_WITH" {
			continue
		}
		for _, pair := range [][2]string{{relation.FromID, relation.ToID}, {relation.ToID, relation.FromID}} {
			changedPath, docPath := changedFileIDs[pair[0]], headFilePaths[pair[1]]
			if changedPath == "" || docPath == "" || headFiles[docPath].Language != "Markdown" ||
				headFiles[changedPath].Language == "Markdown" || baseFiles[changedPath].Language == "Markdown" {
				continue
			}
			if docPartners[changedPath] == nil {
				docPartners[changedPath] = map[string]string{}
			}
			docPartners[changedPath][pair[1]] = docPath
		}
	}
	for changedPath, docs := range docPartners {
		// ponytail: a code file that co-changes with more than three docs is a
		// hub (help text, a command switch) whose pairs say little about any one
		// doc, so it contributes none. Upgrade: weigh shared commits against
		// each file's own commit count, which the edge does not carry today.
		if len(docs) > 3 {
			continue
		}
		for docID, docPath := range docs {
			if !changedFiles[docPath] {
				addReason(docID, docsReason{Kind: "co_changes", Target: changedPath, Path: changedPath, Change: "changed"})
			}
		}
	}

	response.AlreadyUpdated = len(alreadyUpdated)
	for _, item := range items {
		response.Items = append(response.Items, *item)
	}
	sort.Slice(response.Items, func(i, j int) bool {
		left, right := response.Items[i], response.Items[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.Line < right.Line
	})
	if len(response.Items) > limit {
		response.Truncated = len(response.Items) - limit
		response.Items = response.Items[:limit]
	}
	return response
}

// changedDocSections maps the ID of every Markdown section, and of the file for
// text above a document's first heading, whose text differs between HEAD and the
// working tree to "changed", or to "removed" when it no longer exists. Section
// symbols span only their heading line, so a section's text is everything from
// its heading to the next one.
func changedDocSections(base, head sem.ProviderSnapshot, changedFiles map[string]bool, baseFiles, headFiles map[string]sem.FileRecord, readBase, readHead lineReader) map[string]string {
	headSections, baseSections := docSectionsByFile(head.Symbols), docSectionsByFile(base.Symbols)
	changes := map[string]string{}
	for path := range changedFiles {
		headFile, inHead := headFiles[path]
		baseFile, inBase := baseFiles[path]
		if inHead && headFile.Language != "Markdown" || !inHead && baseFile.Language != "Markdown" {
			continue
		}
		after := map[string]string{}
		if inHead {
			after = docSectionTexts(path, headFile.ID, headSections[path], readHead)
		}
		before := map[string]string{}
		if inBase {
			before = docSectionTexts(path, baseFile.ID, baseSections[path], readBase)
		}
		for id, text := range after {
			if previous, ok := before[id]; !ok || previous != text {
				changes[id] = "changed"
			}
		}
		for id := range before {
			if _, ok := after[id]; !ok {
				changes[id] = "removed"
			}
		}
	}
	return changes
}

// docSectionsByFile groups Markdown section symbols by file, in line order.
func docSectionsByFile(symbols []sem.SymbolRecord) map[string][]sem.SymbolRecord {
	sections := map[string][]sem.SymbolRecord{}
	for _, symbol := range symbols {
		if symbol.Language == "Markdown" && symbol.Kind == "section" && symbol.StartLine > 0 {
			sections[symbol.FilePath] = append(sections[symbol.FilePath], symbol)
		}
	}
	for _, list := range sections {
		sort.Slice(list, func(i, j int) bool { return list[i].StartLine < list[j].StartLine })
	}
	return sections
}

// docSectionTexts returns each section's text keyed by section ID, plus the
// preamble above the first heading keyed by the file ID.
func docSectionTexts(path, fileID string, sections []sem.SymbolRecord, read lineReader) map[string]string {
	lines, ok := read(path)
	if !ok {
		return map[string]string{}
	}
	texts := map[string]string{}
	start, id := 1, fileID
	for _, section := range sections {
		texts[id] = docLines(lines, start, section.StartLine-1)
		start, id = section.StartLine, section.ID
	}
	texts[id] = docLines(lines, start, len(lines))
	return texts
}

func docLines(lines []string, first, last int) string {
	if first < 1 {
		first = 1
	}
	if last > len(lines) {
		last = len(lines)
	}
	if first > last {
		return ""
	}
	return strings.Join(lines[first-1:last], "\n")
}

func docsItemFor(id string, symbols map[string]sem.SymbolRecord, filePaths map[string]string) (*docsItem, bool) {
	if symbol, ok := symbols[id]; ok {
		return &docsItem{Path: symbol.FilePath, Line: symbol.StartLine, SectionID: symbol.ID, Heading: docsHeading(symbol)}, true
	}
	if path := filePaths[id]; path != "" {
		return &docsItem{Path: path, Line: 1}, true
	}
	return nil, false
}

func docsHeading(symbol sem.SymbolRecord) string {
	return strings.TrimPrefix(symbol.Signature, "markdown heading ")
}

// mergeDiagnostics concatenates diagnostic lists, dropping exact duplicates (a
// file that fails to parse in both snapshots is reported once).
func mergeDiagnostics[T comparable](lists ...[]T) []T {
	merged := []T{}
	seen := map[T]bool{}
	for _, list := range lists {
		for _, diagnostic := range list {
			if !seen[diagnostic] {
				seen[diagnostic] = true
				merged = append(merged, diagnostic)
			}
		}
	}
	return merged
}

func writeDocsText(out io.Writer, response docsResponse) {
	commit := response.BaseCommit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	against := "HEAD (" + commit + ")"
	if response.BaseRef != "" {
		against = termsafe.Line(response.BaseRef) + " (merge base " + commit + ")"
	}
	if response.ChangedFiles == 0 {
		fmt.Fprintf(out, "No changes against %s.\n", against)
		return
	}
	if len(response.AffectingFailures) > 0 {
		fmt.Fprintf(out, "Incomplete: %d changed or Markdown file%s failed to parse, so docs pointing at them may be missing:\n",
			len(response.AffectingFailures), pluralSuffix(len(response.AffectingFailures)))
		for i, failure := range response.AffectingFailures {
			if i == 5 {
				fmt.Fprintf(out, "- ... %d more; see --format json\n", len(response.AffectingFailures)-i)
				break
			}
			fmt.Fprintf(out, "- %s: %s\n", failure.Code, termsafe.Line(failure.FilePath))
		}
	}
	if len(response.Items) == 0 {
		fmt.Fprintf(out, "No docs point at what changed against %s: %d files, %d symbols.\n", against, response.ChangedFiles, response.ChangedSymbols)
	} else {
		fmt.Fprintf(out, "Docs that may be stale after these changes against %s. Update them in the same change:\n", against)
	}
	for _, item := range response.Items {
		heading := "(file)"
		if item.Heading != "" {
			heading = item.Heading
		}
		fmt.Fprintf(out, "%s:%d  %s\n", termsafe.Line(item.Path), item.Line, termsafe.Line(heading))
		var coChanges []string
		for _, reason := range item.Reasons {
			switch reason.Kind {
			case "mentions":
				fmt.Fprintf(out, "  - mentions `%s` (%s)\n", termsafe.Line(reason.Target), reason.Change)
			case "links":
				fmt.Fprintf(out, "  - links to %s (%s)\n", termsafe.Line(reason.Target), reason.Change)
			case "co_changes":
				coChanges = append(coChanges, termsafe.Line(reason.Target))
			}
		}
		if len(coChanges) > 0 {
			sort.Strings(coChanges)
			fmt.Fprintf(out, "  - usually changes with %s\n", strings.Join(coChanges, ", "))
		}
	}
	if response.Truncated > 0 {
		fmt.Fprintf(out, "... %d more (raise --limit, or use --format json)\n", response.Truncated)
	}
	if response.AlreadyUpdated > 0 {
		fmt.Fprintf(out, "%d doc sections that point at these changes were already edited in them.\n", response.AlreadyUpdated)
	}
}
