package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file centralizes unified-diff (git-style ---/+++/@@) production and
// normalization so every agent backend (pi, codex, claude) can hand the
// frontend the same shape for a single-file change: a standard unified diff
// string that web/src/lib/diff-parser.ts / DiffPreview.tsx can parse just
// like a git diff.

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// looksLikeUnifiedDiff reports whether raw already has a real git-style
// unified diff file header ("--- a/..." immediately followed by
// "+++ b/..."), so callers can pass it straight through unmodified.
//
// This walks the text as a small state machine rather than pattern-matching
// each line in isolation: a removed/added *content* line can itself look
// exactly like a "--- "/"+++ " header pair (e.g. changing "-- old" to
// "++ new" renders as the two consecutive diff lines "--- old"/"+++ new"),
// so a header is only recognized outside any hunk — tracked via each
// hunk's own "@@ -a,b +c,d @@" declared old/new line counts, which is the
// only structurally reliable signal available. It also intentionally does
// NOT match on a bare "@@" alone: codex's apply_patch envelope also uses
// "@@" as a hunk/context marker but has no file header at all, so a
// "@@"-only heuristic would wrongly accept it and skip the header-wrapping
// normalizeApplyPatchDiff() needs to do below.
func looksLikeUnifiedDiff(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "diff --git") {
		return true
	}
	lines := strings.Split(trimmed, "\n")
	oldRemaining, newRemaining := 0, 0
	inHunk := func() bool { return oldRemaining > 0 || newRemaining > 0 }
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !inHunk() {
			if strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
				return true
			}
			if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
				oldRemaining, newRemaining = hunkCounts(m)
			}
			continue
		}
		if line == noNewlineMarker {
			// Annotates the *previous* line's missing trailing newline; it is
			// not itself a diff line and must not consume any of the hunk's
			// declared old/new line budget.
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			newRemaining--
		case strings.HasPrefix(line, "-"):
			oldRemaining--
		default:
			oldRemaining--
			newRemaining--
		}
	}
	return false
}

// noNewlineMarker is the unified-diff convention for "the preceding line has
// no trailing newline in the file" (git, diffy, GNU diff all emit this
// verbatim). It must be recognized and skipped — without being counted
// against a hunk's declared line totals — by every state machine in this
// file that walks hunk bodies.
const noNewlineMarker = `\ No newline at end of file`

// hunkCounts parses the optional old/new line-count groups from a
// "@@ -a,b +c,d @@" match (b/d default to 1 when omitted, matching the
// unified diff spec for single-line hunks).
func hunkCounts(m []string) (oldCount, newCount int) {
	oldCount, newCount = 1, 1
	if m[2] != "" {
		if n, err := strconv.Atoi(m[2]); err == nil {
			oldCount = n
		}
	}
	if m[4] != "" {
		if n, err := strconv.Atoi(m[4]); err == nil {
			newCount = n
		}
	}
	return oldCount, newCount
}

// unifiedDiffFromHunks assembles a standard unified diff from pre-rendered
// hunk bodies (each already starting with "@@ ... @@\n" and containing
// +/-/space-prefixed lines), as delivered whole by Claude Code's
// structuredPatch or Codex's native FileUpdateChange.diff.
func unifiedDiffFromHunks(path string, hunks []string) string {
	nonEmpty := hunks[:0:0]
	for _, h := range hunks {
		if strings.TrimSpace(h) != "" {
			nonEmpty = append(nonEmpty, h)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	for _, h := range nonEmpty {
		if !strings.HasSuffix(h, "\n") {
			h += "\n"
		}
		out.WriteString(h)
	}
	return out.String()
}

// unifiedDiffForFullContent wraps a file's full content (no "before" to
// diff against) as an add- or delete-only unified diff: every line
// prefixed "+" (add) or "-" (delete), with /dev/null on the missing side.
// Used for Add/Delete file changes, where there is nothing to hunk-diff.
func unifiedDiffForFullContent(path, content string, isDelete bool) string {
	// Distinguish "no content at all" (nothing to diff) from "content is a
	// single blank line" (content == "\n", a real one-line file) — checking
	// emptiness *before* trimming the trailing newline, rather than after,
	// keeps the latter from being mistaken for the former and silently
	// dropped.
	if content == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	sign := "+"
	oldHeader, newHeader := "/dev/null", "b/"+path
	oldCount, newCount := 0, len(lines)
	if isDelete {
		sign = "-"
		oldHeader, newHeader = "a/"+path, "/dev/null"
		oldCount, newCount = len(lines), 0
	}
	var body strings.Builder
	for i, l := range lines {
		body.WriteString(sign)
		body.WriteString(l)
		body.WriteString("\n")
		if i == len(lines)-1 && !strings.HasSuffix(content, "\n") {
			// Preserve "this file has no trailing newline" fidelity, matching
			// what real git/diffy/GNU diff output for the same file would
			// look like — otherwise every full-content add/delete diff looks
			// like the file ends with a newline even when it doesn't.
			body.WriteString(noNewlineMarker)
			body.WriteString("\n")
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -1,%d +1,%d @@\n%s", oldHeader, newHeader, oldCount, newCount, body.String())
	return out.String()
}

// normalizeApplyPatchDiff converts codex's compat-mode apply_patch textual
// envelope:
//
//	*** Begin Patch
//	*** Update File: path/to/file
//	 unchanged context line
//	-removed line
//	+added line
//	*** End Patch
//
// (also *** Add File / *** Delete File / *** Move to) into a standard
// unified diff with git-style headers, restricted to the single file at
// `path` (the tool call this diff is attached to is already one file).
// Codex's envelope carries no old/new starting line numbers, so hunk
// headers use a running count seeded at line 1 rather than the file's real
// offsets — sufficient for a visual diff preview, not for `git apply`.
// If raw already looks like a standard unified diff, it is returned
// unchanged. If nothing recognizable is found, "" is returned rather than
// surfacing raw protocol text as if it were a diff.
func normalizeApplyPatchDiff(path, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Check for the apply_patch envelope *before* looksLikeUnifiedDiff: the
	// envelope's own hunks use bare "@@" (no "-a,b +c,d" counts), so
	// looksLikeUnifiedDiff's hunk-tracking state machine can't recognize
	// them as hunks, which means content inside an envelope that happens to
	// contain a "--- x"/"+++ y" pair (e.g. a diff of lines that themselves
	// start with "--"/"++") would otherwise be misdetected as a real file
	// header. "*** Begin Patch" is unambiguous enough to check first and
	// unconditionally route to the apply_patch parser.
	if strings.Contains(raw, "*** Begin Patch") {
		section, sectionPath := applyPatchSectionForPath(raw, path)
		if section == nil {
			return ""
		}
		return applyPatchSectionDiff(sectionPath, *section)
	}
	if looksLikeUnifiedDiff(raw) {
		return raw
	}
	// Doesn't look like a patch of any recognized shape (e.g. it's the raw
	// begin/end event JSON with no diff field at all) — don't surface it as
	// if it were one.
	return ""
}

// applyPatchSectionDiff renders one applyPatchSection as a standard unified
// diff, shared by normalizeApplyPatchDiff's single-file path and
// codexPatchToolStart's multi-file path (see codex_payload_helpers.go).
func applyPatchSectionDiff(path string, section applyPatchSection) string {
	switch section.kind {
	case "add":
		return unifiedDiffForFullContent(path, strings.Join(section.body, "\n"), false)
	case "delete":
		return unifiedDiffForFullContent(path, strings.Join(section.body, "\n"), true)
	case "update":
		return unifiedDiffFromApplyPatchHunks(path, section.body)
	default:
		return ""
	}
}

type applyPatchSection struct {
	kind string // "add", "delete", "update"
	path string
	body []string // lines following the "*** ... File:" header, up to the next "*** " marker
}

// applyPatchSectionForPath splits a (possibly multi-file) apply_patch
// envelope into per-file sections and returns the one matching `path` (or,
// if path is empty/unmatched and there is exactly one file section, that
// one — the tool call this is attached to is always single-file, so an
// empty `path` means the caller couldn't resolve it from elsewhere).
func applyPatchSectionForPath(raw, path string) (*applyPatchSection, string) {
	sections := parseApplyPatchSections(raw)
	if len(sections) == 0 {
		return nil, ""
	}
	for i := range sections {
		if sections[i].path == path {
			return &sections[i], sections[i].path
		}
	}
	// Only fall back to "the one file section" when the caller couldn't
	// resolve a path at all (path == ""). If path is known and simply
	// doesn't match any section, that's a real mismatch — falling back
	// anyway would risk attaching some *other* file's diff to this one.
	if path == "" && len(sections) == 1 {
		return &sections[0], sections[0].path
	}
	return nil, ""
}

// parseApplyPatchSections splits a (possibly multi-file) apply_patch
// envelope into one section per "*** Add/Delete/Update File:" block, in
// envelope order. Used both by applyPatchSectionForPath (single-file lookup,
// for the common case) and by callers that need every file a compat-mode
// patch touched (multi-file patches — see codexPatchToolStart).
func parseApplyPatchSections(raw string) []applyPatchSection {
	lines := strings.Split(raw, "\n")
	var sections []applyPatchSection
	var cur *applyPatchSection
	flush := func() {
		if cur != nil {
			sections = append(sections, *cur)
			cur = nil
		}
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "*** Begin Patch"):
			continue
		case strings.HasPrefix(line, "*** End Patch"):
			// Close out whatever section is open and stop — anything after
			// this (typically just a trailing newline from strings.Split, but
			// potentially stray trailer text) must not be appended to the
			// last section's body as a spurious extra context line.
			flush()
			return sections
		case strings.HasPrefix(line, "*** Add File:"):
			flush()
			cur = &applyPatchSection{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))}
		case strings.HasPrefix(line, "*** Delete File:"):
			flush()
			cur = &applyPatchSection{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))}
		case strings.HasPrefix(line, "*** Update File:"):
			flush()
			cur = &applyPatchSection{kind: "update", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))}
		case strings.HasPrefix(line, "*** Move to:"):
			// Renames aren't represented in our single-file diff shape;
			// keep the section's original path and drop the move marker.
			continue
		default:
			if cur != nil {
				// Add-file bodies use a leading "+"; strip it back to plain
				// content so unifiedDiffForFullContent can re-add it.
				if cur.kind == "add" {
					cur.body = append(cur.body, strings.TrimPrefix(line, "+"))
				} else {
					cur.body = append(cur.body, line)
				}
			}
		}
	}
	flush()
	return sections
}

// unifiedDiffFromApplyPatchHunks converts an apply_patch Update section's
// body (one or more "@@ ..." hunk markers followed by " "/"+"/"-" lines,
// possibly with unrelated free-text context after "@@") into standard
// unified-diff hunks with synthesized (non-authoritative) line numbers, and
// wraps them with a git-style file header.
func unifiedDiffFromApplyPatchHunks(path string, lines []string) string {
	type hunkLine struct {
		sign byte // ' ', '+', '-'
		text string
	}
	var hunks [][]hunkLine
	var cur []hunkLine
	flush := func() {
		if len(cur) > 0 {
			hunks = append(hunks, cur)
			cur = nil
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			flush()
			continue
		}
		if strings.HasPrefix(line, "*** ") {
			// Apply_patch protocol markers that can appear inside an Update
			// section's body (e.g. "*** End of File") — not file content, so
			// they must not be rendered as a context line.
			continue
		}
		if line == "" {
			cur = append(cur, hunkLine{sign: ' ', text: ""})
			continue
		}
		switch line[0] {
		case '+', '-', ' ':
			cur = append(cur, hunkLine{sign: line[0], text: line[1:]})
		default:
			// A line with no recognized diff-line prefix at all is unexpected
			// in apply_patch's grammar (every real content line starts with
			// " "/"+"/"-"); rather than guessing it's context (and risking a
			// count mismatch against the hunk's declared size), drop it.
		}
	}
	flush()
	if len(hunks) == 0 {
		return ""
	}

	oldLine, newLine := 1, 1
	var body strings.Builder
	for _, hunk := range hunks {
		oldCount, newCount := 0, 0
		var hunkBody strings.Builder
		for _, hl := range hunk {
			switch hl.sign {
			case '+':
				newCount++
			case '-':
				oldCount++
			default:
				oldCount++
				newCount++
			}
			hunkBody.WriteByte(hl.sign)
			hunkBody.WriteString(hl.text)
			hunkBody.WriteString("\n")
		}
		fmt.Fprintf(&body, "@@ -%d,%d +%d,%d @@\n%s", oldLine, oldCount, newLine, newCount, hunkBody.String())
		oldLine += oldCount
		newLine += newCount
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n%s", path, path, body.String())
	return out.String()
}
