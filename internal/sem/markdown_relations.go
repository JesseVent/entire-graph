package sem

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Markdown prose relations. They are experimental, so they carry the provider
// namespace ADR 0001 rule 4 reserves for that: the shape can change without a
// schema major, and a tolerant reader that does not know them ignores them.
const (
	// MarkdownMentionsRelation runs from a Markdown section (or the file, for the
	// preamble above its first heading) to a code symbol or repository file the
	// prose names in inline code.
	MarkdownMentionsRelation = "X-entire-graph:MENTIONS"
	// MarkdownLinksToRelation runs from a Markdown section to the repository file,
	// or the section of one, that a Markdown link points at.
	MarkdownLinksToRelation = "X-entire-graph:LINKS_TO"
)

var (
	markdownLinkRe         = regexp.MustCompile(`(!?)\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+["'][^)]*["'])?\s*\)`)
	markdownLinkSchemeRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	markdownMentionIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
)

// markdownRelations emits MENTIONS and LINKS_TO for every Markdown file in
// recordsByFile. Lines inside code fences are code, not prose, and are skipped:
// a fenced shell session names commands, not the symbols a section is about.
//
// MENTIONS resolves an inline code span two ways. A span that is a repository
// path (optionally `:line`) names that file exactly. A span shaped like an
// identifier (`Name`, `Name()`, `pkg.Name`, `Type.Member`) resolves against the
// workspace's code symbols by short name and only when exactly one candidate
// survives, so `Run` with three definitions names nothing rather than the one
// that sorts first. Spans that read as ordinary words (`query`, `index`) are
// not names at all and are skipped.
func markdownRelations(repoKey string, recordsByFile map[string][]SymbolRecord, symbolsByShortName map[string][]SymbolRecord, knownFiles map[string]bool, readContent contentReader, spec profileSpec) []RelationRecord {
	paths := make([]string, 0, len(recordsByFile))
	for docPath := range recordsByFile {
		paths = append(paths, docPath)
	}
	sort.Strings(paths)

	anchorsByFile := map[string]map[string]string{}
	anchors := func(file string) map[string]string {
		if found, ok := anchorsByFile[file]; ok {
			return found
		}
		found := markdownSectionAnchors(recordsByFile[file])
		anchorsByFile[file] = found
		return found
	}

	var relations []RelationRecord
	seen := map[string]bool{}
	add := func(relation RelationRecord) {
		key := relation.FromID + "\x00" + relation.ToID + "\x00" + relation.Type
		if relation.FromID == relation.ToID || seen[key] {
			return
		}
		seen[key] = true
		relations = append(relations, relation)
	}
	for _, docPath := range paths {
		content, ok := readContent(docPath)
		if !ok {
			continue
		}
		sectionsByLine := map[int]string{}
		for _, symbol := range recordsByFile[docPath] {
			if symbol.Kind == "section" && symbol.StartLine > 0 {
				sectionsByLine[symbol.StartLine] = symbol.ID
			}
		}
		headingLines := proseSectionHeadingLines(recordsByFile[docPath])
		lines := strings.Split(content, "\n")
		inFence := markdownFenceLines(lines)
		for index, line := range lines {
			if inFence[index] {
				continue
			}
			lineNumber := index + 1
			from := fileID(repoKey, docPath)
			if heading := proseSectionHeadingAt(headingLines, lineNumber); heading > 0 {
				from = sectionsByLine[heading]
			}
			spans, prose := markdownCodeSpans(line)
			if spec.emits(MarkdownMentionsRelation) {
				for _, span := range spans {
					toID, targetKind, resolution, confidence, ok := markdownMentionTarget(repoKey, span, knownFiles, symbolsByShortName)
					if !ok {
						continue
					}
					add(markdownRelation(from, toID, MarkdownMentionsRelation, confidence, "Markdown names this in inline code", resolution, targetKind, "inline_code", docPath, lineNumber, span))
				}
			}
			if spec.emits(MarkdownLinksToRelation) {
				for _, match := range markdownLinkRe.FindAllStringSubmatch(prose, -1) {
					if match[1] == "!" {
						continue
					}
					toID, targetKind, ok := markdownLinkTarget(repoKey, docPath, match[2], knownFiles, anchors)
					if !ok {
						continue
					}
					add(markdownRelation(from, toID, MarkdownLinksToRelation, 1, "Markdown link", "exact", targetKind, "link", docPath, lineNumber, match[2]))
				}
			}
		}
	}
	return relations
}

func markdownRelation(from, to, relationType string, confidence float64, reason, resolution, targetKind, evidenceKind, docPath string, line int, detail string) RelationRecord {
	return RelationRecord{
		RecordType:    "relation",
		FromID:        from,
		ToID:          to,
		Type:          relationType,
		Confidence:    confidence,
		Reason:        reason,
		RelationScope: "workspace",
		Resolution:    resolution,
		TargetKind:    targetKind,
		Evidence: []Evidence{{
			Kind:      evidenceKind,
			FilePath:  docPath,
			StartLine: line,
			EndLine:   line,
			Detail:    detail,
		}},
		WarningCodes: []string{},
	}
}

func markdownMentionTarget(repoKey, span string, knownFiles map[string]bool, symbolsByShortName map[string][]SymbolRecord) (toID, targetKind, resolution string, confidence float64, ok bool) {
	if file, _, _ := strings.Cut(span, ":"); knownFiles[file] {
		return fileID(repoKey, file), "file", "exact", 0.9, true
	}
	name, called := strings.CutSuffix(span, "()")
	if !markdownMentionIdentRe.MatchString(name) {
		return "", "", "", 0, false
	}
	qualifier, short := "", name
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		qualifier, short = name[:dot], name[dot+1:]
	}
	if !markdownMentionDistinctive(short, qualifier != "" || called) {
		return "", "", "", 0, false
	}
	var candidates []SymbolRecord
	for _, symbol := range symbolsByShortName[short] {
		if !markdownMentionTargetKind(symbol.Kind) {
			continue
		}
		// A qualifier must agree with the candidate: `Result.Field` names the
		// member of Result, and `sem.Func` names the function in package sem.
		if qualifier != "" && symbol.QualifiedName != name && !strings.HasSuffix(symbol.QualifiedName, "."+name) && path.Base(path.Dir(symbol.FilePath)) != qualifier {
			continue
		}
		candidates = append(candidates, symbol)
	}
	if len(candidates) != 1 {
		return "", "", "", 0, false
	}
	return candidates[0].ID, "symbol", "name_only", 0.6, true
}

// markdownMentionDistinctive reports whether a backticked word is shaped like a
// name rather than prose. Short names and plain lowercase words collide with
// ordinary English far too often to name one definition; a qualifier, a call
// suffix, a capital, or an underscore marks the span as code.
func markdownMentionDistinctive(short string, marked bool) bool {
	if len(short) < 4 {
		return false
	}
	return marked || strings.ContainsAny(short, "_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func markdownMentionTargetKind(kind string) bool {
	switch kind {
	case "function", "method", "field", "property", "constant", "variable":
		return true
	}
	return typeLikeKind(kind)
}

func markdownLinkTarget(repoKey, docPath, target string, knownFiles map[string]bool, anchors func(string) map[string]string) (toID, targetKind string, ok bool) {
	if markdownLinkSchemeRe.MatchString(target) || strings.HasPrefix(target, "//") {
		return "", "", false
	}
	rawPath, anchor, _ := strings.Cut(target, "#")
	if unescaped, err := url.PathUnescape(rawPath); err == nil {
		rawPath = unescaped
	}
	file := docPath
	switch {
	case rawPath == "":
	case strings.HasPrefix(rawPath, "/"):
		file = path.Clean(strings.TrimPrefix(rawPath, "/"))
	default:
		file = path.Clean(path.Join(path.Dir(docPath), rawPath))
	}
	if !knownFiles[file] {
		return "", "", false
	}
	if anchor != "" {
		if unescaped, err := url.PathUnescape(anchor); err == nil {
			anchor = unescaped
		}
		if sectionID, found := anchors(file)[strings.ToLower(anchor)]; found {
			return sectionID, "symbol", true
		}
	}
	if file == docPath {
		return "", "", false
	}
	return fileID(repoKey, file), "file", true
}

// markdownSectionAnchors maps each section's GitHub anchor to its symbol ID.
// The anchor is recomputed from the heading text rather than taken from the
// symbol name: symbol names use slugName, whose output is part of the frozen
// compound-v1 identity and is not GitHub's anchor algorithm.
func markdownSectionAnchors(records []SymbolRecord) map[string]string {
	sections := make([]SymbolRecord, 0, len(records))
	for _, symbol := range records {
		if symbol.Kind == "section" {
			sections = append(sections, symbol)
		}
	}
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].StartLine < sections[j].StartLine })
	anchors := map[string]string{}
	uses := map[string]int{}
	for _, section := range sections {
		slug := githubAnchorSlug(strings.TrimPrefix(section.Signature, "markdown heading "))
		anchor := slug
		if count := uses[slug]; count > 0 {
			anchor = slug + "-" + strconv.Itoa(count)
		}
		uses[slug]++
		if _, taken := anchors[anchor]; !taken {
			anchors[anchor] = section.ID
		}
	}
	return anchors
}

// githubAnchorSlug is GitHub's heading anchor: lowercase, every character that
// is not a letter, mark, number, connector, space, or hyphen dropped, and each
// space turned into a hyphen (runs are not merged).
func githubAnchorSlug(heading string) string {
	var slug strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			slug.WriteByte('-')
		case r == '-' || unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.Pc):
			slug.WriteRune(r)
		}
	}
	return slug.String()
}

// markdownCodeSpans returns a line's inline code spans and the line with those
// spans blanked, so a link written inside backticks is not read as a link. A
// span closes on the next backtick run of the same length; an unmatched run is
// literal.
//
// The line is repository content and may be megabytes long, so the closing run
// is found through a per-width cursor over the runs rather than by rescanning
// the rest of the line from every opener: a line of unmatched runs of widths
// 1, 2, 3, ... would make the rescan quadratic. Each cursor only moves forward,
// so the whole line costs time linear in its length.
func markdownCodeSpans(line string) ([]string, string) {
	type backtickRun struct{ start, width int }
	var runs []backtickRun
	runsByWidth := map[int][]int{}
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		start := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		runsByWidth[i-start] = append(runsByWidth[i-start], len(runs))
		runs = append(runs, backtickRun{start: start, width: i - start})
	}
	var spans []string
	prose := []byte(line)
	cursor := map[int]int{}
	for index := 0; index < len(runs); {
		open := runs[index]
		sameWidth := runsByWidth[open.width]
		next := cursor[open.width]
		for next < len(sameWidth) && sameWidth[next] <= index {
			next++
		}
		cursor[open.width] = next
		if next == len(sameWidth) {
			index++
			continue
		}
		closing := runs[sameWidth[next]]
		spans = append(spans, strings.TrimSpace(line[open.start+open.width:closing.start]))
		for p := open.start; p < closing.start+closing.width; p++ {
			prose[p] = ' '
		}
		index = sameWidth[next] + 1
	}
	return spans, string(prose)
}

// markdownFenceLines marks every line that belongs to a code fence, including
// the opening and closing fence lines, with the same rules markdownEntities uses.
func markdownFenceLines(lines []string) []bool {
	inFence := make([]bool, len(lines))
	openFence := ""
	for index, line := range lines {
		if openFence != "" {
			inFence[index] = true
			if markdownFenceCloses(line, openFence) {
				openFence = ""
			}
			continue
		}
		if marker, _, ok := markdownFenceOpens(line); ok {
			openFence = marker
			inFence[index] = true
		}
	}
	return inFence
}
