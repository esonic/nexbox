import { useState, useEffect, useRef } from "react";
import { membersApi, type Member } from "@/lib/api";
import { cn } from "@/lib/utils";
import { X, Loader2, Pencil } from "lucide-react";

// Module-level cache: short name (e.g. "leo.xue") → full Member object
const memberCache = new Map<string, Member>();

// Fetch and cache member by short name
async function fetchMember(shortName: string): Promise<Member | undefined> {
  if (memberCache.has(shortName)) return memberCache.get(shortName);
  const data = await membersApi.search(shortName);
  const match = (data || []).find((m) => {
    const s = m.user_name.includes("@") ? m.user_name.split("@")[0] : m.user_name;
    return s === shortName;
  });
  if (match) memberCache.set(shortName, match);
  return match;
}

// ── UserAvatar ──────────────────────────────────────────────────────────────

interface UserAvatarProps {
  userName: string;
  avatarUrl?: string;
  size?: "xs" | "sm" | "md";
  className?: string;
  onClick?: (e: React.MouseEvent) => void;
}

export function UserAvatar({ userName, avatarUrl, size = "sm", className, onClick }: UserAvatarProps) {
  const [imgError, setImgError] = useState(false);
  const sizeClass = size === "xs" ? "w-4 h-4 text-[9px]" : size === "sm" ? "w-5 h-5 text-[10px]" : "w-6 h-6 text-xs";
  const initial = userName ? userName.charAt(0).toUpperCase() : "?";
  const clickProps = onClick ? { onClick, role: "button" as const } : {};

  if (avatarUrl && !imgError) {
    return (
      <img
        src={avatarUrl}
        alt={userName}
        className={cn("rounded-full object-cover shrink-0", onClick && "cursor-pointer", sizeClass, className)}
        onError={() => setImgError(true)}
        {...clickProps}
      />
    );
  }

  return (
    <span
      className={cn(
        "rounded-full bg-amber-500/20 text-amber-600 dark:text-amber-400 flex items-center justify-center font-medium shrink-0",
        onClick && "cursor-pointer",
        sizeClass,
        className
      )}
      {...clickProps}
    >
      {initial}
    </span>
  );
}

// ── UserCard (popup) ────────────────────────────────────────────────────────

function UserCard({ member, onClose }: { member: Member; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const shortName = member.user_name.includes("@") ? member.user_name.split("@")[0] : member.user_name;

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [onClose]);

  return (
    <div
      ref={ref}
      className="absolute left-0 top-full mt-1.5 z-50 bg-popover border rounded-xl shadow-lg p-4 w-52 flex flex-col items-center gap-2"
    >
      {member.avatar_url ? (
        <img
          src={member.avatar_url}
          alt={shortName}
          className="w-14 h-14 rounded-full object-cover"
        />
      ) : (
        <span className="w-14 h-14 rounded-full bg-amber-500/20 text-amber-600 dark:text-amber-400 flex items-center justify-center text-2xl font-semibold">
          {shortName.charAt(0).toUpperCase()}
        </span>
      )}
      {member.display_name && (
        <div className="text-sm font-semibold text-center leading-tight">{member.display_name}</div>
      )}
      <div className="text-xs text-muted-foreground text-center break-all">{member.user_name}</div>
    </div>
  );
}

// ── UserChip (compact, for list rows) ──────────────────────────────────────

interface UserChipProps {
  userName: string;
  className?: string;
}

export function UserChip({ userName, className }: UserChipProps) {
  const [member, setMember] = useState<Member | undefined>(memberCache.get(userName));

  useEffect(() => {
    if (!userName) return;
    fetchMember(userName).then((m) => { if (m) setMember(m); }).catch(() => {});
  }, [userName]);

  if (!userName) return null;

  return (
    <div className={cn("shrink-0 flex items-center gap-0.5 text-[10px]", className)}>
      <UserAvatar userName={userName} avatarUrl={member?.avatar_url} size="xs" />
      <span className="max-w-[60px] truncate">{userName}</span>
    </div>
  );
}

// ── UserDisplay (read-only) ─────────────────────────────────────────────────

interface UserDisplayProps {
  userName: string;
  className?: string;
}

export function UserDisplay({ userName, className }: UserDisplayProps) {
  const [member, setMember] = useState<Member | undefined>(memberCache.get(userName));
  const [showCard, setShowCard] = useState(false);

  useEffect(() => {
    if (!userName) return;
    fetchMember(userName).then((m) => { if (m) setMember(m); }).catch(() => {});
  }, [userName]);

  if (!userName) return null;

  return (
    <div className={cn("relative inline-flex", className)}>
      <span className="flex items-center gap-1.5 text-sm">
        <UserAvatar
          userName={userName}
          avatarUrl={member?.avatar_url}
          size="sm"
          onClick={(e) => { e.stopPropagation(); setShowCard((v) => !v); }}
        />
        <span className="text-foreground">{userName}</span>
      </span>
      {showCard && member && (
        <UserCard member={member} onClose={() => setShowCard(false)} />
      )}
    </div>
  );
}

// ── UserPicker ──────────────────────────────────────────────────────────────

interface UserPickerProps {
  value?: string;
  onChange: (userName: string) => void;
  placeholder?: string;
  className?: string;
}

export function UserPicker({ value, onChange, placeholder = "Search members…", className }: UserPickerProps) {
  const [editing, setEditing] = useState(false);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Member[]>([]);
  const [loading, setLoading] = useState(false);
  const [member, setMember] = useState<Member | undefined>(value ? memberCache.get(value) : undefined);
  const [showCard, setShowCard] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Fetch member info for existing value
  useEffect(() => {
    if (!value) { setMember(undefined); return; }
    fetchMember(value).then((m) => { if (m) setMember(m); }).catch(() => {});
  }, [value]);

  // Close picker/card when clicking outside
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (!containerRef.current?.contains(e.target as Node)) {
        setEditing(false);
        setShowCard(false);
        setQuery("");
        setResults([]);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, []);

  const openEdit = () => {
    setShowCard(false);
    setEditing(true);
    setQuery("");
    setResults([]);
    setTimeout(() => inputRef.current?.focus(), 50);
  };

  const handleInputChange = (q: string) => {
    setQuery(q);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (!q.trim()) { setResults([]); return; }
    debounceRef.current = setTimeout(async () => {
      setLoading(true);
      try {
        const data = await membersApi.search(q);
        setResults(data || []);
      } catch {
        setResults([]);
      } finally {
        setLoading(false);
      }
    }, 300);
  };

  const handleSelect = (m: Member) => {
    const name = m.user_name.includes("@") ? m.user_name.split("@")[0] : m.user_name;
    memberCache.set(name, m);
    onChange(name);
    setEditing(false);
    setQuery("");
    setResults([]);
  };

  const handleClear = (e: React.MouseEvent) => {
    e.stopPropagation();
    onChange("");
    setEditing(false);
    setShowCard(false);
    setQuery("");
    setResults([]);
  };

  return (
    <div ref={containerRef} className={cn("relative", className)}>
      {editing ? (
        <div className="relative">
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => handleInputChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") { setEditing(false); setQuery(""); setResults([]); }
            }}
            placeholder={placeholder}
            className="text-sm bg-muted/50 border rounded px-2 py-0.5 outline-none focus:ring-1 focus:ring-ring w-48 pr-6"
          />
          {loading && (
            <Loader2 className="absolute right-2 top-1/2 -translate-y-1/2 h-3 w-3 animate-spin text-muted-foreground" />
          )}
          {results.length > 0 && (
            <div className="absolute left-0 top-full mt-1 z-50 bg-popover border rounded-md shadow-md w-56 max-h-[240px] overflow-y-auto">
              {results.map((m) => (
                <button
                  key={m.user_name}
                  onClick={() => handleSelect(m)}
                  className="w-full flex items-center gap-2.5 px-3 py-2 hover:bg-accent text-left transition-colors"
                >
                  <UserAvatar userName={m.user_name} avatarUrl={m.avatar_url} size="sm" />
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium truncate">{m.display_name || m.user_name}</div>
                    {m.display_name && (
                      <div className="text-xs text-muted-foreground truncate">{m.user_name}</div>
                    )}
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>
      ) : value ? (
        <button
          onClick={openEdit}
          className="group flex items-center gap-1.5 text-sm hover:text-primary/80 transition-colors"
        >
          <UserAvatar
            userName={value}
            avatarUrl={member?.avatar_url}
            size="sm"
            onClick={(e) => { e.stopPropagation(); setShowCard((v) => !v); }}
          />
          <span className="text-foreground">{value}</span>
          <Pencil className="h-3 w-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
          <span
            role="button"
            onClick={handleClear}
            className="ml-0.5 p-0.5 rounded hover:bg-accent text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity"
          >
            <X className="h-3 w-3" />
          </span>
        </button>
      ) : (
        <button
          onClick={openEdit}
          className="group flex items-center gap-1.5 text-sm hover:text-primary/80 transition-colors"
        >
          <span className="text-muted-foreground/50">Unassigned</span>
          <Pencil className="h-3 w-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
        </button>
      )}

      {showCard && member && (
        <UserCard member={member} onClose={() => setShowCard(false)} />
      )}
    </div>
  );
}
