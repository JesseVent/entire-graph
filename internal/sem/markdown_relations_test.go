package sem

import (
	"sort"
	"strings"
	"testing"
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
