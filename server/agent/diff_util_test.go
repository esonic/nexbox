package agent

import (
	"strings"
	"testing"
)

func TestLooksLikeUnifiedDiff(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"git header", "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-a\n+b\n", true},
		{"diff --git header", "diff --git a/x b/x\nindex 1..2\n--- a/x\n+++ b/x\n", true},
		{"bare hunk, no header", "@@ -1,2 +1,2 @@\n-a\n+b\n", false},
		{"apply_patch envelope", "*** Begin Patch\n*** Update File: x\n@@\n-a\n+b\n*** End Patch\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeUnifiedDiff(c.in); got != c.want {
				t.Fatalf("looksLikeUnifiedDiff(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestUnifiedDiffForFullContent(t *testing.T) {
	add := unifiedDiffForFullContent("x.txt", "a\nb\n", false)
	if !strings.Contains(add, "--- /dev/null") || !strings.Contains(add, "+++ b/x.txt") {
		t.Fatalf("add diff missing headers: %q", add)
	}
	if !strings.Contains(add, "+a\n+b\n") {
		t.Fatalf("add diff missing content: %q", add)
	}
	if strings.Contains(add, "\n-a") {
		t.Fatalf("add diff should not contain removed lines: %q", add)
	}

	del := unifiedDiffForFullContent("x.txt", "a\nb\n", true)
	if !strings.Contains(del, "--- a/x.txt") || !strings.Contains(del, "+++ /dev/null") {
		t.Fatalf("delete diff missing headers: %q", del)
	}
	if !strings.Contains(del, "-a\n-b\n") {
		t.Fatalf("delete diff missing content: %q", del)
	}

	if got := unifiedDiffForFullContent("x.txt", "", false); got != "" {
		t.Fatalf("empty content should produce no diff, got %q", got)
	}
}

func TestNormalizeApplyPatchDiffUpdate(t *testing.T) {
	raw := "*** Begin Patch\n*** Update File: build.sh\n@@\n echo start\n-echo hi\n+echo hey\n echo end\n*** End Patch\n"
	got := normalizeApplyPatchDiff("build.sh", raw)
	if !strings.HasPrefix(got, "--- a/build.sh\n+++ b/build.sh\n") {
		t.Fatalf("missing git-style header: %q", got)
	}
	if !strings.Contains(got, "-echo hi\n") || !strings.Contains(got, "+echo hey\n") {
		t.Fatalf("missing hunk content: %q", got)
	}
	if !strings.Contains(got, "@@ -1,3 +1,3 @@") {
		t.Fatalf("unexpected hunk header: %q", got)
	}
}

func TestNormalizeApplyPatchDiffMultiHunk(t *testing.T) {
	raw := "*** Begin Patch\n*** Update File: f.go\n@@\n ctx1\n-old1\n+new1\n ctx2\n@@\n ctx3\n-old2\n+new2\n*** End Patch\n"
	got := normalizeApplyPatchDiff("f.go", raw)
	hunkCount := strings.Count(got, "@@ -")
	if hunkCount != 2 {
		t.Fatalf("expected 2 hunks, got %d: %q", hunkCount, got)
	}
	// Second hunk's line numbers should continue from the first, not restart at 1.
	if !strings.Contains(got, "@@ -4,2 +4,2 @@") {
		t.Fatalf("second hunk should continue numbering: %q", got)
	}
}

func TestNormalizeApplyPatchDiffAddFile(t *testing.T) {
	raw := "*** Begin Patch\n*** Add File: new.txt\n+line one\n+line two\n*** End Patch\n"
	got := normalizeApplyPatchDiff("new.txt", raw)
	if !strings.Contains(got, "--- /dev/null") {
		t.Fatalf("add file diff should have /dev/null old side: %q", got)
	}
	if !strings.Contains(got, "+line one\n+line two\n") {
		t.Fatalf("add file diff missing content: %q", got)
	}
}

func TestNormalizeApplyPatchDiffDeleteFile(t *testing.T) {
	raw := "*** Begin Patch\n*** Delete File: old.txt\n*** End Patch\n"
	// No body content available for the delete in this envelope shape;
	// should degrade to "" rather than crash or show a header-only diff.
	got := normalizeApplyPatchDiff("old.txt", raw)
	if got != "" {
		t.Fatalf("expected empty diff without content, got %q", got)
	}
}

func TestNormalizeApplyPatchDiffMoveIgnoredButUpdateKept(t *testing.T) {
	raw := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n@@\n-a\n+b\n*** End Patch\n"
	got := normalizeApplyPatchDiff("old.txt", raw)
	if !strings.Contains(got, "-a\n") || !strings.Contains(got, "+b\n") {
		t.Fatalf("expected hunk content preserved despite move marker: %q", got)
	}
}

func TestNormalizeApplyPatchDiffAlreadyUnified(t *testing.T) {
	raw := "--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-old\n+new\n"
	got := normalizeApplyPatchDiff("x", raw)
	if got != strings.TrimSpace(raw) {
		t.Fatalf("already-unified diff should pass through unchanged: got %q want %q", got, strings.TrimSpace(raw))
	}
}

func TestNormalizeApplyPatchDiffUnrecognized(t *testing.T) {
	if got := normalizeApplyPatchDiff("x", `{"some":"json"}`); got != "" {
		t.Fatalf("unrecognized text should not be surfaced as a diff, got %q", got)
	}
	if got := normalizeApplyPatchDiff("x", ""); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}

// TestLooksLikeUnifiedDiffAmbiguousContentInsideHunk is the adversarial case
// from review: a real diff that removes a line starting with "--" and adds
// one starting with "++" renders as two consecutive lines, "--- old" and
// "+++ new", inside a hunk — textually identical to a genuine file header
// pair. It must NOT be misdetected as a second/nested file header; the hunk
// tracking must keep it classified as del/add content.
func TestLooksLikeUnifiedDiffAmbiguousContentInsideHunk(t *testing.T) {
	// A single valid diff (has a real "--- "/"+++ " pair before the hunk) is
	// still a valid diff overall, even though it contains an ambiguous pair
	// inside the hunk body — looksLikeUnifiedDiff should say true because of
	// the real header, not because of the ambiguous one.
	diff := "--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n ctx1\n-- old\n++ new\n ctx2\n"
	if !looksLikeUnifiedDiff(diff) {
		t.Fatalf("expected true for a diff with a real header, got false: %q", diff)
	}

	// An apply_patch envelope containing the same ambiguous pair inside its
	// (bare-"@@", no numbers) hunk must be routed through the apply_patch
	// parser, not mistaken for an already-valid unified diff.
	raw := "*** Begin Patch\n*** Update File: x\n@@\n ctx1\n-- old\n++ new\n ctx2\n*** End Patch\n"
	got := normalizeApplyPatchDiff("x", raw)
	if !strings.Contains(got, "@@ -1,3 +1,3 @@") {
		t.Fatalf("expected a synthesized numbered hunk header, got %q", got)
	}
	if !strings.Contains(got, "-- old\n") || !strings.Contains(got, "++ new\n") {
		t.Fatalf("expected the ambiguous pair preserved as del/add content, got %q", got)
	}
}

// TestLooksLikeUnifiedDiffAmbiguousContentWithoutRealHeader is the sharper
// version of the case above: no genuine "--- "/"+++ " header exists
// anywhere — just a legitimate numbered hunk whose body happens to contain
// the ambiguous "--- old"/"+++ new" pair. Without hunk-budget tracking, a
// naive scanner would find that pair and wrongly conclude this is a valid
// unified diff (which it is not: there's no file header at all).
func TestLooksLikeUnifiedDiffAmbiguousContentWithoutRealHeader(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n ctx1\n-- old\n++ new\n ctx2\n"
	if looksLikeUnifiedDiff(diff) {
		t.Fatalf("expected false (no real file header anywhere), got true for %q", diff)
	}
}

// TestLooksLikeUnifiedDiffSkipsNoNewlineMarker specifically exercises the
// hunk-budget-tracking branch, not just the top-level true/false answer: a
// diff with a real header at the very start would return true immediately
// on line 1 regardless of whether the marker is handled correctly deeper
// inside the hunk, so that shape doesn't actually prove anything about the
// marker logic. This one has NO real header, so the only way it can come
// out false is if the no-newline marker did *not* wrongly consume hunk
// budget — if it had, the state machine would exit the hunk one line early
// and misdetect the later (still-inside-the-hunk) "--- old_amb"/
// "+++ new_amb" pair as a real file header, wrongly returning true.
func TestLooksLikeUnifiedDiffSkipsNoNewlineMarker(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n" +
		" ctx1\n" +
		"-old_x\n" +
		noNewlineMarker + "\n" +
		"+new_x\n" +
		"--- old_amb\n" +
		"+++ new_amb\n"
	if looksLikeUnifiedDiff(diff) {
		t.Fatalf("expected false, got true for %q", diff)
	}
}

// TestParseApplyPatchSectionsStopsAtEndPatch guards against a regression
// where "*** End Patch" was treated as a no-op continue: since the input
// typically ends with a trailing newline, strings.Split produces a final
// empty element *after* "*** End Patch" that would otherwise be appended to
// the last (Update) section's body as a spurious extra context line,
// inflating its hunk's line count beyond what its own "@@" header declared.
func TestParseApplyPatchSectionsStopsAtEndPatch(t *testing.T) {
	raw := "*** Begin Patch\n*** Update File: x\n@@\n-old\n+new\n*** End Patch\n"
	sections := parseApplyPatchSections(raw)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d: %+v", len(sections), sections)
	}
	// Body includes the "@@" line itself (unifiedDiffFromApplyPatchHunks
	// strips it later); the point of this assertion is that there is no
	// spurious 4th (empty) line leaking in from after "*** End Patch".
	if got := len(sections[0].body); got != 3 {
		t.Fatalf("expected exactly 3 body lines (@@, -old, +new), got %d: %+v", got, sections[0].body)
	}

	// Multi-file variant: nothing must leak past the last section either.
	raw = "*** Begin Patch\n*** Add File: a.txt\n+line\n*** Update File: b.txt\n@@\n-old\n+new\n*** End Patch\n"
	sections = parseApplyPatchSections(raw)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d: %+v", len(sections), sections)
	}
	if got := len(sections[1].body); got != 3 {
		t.Fatalf("expected exactly 3 body lines in the last section, got %d: %+v", got, sections[1].body)
	}
}

func TestNormalizeApplyPatchDiffPathMismatchDoesNotFallBack(t *testing.T) {
	raw := "*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+y\n*** End Patch\n"
	// path is known (non-empty) and doesn't match the only section in the
	// envelope — must not fall back to that section's diff, which would
	// attach the wrong file's change to this tool call's card.
	if got := normalizeApplyPatchDiff("b.txt", raw); got != "" {
		t.Fatalf("expected no diff on path mismatch, got %q", got)
	}
}

func TestUnifiedDiffForFullContentPreservesBlankAndWhitespaceOnlyContent(t *testing.T) {
	// A file whose entire content is a single blank line ("\n") is not the
	// same as "no content" — it must still produce a one-line diff.
	got := unifiedDiffForFullContent("x.txt", "\n", false)
	if got == "" {
		t.Fatalf("a single blank line should still produce a diff, got empty")
	}
	if !strings.Contains(got, "@@ -1,0 +1,1 @@") {
		t.Fatalf("expected a one-line add-only hunk, got %q", got)
	}

	// Leading/trailing whitespace-only content must be preserved verbatim,
	// not trimmed away.
	got = unifiedDiffForFullContent("x.txt", "   \n", false)
	if !strings.Contains(got, "+   \n") {
		t.Fatalf("expected whitespace-only line preserved, got %q", got)
	}
}

func TestUnifiedDiffForFullContentNoTrailingNewline(t *testing.T) {
	// Content without a trailing newline must annotate its last line with
	// the standard "\ No newline at end of file" marker, matching what
	// real git/diffy/GNU diff would emit for the same file — otherwise the
	// diff misrepresents the file as ending with a newline when it doesn't.
	got := unifiedDiffForFullContent("x.txt", "abc", false)
	if !strings.Contains(got, "+abc\n"+noNewlineMarker+"\n") {
		t.Fatalf("expected no-newline marker after the last line, got %q", got)
	}

	// Content that *does* end with a newline must NOT get the marker.
	got = unifiedDiffForFullContent("x.txt", "abc\n", false)
	if strings.Contains(got, noNewlineMarker) {
		t.Fatalf("did not expect a no-newline marker for a file ending in \\n, got %q", got)
	}

	// Multi-line content: the marker belongs only on the *last* line.
	got = unifiedDiffForFullContent("x.txt", "a\nb", false)
	if !strings.Contains(got, "+a\n+b\n"+noNewlineMarker+"\n") {
		t.Fatalf("expected marker only after the last line, got %q", got)
	}
	if strings.Count(got, noNewlineMarker) != 1 {
		t.Fatalf("expected exactly one marker, got %q", got)
	}

	// The generated diff must still parse correctly as a valid unified diff
	// (i.e. the marker mustn't throw off looksLikeUnifiedDiff's own hunk
	// tracking, which has to skip it too).
	if !looksLikeUnifiedDiff(unifiedDiffForFullContent("x.txt", "abc", false)) {
		t.Fatalf("expected the generated diff to itself look like a valid unified diff")
	}
}

func TestUnifiedDiffFromHunks(t *testing.T) {
	got := unifiedDiffFromHunks("x.go", []string{"@@ -1,2 +1,2 @@\n-a\n+b\n c\n"})
	if !strings.HasPrefix(got, "--- a/x.go\n+++ b/x.go\n") {
		t.Fatalf("missing header: %q", got)
	}
	if !strings.Contains(got, "-a\n+b\n c\n") {
		t.Fatalf("missing hunk body: %q", got)
	}
	if got := unifiedDiffFromHunks("x.go", []string{"", "  "}); got != "" {
		t.Fatalf("blank hunks should produce no diff, got %q", got)
	}
}
