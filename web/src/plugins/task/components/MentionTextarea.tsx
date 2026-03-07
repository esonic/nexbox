import { useState, useEffect, useRef, useCallback } from "react";
import { createPortal } from "react-dom";
import { membersApi, type Member } from "@/lib/api";
import { UserAvatar } from "./UserPicker";

interface MentionTextareaProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  minRows?: number;
  onKeyDown?: (e: React.KeyboardEvent<HTMLTextAreaElement>) => void;
}

interface DropdownPos {
  top: number;
  left: number;
  width: number;
}

export function MentionTextarea({ value, onChange, placeholder, className, minRows = 3, onKeyDown: externalKeyDown }: MentionTextareaProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [atStart, setAtStart] = useState<number | null>(null);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Member[]>([]);
  const [loading, setLoading] = useState(false);
  const [selectedIdx, setSelectedIdx] = useState(0);
  const [menuPos, setMenuPos] = useState<DropdownPos | null>(null);
  const showMenu = atStart !== null && menuPos !== null;

  // Compute dropdown position from textarea bounding rect
  const updateMenuPos = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    setMenuPos({ top: rect.bottom + 4, left: rect.left, width: rect.width });
  }, []);

  // Close dropdown when clicking outside
  useEffect(() => {
    if (!showMenu) return;
    const handler = (e: MouseEvent) => {
      if (menuRef.current?.contains(e.target as Node)) return;
      if (textareaRef.current?.contains(e.target as Node)) return;
      setAtStart(null);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showMenu]);

  // Search members when query changes
  useEffect(() => {
    if (atStart === null) return;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(async () => {
      setLoading(true);
      try {
        const data = await membersApi.search(query);
        setResults(data || []);
        setSelectedIdx(0);
      } catch {
        setResults([]);
      } finally {
        setLoading(false);
      }
    }, 150);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  }, [query, atStart]);

  const shortName = (m: Member) =>
    m.user_name.includes("@") ? m.user_name.split("@")[0] : m.user_name;

  const insertMention = (member: Member) => {
    if (atStart === null) return;
    const name = shortName(member);
    const before = value.slice(0, atStart);
    const after = value.slice(textareaRef.current?.selectionStart ?? value.length);
    const newValue = before + "@" + name + " " + after;
    onChange(newValue);
    setAtStart(null);
    // Restore focus and move cursor after inserted mention
    setTimeout(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      const pos = before.length + name.length + 2; // "@" + name + " "
      el.setSelectionRange(pos, pos);
    }, 0);
  };

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const newVal = e.target.value;
    onChange(newVal);

    const cursor = e.target.selectionStart;
    const textBefore = newVal.slice(0, cursor);
    const atIdx = textBefore.lastIndexOf("@");

    if (atIdx !== -1) {
      const textAfterAt = textBefore.slice(atIdx + 1);
      // Valid mention: no space after @
      if (!/\s/.test(textAfterAt)) {
        if (atStart === null) updateMenuPos();
        setAtStart(atIdx);
        setQuery(textAfterAt);
        return;
      }
    }
    setAtStart(null);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (showMenu && results.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSelectedIdx((i) => (i + 1) % results.length);
        return;
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setSelectedIdx((i) => (i - 1 + results.length) % results.length);
        return;
      } else if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        insertMention(results[selectedIdx]);
        return;
      } else if (e.key === "Escape") {
        setAtStart(null);
        return;
      }
    }
    externalKeyDown?.(e);
  };

  const dropdown = showMenu ? (
    <div
      ref={menuRef}
      style={{ position: "fixed", top: menuPos!.top, left: menuPos!.left, width: menuPos!.width, zIndex: 9999 }}
      className="bg-popover border rounded-lg shadow-lg overflow-hidden max-h-52 overflow-y-auto"
    >
      {loading ? (
        <div className="px-3 py-2 text-xs text-muted-foreground">Searching…</div>
      ) : results.length === 0 ? (
        <div className="px-3 py-2 text-xs text-muted-foreground">
          {query ? "No members found" : "Type to search members…"}
        </div>
      ) : (
        results.map((m, i) => (
          <div
            key={m.user_name}
            onMouseDown={(e) => { e.preventDefault(); insertMention(m); }}
            onMouseEnter={() => setSelectedIdx(i)}
            className={`flex items-center gap-2 px-3 py-1.5 cursor-pointer text-sm ${i === selectedIdx ? "bg-accent" : "hover:bg-accent/50"}`}
          >
            <UserAvatar userName={shortName(m)} avatarUrl={m.avatar_url} size="sm" />
            <span>{shortName(m)}</span>
            {m.display_name && <span className="text-xs text-muted-foreground">{m.display_name}</span>}
          </div>
        ))
      )}
    </div>
  ) : null;

  return (
    <>
      <textarea
        ref={textareaRef}
        value={value}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        rows={minRows}
        className={className}
        spellCheck={false}
      />
      {dropdown && createPortal(dropdown, document.body)}
    </>
  );
}
