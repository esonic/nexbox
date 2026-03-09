import { useState } from "react";
import { Brain, ChevronRight, ChevronDown, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";

interface ThinkingBlockProps {
  content: string;
  isStreaming?: boolean;
  onLoadDetails?: () => Promise<boolean>;
}

function dedent(text: string): string {
  const trimmed = text.replace(/^\n+/, "").replace(/\s+$/, "");
  const lines = trimmed.split("\n");
  const indents = lines.filter((l) => l.trim().length > 0).map((l) => l.match(/^(\s*)/)?.[1].length ?? 0);
  const min = indents.length > 0 ? Math.min(...indents) : 0;
  if (min === 0) return trimmed;
  return lines.map((l) => l.slice(min)).join("\n");
}

export function ThinkingBlock({ content, isStreaming, onLoadDetails }: ThinkingBlockProps) {
  const [expanded, setExpanded] = useState(false);
  const [loading, setLoading] = useState(false);

  const handleOpenChange = async (open: boolean) => {
    if (open && !content && onLoadDetails) {
      setLoading(true);
      const ok = await onLoadDetails();
      setLoading(false);
      if (!ok) return; // keep collapsed on load failure
    }
    setExpanded(open);
  };

  return (
    <Collapsible open={expanded} onOpenChange={handleOpenChange} className="my-1">
      <CollapsibleTrigger
        className={cn(
          "flex items-center gap-1.5 w-full text-left py-0.5 text-xs",
          "hover:text-foreground transition-colors outline-none",
        )}
      >
        <Brain className={cn(
          "h-3.5 w-3.5 shrink-0 text-muted-foreground",
          isStreaming && "animate-pulse"
        )} />
        <span className={cn(
          "font-medium text-muted-foreground",
          isStreaming && "text-foreground"
        )}>
          {isStreaming ? "Thinking..." : "Thought"}
        </span>
        <span className="ml-auto shrink-0">
          {loading ? (
            <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />
          ) : expanded ? (
            <ChevronDown className="h-3 w-3 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3 w-3 text-muted-foreground" />
          )}
        </span>
      </CollapsibleTrigger>

      {/* 流式预览：未展开且正在流式时，显示内容片段 */}
      {isStreaming && !expanded && content && (
        <div className="ml-5 pl-3 border-l border-blue-500/30 mt-0.5 mb-0.5 max-h-[60px] overflow-hidden relative">
          <pre className="text-[11px] text-muted-foreground/70 whitespace-pre-wrap break-words leading-relaxed animate-pulse">
            {content.slice(-300)}
          </pre>
          {/* 底部渐变遮罩 */}
          <div className="absolute bottom-0 left-0 right-0 h-4 bg-gradient-to-t from-muted/50 to-transparent pointer-events-none" />
        </div>
      )}

      <CollapsibleContent>
        {content ? (
          <div className="ml-5 pl-3 border-l border-border/40 mt-0.5 mb-1">
            <pre className="text-xs text-muted-foreground whitespace-pre-wrap break-words max-h-[300px] overflow-y-auto leading-relaxed">
              {dedent(content)}
            </pre>
          </div>
        ) : expanded && !loading ? (
          <div className="ml-5 pl-3 border-l border-border/40 mt-0.5 mb-1">
            <span className="text-xs text-muted-foreground/60 italic">Details not available</span>
          </div>
        ) : null}
      </CollapsibleContent>
    </Collapsible>
  );
}
