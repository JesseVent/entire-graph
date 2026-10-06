package sem

import (
	"fmt"
	"strings"
	"testing"
)

func TestParserIdentityRevisionsSnapshotAndCaches(t *testing.T) {
	header := leanHeader(sourceContext{}, "same-release", profileSpec{})
	if header.IdentityRevision != "4" {
		t.Fatalf("identity=%q", header.IdentityRevision)
	}
	if !strings.HasSuffix(searchSnapshotCacheVersion, "-"+header.IdentityRevision) || !strings.HasSuffix(providerRecordsCacheVersion, "-"+header.IdentityRevision) {
		t.Fatal("parser identity does not invalidate cached snapshots")
	}
}

func TestDefaultExportIdentityCorrectionIsRevisioned(t *testing.T) {
	entities := javascriptDefaultExportEntities("helper.js", "export default classifier => classifier()\n")
	symbols := entitySymbols("local/example", "helper.js", "JavaScript", entities)
	if len(symbols) != 1 || symbols[0].ID != "local/example:JavaScript:helper.js:function:helper" {
		t.Fatalf("corrected default export symbols = %+v", symbols)
	}
	if IdentityRevision == "js-ts-callable-scope-1" {
		t.Fatal("default export identity correction must invalidate the previous parser revision")
	}
}

// Issue #199: qualifying a Python nested callable by the enclosing CALLABLE
// re-keys every Python nested-callable symbol, so serving it from a cache
// written by the previous revision would keep publishing the phantom
// `C.helper`. The cache key carries IdentityRevision and nothing else in it
// changes (the cache is keyed on the git TREE of the source, which an edit to
// the parser does not touch), so the bump is the entire invalidation mechanism.
func TestPythonNestedCallableCorrectionIsRevisioned(t *testing.T) {
	entities, _, status := TreeSitterParser{}.ParseWithStatus("c.py",
		"class C:\n    def m(self):\n        def helper(v):\n            return v\n        return helper(1)\n")
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	symbols := entitySymbols("local/example", "c.py", "Python", entities)
	corrected := false
	for _, symbol := range symbols {
		if symbol.ID == "local/example:Python:c.py:function:C.m.helper" {
			corrected = true
		}
		if symbol.ID == "local/example:Python:c.py:method:C.helper" {
			t.Errorf("phantom class member still emitted: %s", symbol.ID)
		}
	}
	if !corrected {
		t.Fatalf("corrected Python nested callable missing; symbols = %s", symbolIDs(symbols))
	}
	if IdentityRevision == "2" {
		t.Fatal("Python nested-callable correction must invalidate the previous parser revision")
	}
}

// Fence tracking removes phantom sections (headings inside fences) and phantom
// closing fences, which renumbers later code_fence_N names: a re-key.
func TestMarkdownFenceCorrectionIsRevisioned(t *testing.T) {
	content := strings.Join([]string{
		"# Title",
		"```bash",
		"# not a heading",
		"```",
		"## Real",
		"~~~python",
		"## also not a heading",
		"```",
		"~~~",
		"```text``` is inline code, not a fence",
		"```",
		"# swallowed by the unclosed fence",
	}, "\n")
	var got []string
	for _, entity := range markdownEntities(content) {
		got = append(got, fmt.Sprintf("%s:%s@%d", entity.Kind, entity.Name, entity.StartLine))
	}
	want := []string{
		"section:Title@1",
		"code_fence:code_fence_1_bash@2",
		"section:Real@5",
		"code_fence:code_fence_2_python@6",
		"code_fence:code_fence_3_text@11",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("markdown entities =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if IdentityRevision == "3" {
		t.Fatal("Markdown fence correction must invalidate the previous parser revision")
	}
}
