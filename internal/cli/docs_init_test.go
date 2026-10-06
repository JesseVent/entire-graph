package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsInitAuditsAndAddsTheRunnerOnce(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Tests")
	git(t, repo, "config", "user.email", "tests@entire.local")
	write(t, repo, "app/app.go", "package app\n\nfunc Orders() int { return 1 }\n")
	write(t, repo, "app/old.go", "package app\n\nfunc Old() int { return 1 }\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	git(t, repo, "rm", "-q", "app/old.go")
	git(t, repo, "commit", "-m", "remove old.go")
	write(t, repo, "README.md", strings.Join([]string{
		"# Orders",
		"Call `Orders()`; see [the design](docs/design.md) and `app/old.go`.",
		"# Other",
		"Plain prose.",
	}, "\n")+"\n")

	cacheDir := t.TempDir()
	run := func() docsInitResponse {
		t.Helper()
		var out bytes.Buffer
		if err := Run(t.Context(), Options{Version: "0.1.0", Env: EntireEnv{RepoRoot: repo}, Stdout: &out},
			[]string{"docs", "init", "--repo", repo, "--cache-dir", cacheDir, "--format", "json"}); err != nil {
			t.Fatal(err)
		}
		var response docsInitResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", out.String(), err)
		}
		return response
	}

	// With no trail runners set up, the audit runs and nothing is written.
	got := run()
	var problems []string
	for _, problem := range got.Problems {
		problems = append(problems, problem.Kind+" "+problem.Target)
	}
	if strings.Join(problems, "; ") != "stale_path app/old.go; broken_link docs/design.md" ||
		got.Sections != 2 || got.SectionsNamingCode != 1 || got.Runner.Status != "no_runners" {
		t.Fatalf("first audit = %+v (problems %q)", got, problems)
	}
	runner := filepath.Join(repo, ".entire", "runners", "trail-docs.json")
	if _, err := os.Stat(runner); !os.IsNotExist(err) {
		t.Fatalf("runner written without a runners directory: %v", err)
	}

	// Once runners exist, the Doc staleness runner is added, and a second run
	// leaves it alone.
	if err := os.MkdirAll(filepath.Dir(runner), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := run(); got.Runner.Status != "written" {
		t.Fatalf("runner status = %q, want written", got.Runner.Status)
	}
	data, err := os.ReadFile(runner)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ID     string `json:"id"`
		Prompt struct {
			Template string `json:"template"`
		} `json:"prompt"`
		Output struct {
			TrailMonitor struct {
				Key string `json:"key"`
			} `json:"trail_monitor"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("runner is not valid JSON: %v\n%s", err, data)
	}
	if config.ID != "trail-docs" || config.Output.TrailMonitor.Key != "doc_staleness" ||
		!strings.Contains(config.Prompt.Template, "entire graph docs --repo . --base <base-ref> --format json") {
		t.Fatalf("runner config = %+v", config)
	}
	if err := os.WriteFile(runner, []byte("{\"id\":\"edited\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(); got.Runner.Status != "exists" {
		t.Fatalf("runner status on the second run = %q, want exists", got.Runner.Status)
	}
	if data, _ := os.ReadFile(runner); string(data) != "{\"id\":\"edited\"}\n" {
		t.Fatalf("an existing runner was replaced: %s", data)
	}
}
