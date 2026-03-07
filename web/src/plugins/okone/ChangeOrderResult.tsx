import { Check, X, ExternalLink } from "lucide-react";
import { useChangeOrderStore } from "./store";

/** Registered to slot: git.results */
export function ChangeOrderResult(_props: { projectId: string }) {
  const { result, clear } = useChangeOrderStore();

  if (!result) return null;

  return (
    <div className={`rounded-lg border p-3 text-sm ${
      result.success ? "border-green-500/30" : "border-destructive/30"
    }`}>
      <div className="flex items-start gap-2">
        {result.success ? (
          <Check className="h-4 w-4 text-green-500 shrink-0 mt-0.5" />
        ) : (
          <X className="h-4 w-4 text-destructive shrink-0 mt-0.5" />
        )}
        <div className="flex-1 min-w-0">
          {result.success && result.data ? (
            <>
              <div className="font-medium text-green-600 dark:text-green-400 text-xs">Change Order Created</div>
              <a
                href={result.data.order_url}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 text-xs text-blue-500 hover:underline mt-0.5"
              >
                #{result.data.order_id}
                <ExternalLink className="h-3 w-3 shrink-0" />
              </a>
            </>
          ) : (
            <>
              <div className="font-medium text-destructive text-xs">Change Order Failed</div>
              <div className="text-xs text-muted-foreground mt-0.5 break-all">{result.error}</div>
            </>
          )}
        </div>
        <button onClick={clear} className="text-muted-foreground hover:text-foreground shrink-0">
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  );
}
