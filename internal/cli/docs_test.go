package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestDocsListsSectionsThatPointAtWhatChanged(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "app/app.go", "package app\n\nfunc Orders() int { return 1 }\n\nfunc Totals() int { return 2 }\n")
	write(t, repo, "app/notes.go", "package app\n\nfunc Notes() int { return 1 }\n")
	write(t, repo, "README.md", strings.Join([]string{
		"# Orders",
		"Call `Orders()` for the list.",
		"# Totals",
		"`Totals()` sums them. See [the design](docs/design.md#cache).",
		"# Unrelated",
		"See `app/notes.go` for notes.",
	}, "\n")+"\n")
	write(t, repo, "docs/design.md", "# Cache\nThe cache is keyed on the tree.\n# Other\nDraft.\n")
	write(t, repo, "docs/notes.md", "# Notes\nPlain prose.\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	// A second commit touching notes.go and notes.md together makes them a
	// co-change pair (the floor is two shared commits). A third makes design.md
	// and notes.md a doc-to-doc pair.
	write(t, repo, "app/notes.go", "package app\n\nfunc Notes() int { return 2 }\n")
	write(t, repo, "docs/notes.md", "# Notes\nPlain prose, revised.\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "notes")
	design := "# Cache\nThe cache is keyed on the tree.\n# Other\nUnchanged.\n"
	write(t, repo, "docs/design.md", design)
	write(t, repo, "docs/notes.md", "# Notes\nPlain prose, revised twice.\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "docs")

	cacheDir := t.TempDir()
	run := func(format string) (string, docsResponse) {
		t.Helper()
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
			[]string{"docs", "--repo", repo, "--cache-dir", cacheDir, "--format", format}); err != nil {
			t.Fatal(err)
		}
		var response docsResponse
		if format == "json" {
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatalf("decode %s: %v", out.String(), err)
			}
		}
		return out.String(), response
	}
	render := func(response docsResponse) []string {
		var lines []string
		for _, item := range response.Items {
			heading := item.Heading
			if heading == "" {
				heading = "(file)"
			}
			var reasons []string
			for _, reason := range item.Reasons {
				reasons = append(reasons, reason.Kind+" "+reason.Target+" "+reason.Change)
			}
			sort.Strings(reasons)
			lines = append(lines, fmt.Sprintf("%s:%d %s | %s", item.Path, item.Line, heading, strings.Join(reasons, "; ")))
		}
		return lines
	}
	check := func(step string, got docsResponse, want []string, alreadyUpdated int) {
		t.Helper()
		if lines := render(got); strings.Join(lines, "\n") != strings.Join(want, "\n") || got.AlreadyUpdated != alreadyUpdated {
			t.Fatalf("%s: items =\n  %s\nalready_updated=%d\nwant\n  %s\nalready_updated=%d",
				step, strings.Join(lines, "\n  "), got.AlreadyUpdated, strings.Join(want, "\n  "), alreadyUpdated)
		}
	}

	if _, got := run("json"); got.ChangedFiles != 0 || len(got.Items) != 0 {
		t.Fatalf("clean tree: %+v", got)
	}

	// A doc-only edit reaches other docs through links and mentions, not
	// co-change: editing an unlinked section of design.md lists nothing, though
	// notes.md usually changes with it.
	write(t, repo, "docs/design.md", "# Cache\nThe cache is keyed on the tree.\n# Other\nEdited.\n")
	_, got := run("json")
	check("doc-only edit", got, nil, 0)
	write(t, repo, "docs/design.md", design)

	// Changing a mentioned function lists the section that names it; changing a
	// file lists the untouched doc that usually changes with it. A content change
	// to a file a section names by path (Unrelated names app/notes.go) does not.
	write(t, repo, "app/app.go", "package app\n\nfunc Orders() int { return 3 }\n\nfunc Totals() int { return 2 }\n")
	write(t, repo, "app/notes.go", "package app\n\nfunc Notes() int { return 3 }\n")
	_, got = run("json")
	check("code change", got, []string{
		"README.md:1 Orders | mentions Orders changed",
		"docs/notes.md:1 (file) | co_changes app/notes.go changed",
	}, 0)

	// Editing the Orders section counts it as updated. Editing the linked Cache
	// section lists the section that links to it, and removing Totals still lists
	// the section that named it, through the HEAD snapshot's mention. Removing
	// app/notes.go lists the section that names its path, and its co-change pair
	// survives through the HEAD snapshot.
	write(t, repo, "README.md", strings.Join([]string{
		"# Orders",
		"Call `Orders()` for the list; it now returns three.",
		"# Totals",
		"`Totals()` sums them. See [the design](docs/design.md#cache).",
		"# Unrelated",
		"See `app/notes.go` for notes.",
	}, "\n")+"\n")
	write(t, repo, "docs/design.md", "# Cache\nThe cache is keyed on the tree and the profile.\n# Other\nUnchanged.\n")
	write(t, repo, "app/app.go", "package app\n\nfunc Orders() int { return 3 }\n")
	if err := os.Remove(filepath.Join(repo, "app", "notes.go")); err != nil {
		t.Fatal(err)
	}
	_, got = run("json")
	check("doc and removal", got, []string{
		"README.md:3 Totals | links docs/design.md#Cache changed; mentions Totals removed",
		"README.md:5 Unrelated | mentions app/notes.go removed",
		"docs/notes.md:1 (file) | co_changes app/notes.go changed",
	}, 1)

	text, _ := run("text")
	for _, want := range []string{
		"README.md:3  Totals",
		"  - mentions `Totals` (removed)",
		"  - links to docs/design.md#Cache (changed)",
		"  - usually changes with app/notes.go",
		"1 doc sections that point at these changes were already edited in them.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestDocsSkipsCoChangeFromHubCodeFiles(t *testing.T) {
	t.Parallel()
	file := func(path, blob string) sem.FileRecord {
		language := "Go"
		if strings.HasSuffix(path, ".md") {
			language = "Markdown"
		}
		return sem.FileRecord{ID: "file:" + path, Path: path, Blob: blob, Language: language}
	}
	coChange := func(a, b string) sem.RelationRecord {
		return sem.RelationRecord{FromID: "file:" + a, ToID: "file:" + b, Type: "FILE_CHANGES_WITH"}
	}
	docs := []string{"a.md", "b.md", "c.md", "d.md"}
	var files []sem.FileRecord
	var relations []sem.RelationRecord
	for _, doc := range docs {
		files = append(files, file(doc, "1"))
		relations = append(relations, coChange("help.go", doc))
	}
	relations = append(relations, coChange("store.go", "a.md"))
	base := sem.ProviderSnapshot{Header: sem.SnapshotHeader{Commit: "c"}, Files: append(files, file("help.go", "1"), file("store.go", "1")), Relations: relations}
	head := sem.ProviderSnapshot{Files: append(files, file("help.go", "2"), file("store.go", "2")), Relations: relations}

	// help.go co-changes with four docs, so it is a hub and lists none of them;
	// store.go's single doc partner is still listed.
	got := buildDocsResponse(base, head, nil, nil, 50)
	if len(got.Items) != 1 || got.Items[0].Path != "a.md" || len(got.Items[0].Reasons) != 1 || got.Items[0].Reasons[0].Target != "store.go" {
		t.Fatalf("items = %+v, want only a.md co-changing with store.go", got.Items)
	}
}
