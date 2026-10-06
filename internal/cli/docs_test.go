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
	write(t, repo, "docs/design.md", "# Cache\nThe cache is keyed on the tree.\n# Other\nUnchanged.\n")
	write(t, repo, "docs/notes.md", "# Notes\nPlain prose.\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	// A second commit touching notes.go and notes.md together makes them a
	// co-change pair (the floor is two shared commits).
	write(t, repo, "app/notes.go", "package app\n\nfunc Notes() int { return 2 }\n")
	write(t, repo, "docs/notes.md", "# Notes\nPlain prose, revised.\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "notes")

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

	// Changing a mentioned function lists the section that names it; changing a
	// file lists the untouched doc that usually changes with it. A content change
	// to a file a section names by path (Unrelated names app/notes.go) does not.
	write(t, repo, "app/app.go", "package app\n\nfunc Orders() int { return 3 }\n\nfunc Totals() int { return 2 }\n")
	write(t, repo, "app/notes.go", "package app\n\nfunc Notes() int { return 3 }\n")
	_, got := run("json")
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
