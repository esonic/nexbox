# 1code Repository Analysis

## Summary of Key Findings

### 1. Chat Message Storage (`message-store.ts`)

**How messages are stored:**
- **Structured blocks approach**: Messages are stored with a `parts` array, not as a single string
- Each message has:
  - `id`: string
  - `role`: "user" | "assistant" | "system"
  - `parts`: `MessagePart[]` - array of structured parts
  - `metadata`: optional metadata object

**MessagePart structure:**
```typescript
interface MessagePart {
  type: string           // e.g., "text", "tool-invocation", "tool-Compact"
  text?: string          // For text parts
  toolCallId?: string    // For tool calls
  state?: string         // Tool state (e.g., "output-available", "result")
  input?: any            // Tool input
  output?: any           // Tool output
  result?: any           // Tool result
}
```

**Key optimizations:**
- Uses Jotai `atomFamily` for per-message atoms (independent updates)
- Separate atoms for text parts (`textPartAtomFamily`) to prevent re-renders
- Message structure atoms (`messageStructureAtomFamily`) for rendering without text content
- Caching strategies to prevent unnecessary re-renders during streaming

**Storage approach:**
- Messages are stored in memory via Jotai atoms
- Each message has its own atom: `messageAtomFamily(messageId)`
- Message IDs tracked in `messageIdsAtom` array
- Roles cached in `messageRolesAtom` Map for fast lookups
- Per-subChat isolation using `currentSubChatIdAtom`

### 2. Diff Parsing (`diff-parser.ts`)

**How diffs are parsed:**
- Server-side parsing in main process (moved from renderer to avoid blocking UI)
- Parses unified diff format into structured `ParsedDiffFile[]`

**ParsedDiffFile structure:**
```typescript
interface ParsedDiffFile {
  key: string              // Unique key: `${oldPath}->${newPath}`
  oldPath: string         // Original file path
  newPath: string         // New file path
  diffText: string        // Raw diff text for this file
  isBinary: boolean       // Whether file is binary
  additions: number       // Line addition count
  deletions: number       // Line deletion count
  isValid: boolean        // Whether diff format is valid
  fileLang: string | null // Language for syntax highlighting
  isNewFile: boolean      // Whether file is new
  isDeletedFile: boolean  // Whether file is deleted
}
```

**Parsing logic:**
- `splitUnifiedDiffByFile()` splits unified diff into per-file blocks
- Validates diff hunks with `validateDiffHunk()`
- Extracts file paths from `---` and `+++` lines
- Counts additions/deletions by parsing diff lines
- Maps file extensions to language identifiers for syntax highlighting

**File change tracking:**
- Files tracked by their diff key: `${oldPath}->${newPath}`
- Special handling for new files (`/dev/null->{path}`) and deleted files (`{path}->/dev/null`)
- Changes stored in `changes-store.ts` using Zustand
- Selected files tracked per worktree: `selectedFiles: Record<worktreePath, SelectedFileState>`

### 3. Diff Viewer Components (`changes-view.tsx` & `agent-diff-view.tsx`)

**Changes View (`changes-view.tsx`):**
- Main file list component showing staged/unstaged/untracked files
- Supports filtering by text and subchat
- File selection for diff viewing
- Commit selection with checkboxes
- Multi-select for batch operations (Shift+Click)

**Agent Diff View (`agent-diff-view.tsx`):**
- Renders actual diff content using `@pierre/diffs` library
- Supports unified and split view modes
- File-by-file rendering with collapse/expand
- "Viewed" checkbox system (GitHub-style review tracking)
- File operations: open in editor, reveal in finder, copy paths
- Expand to full file content functionality

**File change display:**
- Files grouped by category: staged, unstaged, untracked, against-base
- Each file shows: path, status indicator, addition/deletion counts
- Click to view diff, double-click to open in pinned view
- Context menu for file operations

### 4. Review Button Flow

**Based on `changes-view.tsx` analysis:**

The "Review" button appears to be part of the subchat filtering system:

1. **Subchat Filter Component** (`changes-file-filter.tsx`):
   - Provides a dropdown to filter files by subchat
   - Shows subchat name and file count
   - When a subchat is selected, filters the file list to show only files changed in that subchat

2. **Review Flow** (inferred from code structure):
   - When Review button is clicked (likely in a message or tool output):
     - Sets `initialSubChatFilter` prop on `ChangesView`
     - This filters the file list to show only files from that specific subchat
     - User can then review the filtered changes
     - The filter persists when diff sidebar opens

3. **Integration points:**
   - `ChangesView` accepts `initialSubChatFilter?: string | null`
   - `onSubChatFilterChange` callback updates the filter
   - Filter state managed in `ChangesView` component state
   - Filtered files used for commit selection

**Review button implementation** (likely in message UI):
- Button appears in assistant message or tool output
- On click, opens diff sidebar with subchat filter applied
- Filter shows only files changed in that specific subchat conversation
- User can review, mark as viewed, commit, or discard changes

## Full File Contents

### 1. Message Store (`message-store.ts`)
[See attached file - 1000+ lines showing complete Jotai atom-based message storage]

### 2. Diff Parser (`diff-parser.ts`)
```typescript
// Key exports:
- splitUnifiedDiffByFile(diffText: string): ParsedDiffFile[]
- validateDiffHunk(diffText: string): { valid: boolean; reason?: string }
- getFileLang(filePath: string): string | null
```

### 3. Changes Store (`changes-store.ts`)
```typescript
// Key state:
- selectedFiles: Record<worktreePath, SelectedFileState>
- viewMode: DiffViewMode ("side-by-side" | "unified")
- fileListViewMode: "grouped" | "tree"
- expandedSections: Record<ChangeCategory, boolean>
- baseBranch: string | null
- showRenderedMarkdown: Record<worktreePath, boolean>
```

### 4. Changes View (`changes-view.tsx`)
[See attached file - 35KB showing complete file list and diff integration]

### 5. Chat Markdown Renderer (`chat-markdown-renderer.tsx`)
- Uses `Streamdown` library for markdown parsing
- Splits content into blocks for memoization
- Each block has stable key based on content hash
- Supports code highlighting with Shiki
- Handles streaming content with `isStreaming` prop

## Key Insights

1. **Message Storage**: Uses structured parts array, not single string - enables granular updates during streaming
2. **Diff Parsing**: Server-side parsing prevents UI blocking on large diffs
3. **File Tracking**: Uses `${oldPath}->${newPath}` keys for stable file identification
4. **Review Flow**: Subchat filtering allows reviewing changes from specific conversation threads
5. **Performance**: Extensive memoization and atom-based state management for optimal re-render behavior
