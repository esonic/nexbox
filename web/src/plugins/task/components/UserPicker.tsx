import { useState } from "react";
import { cn } from "@/lib/utils";
import { X, Pencil } from "lucide-react";

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

// ── UserChip (compact, for list rows) ──────────────────────────────────────

interface UserChipProps {
  userName: string;
  className?: string;
}

export function UserChip({ userName, className }: UserChipProps) {
  if (!userName) return null;
  return (
    <div className={cn("shrink-0 flex items-center gap-0.5 text-[10px]", className)}>
      <UserAvatar userName={userName} size="xs" />
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
  if (!userName) return null;
  return (
    <div className={cn("relative inline-flex", className)}>
      <span className="flex items-center gap-1.5 text-sm">
        <UserAvatar userName={userName} size="sm" />
        <span className="text-foreground">{userName}</span>
      </span>
    </div>
  );
}

// ── UserPicker ──────────────────────────────────────────────────────────────
//
// The original implementation hit the namespace members API to offer fuzzy
// search across the team. That API was removed with cloud mode, so this is
// now a simple free-text editor: type a username, press Enter, done.

interface UserPickerProps {
  value?: string;
  onChange: (userName: string) => void;
  placeholder?: string;
  className?: string;
}

export function UserPicker({ value, onChange, placeholder = "Username", className }: UserPickerProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");

  const openEdit = () => {
    setDraft(value ?? "");
    setEditing(true);
  };

  const commit = () => {
    onChange(draft.trim());
    setEditing(false);
  };

  const handleClear = (e: React.MouseEvent) => {
    e.stopPropagation();
    onChange("");
    setEditing(false);
    setDraft("");
  };

  return (
    <div className={cn("relative", className)}>
      {editing ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) { e.preventDefault(); commit(); }
            else if (e.key === "Escape") { setEditing(false); setDraft(value ?? ""); }
          }}
          placeholder={placeholder}
          className="text-sm bg-muted/50 border rounded px-2 py-0.5 outline-none focus:ring-1 focus:ring-ring w-48"
        />
      ) : value ? (
        <button
          onClick={openEdit}
          className="group flex items-center gap-1.5 text-sm hover:text-primary/80 transition-colors"
        >
          <UserAvatar userName={value} size="sm" />
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
    </div>
  );
}
