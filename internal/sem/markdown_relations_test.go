package sem

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMarkdownRelationsMentionAndLinkTheirTargets(t *testing.T) {
	const repoKey = "local/example"
	contents := map[string]string{
		"README.md": strings.Join([]string{
			"Intro names `thing/thing.go:3` before any heading.",
			"",
			"# Guide",
			"",
			"Call `SearchThing()` or `thing.Helper`. `Process` is defined twice, `Run` is short,",
			"`query` is a word, and `Missing` names nothing.",
			"",
			"See [the guide](docs/guide.md#some-heading), [details](#details), [code](thing/thing.go),",
			"[site](https://example.com), ![logo](thing/thing.go) and `[not a link](docs/guide.md)`.",
			"",
			"```go",
			"`SearchThing` inside a fence is code, and so is [this](docs/guide.md).",
			"```",
			"",
			"## Details",
			"",
			"[A missing file](docs/nope.md) and [the guide again](./docs/guide.md).",
		}, "\n"),
		"docs/guide.md":  "# Some Heading\n\nBack to [the readme](../README.md#guide).\n",
		"thing/thing.go": "package thing\n\nfunc SearchThing() {}\n\nfunc Helper() {}\n\nfunc Process() {}\n\nfunc Run() {}\n",
		"other/other.go": "package other\n\nfunc Process() {}\n",
	}
	markdownRecords := map[string][]SymbolRecord{}
	symbolsByShortName := map[string][]SymbolRecord{}
	knownFiles := map[string]bool{}
	for path, content := range contents {
		knownFiles[path] = true
		var symbols []SymbolRecord
		if strings.HasSuffix(path, ".md") {
			symbols = entitySymbols(repoKey, path, "Markdown", markdownEntities(content))
			markdownRecords[path] = symbols
		} else {
			entities, _, status := TreeSitterParser{}.ParseWithStatus(path, content)
			if status.ParseError {
				t.Fatalf("parse %s: %s", path, status.Detail)
			}
			symbols = entitySymbols(repoKey, path, "Go", entities)
		}
		for _, symbol := range symbols {
			symbolsByShortName[symbol.Name] = append(symbolsByShortName[symbol.Name], symbol)
		}
	}
	readContent := func(path string) (string, bool) {
		content, ok := contents[path]
		return content, ok
	}

	var got []string
	for _, relation := range markdownRelations(repoKey, markdownRecords, symbolsByShortName, knownFiles, readContent, resolveProfile(ProfileFull)) {
		got = append(got, strings.TrimPrefix(relation.Type, "X-entire-graph:")+" "+
			strings.TrimPrefix(relation.FromID, repoKey+":")+" -> "+
			strings.TrimPrefix(relation.ToID, repoKey+":")+" "+relation.Resolution)
	}
	sort.Strings(got)
	want := []string{
		"LINKS_TO Markdown:README.md:section:Details -> file:docs/guide.md exact",
		"LINKS_TO Markdown:README.md:section:Guide -> Markdown:README.md:section:Details exact",
		"LINKS_TO Markdown:README.md:section:Guide -> Markdown:docs/guide.md:section:Some-Heading exact",
		"LINKS_TO Markdown:README.md:section:Guide -> file:thing/thing.go exact",
		"LINKS_TO Markdown:docs/guide.md:section:Some-Heading -> Markdown:README.md:section:Guide exact",
		"MENTIONS Markdown:README.md:section:Guide -> Go:thing/thing.go:function:Helper name_only",
		"MENTIONS Markdown:README.md:section:Guide -> Go:thing/thing.go:function:SearchThing name_only",
		"MENTIONS file:README.md -> file:thing/thing.go exact",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("markdown relations =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestGitHubAnchorSlug(t *testing.T) {
	for heading, want := range map[string]string{
		"What this gives you": "what-this-gives-you",
		"Foo & Bar":           "foo--bar",
		"🔍 query — *find the code for a task*": "-query--find-the-code-for-a-task",
		"`entire graph` v1.2_beta":             "entire-graph-v12_beta",
	} {
		if got := githubAnchorSlug(heading); got != want {
			t.Errorf("githubAnchorSlug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestMarkdownCodeSpans(t *testing.T) {
	spans, prose := markdownCodeSpans("`a` then ``b`c`` then ``` x ``` and `unclosed [l](p)")
	if got := strings.Join(spans, "|"); got != "a|b`c|x" {
		t.Errorf("spans = %q, want %q", got, "a|b`c|x")
	}
	// Each span, delimiters included, is blanked in place (3, 7 and 9 bytes),
	// between the single spaces that already surround it.
	if want := "    then" + strings.Repeat(" ", 9) + "then" + strings.Repeat(" ", 11) + "and `unclosed [l](p)"; prose != want {
		t.Errorf("prose = %q, want %q", prose, want)
	}
}

// A line of unmatched backtick runs of widths 1, 2, 3, ... is the worst case
// for a scanner that searches the rest of the line from every opener. The line
// is repository content, so it must cost linear time, not quadratic. On this
// 8 MB line the linear scan takes ~10ms; the quadratic one took ~6s, so the 2s
// deadline separates them with room for a slow or race-instrumented runner.
func TestMarkdownCodeSpansUnmatchedRunsAreLinear(t *testing.T) {
	var line strings.Builder
	for width := 1; width <= 4000; width++ {
		line.WriteString(strings.Repeat("`", width))
		line.WriteByte('x')
	}
	done := make(chan []string, 1)
	go func() {
		spans, _ := markdownCodeSpans(line.String())
		done <- spans
	}()
	select {
	case spans := <-done:
		if len(spans) != 0 {
			t.Fatalf("unmatched runs produced %d spans", len(spans))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("markdownCodeSpans on an %d-byte line of unmatched runs did not finish in 2s", line.Len())
	}
}
