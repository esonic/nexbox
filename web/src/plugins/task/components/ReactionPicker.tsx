import { useState, useEffect, useRef } from "react";
import { toast } from "sonner";
import { Smile } from "lucide-react";
import { reactionsApi } from "@/lib/api";
import type { ReactionSummary } from "@/types";
import { cn } from "@/lib/utils";

interface ReactionPickerProps {
  targetType: string;
  targetId: string;
}

const COMMON_EMOJIS = ["👍", "👎", "😄", "🎉", "😕", "❤️", "🚀", "👀"];

export function ReactionPicker({ targetType, targetId }: ReactionPickerProps) {
  const [summary, setSummary] = useState<ReactionSummary[]>([]);
  const [showPicker, setShowPicker] = useState(false);
  const [loading, setLoading] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    loadReactions();
  }, [targetType, targetId]);

  useEffect(() => {
    if (!showPicker) return;
    const handler = (e: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) {
        setShowPicker(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showPicker]);

  const loadReactions = async () => {
    try {
      const data = await reactionsApi.get(targetType, targetId);
      setSummary(data || []);
    } catch (e) {
      console.error("Failed to load reactions:", e);
    }
  };

  const handleToggle = async (emoji: string) => {
    if (loading) return;
    setLoading(true);
    try {
      const existing = summary.find((r) => r.emoji === emoji);
      if (existing?.user_reacted) {
        await reactionsApi.remove(targetType, targetId, emoji);
      } else {
        await reactionsApi.add(targetType, targetId, emoji);
      }
      await loadReactions();
    } catch (e) {
      console.error("Failed to toggle reaction:", e);
      toast.error("Failed to toggle reaction", { description: String(e) });
    } finally {
      setLoading(false);
      setShowPicker(false);
    }
  };

  return (
    <div className="relative flex items-center gap-1 flex-wrap">
      {summary.map((reaction) => (
        <button
          key={reaction.emoji}
          onClick={() => handleToggle(reaction.emoji)}
          className={cn(
            "inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-xs border transition-colors",
            reaction.user_reacted
              ? "bg-primary/10 border-primary/30 hover:bg-primary/20"
              : "bg-muted/50 border-transparent hover:bg-muted"
          )}
          disabled={loading}
        >
          <span>{reaction.emoji}</span>
          <span className="font-medium tabular-nums">{reaction.count}</span>
        </button>
      ))}

      <div className="relative" ref={pickerRef}>
        <button
          onClick={() => setShowPicker(!showPicker)}
          className="p-1 hover:bg-muted rounded text-muted-foreground hover:text-foreground transition-colors"
          title="Add reaction"
        >
          <Smile className="h-3.5 w-3.5" />
        </button>

        {showPicker && (
          <div className="absolute bottom-full left-0 mb-2 bg-popover border rounded-lg shadow-lg p-2 z-50 flex gap-1">
            {COMMON_EMOJIS.map((emoji) => (
              <button
                key={emoji}
                onClick={() => handleToggle(emoji)}
                className="p-1 hover:bg-muted rounded text-base transition-colors"
                disabled={loading}
              >
                {emoji}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
