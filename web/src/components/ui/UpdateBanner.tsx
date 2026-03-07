import { useState, useEffect } from "react";
import { globalWS } from "@/lib/ws";
import { updateApi } from "@/lib/api";
import { X, RefreshCw, ArrowUpCircle } from "lucide-react";

/**
 * UpdateBanner listens for `update:available` WebSocket events and shows a
 * bottom-right toast notification (VS Code style). The user can restart
 * immediately or dismiss.
 *
 * "Restart now" → POST /api/update/restart → polls /api/update/status until
 * the server comes back online, then reloads the page.
 *
 * "Later" → the binary on disk is already the new version; the next manual
 * restart will pick it up automatically.
 */
export function UpdateBanner() {
  const [newVersion, setNewVersion] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);

  useEffect(() => {
    const unsub = globalWS.subscribeGlobal((event) => {
      if (event.type === "update:available" && typeof event.version === "string") {
        setNewVersion(event.version as string);
      }
    });
    return unsub;
  }, []);

  if (!newVersion) return null;

  async function handleRestartNow() {
    setRestarting(true);
    try {
      await updateApi.restart();
    } catch {
      // Server may close the connection mid-response; that's expected.
    }
    await waitForServer();
    window.location.reload();
  }

  function handleLater() {
    setNewVersion(null);
  }

  return (
    <div className="fixed bottom-4 right-4 z-50 w-80 animate-in slide-in-from-bottom-2 fade-in duration-300">
      <div className="rounded-lg border border-zinc-700 bg-zinc-800 shadow-2xl shadow-black/40">
        <div className="flex items-center justify-between border-b border-zinc-700 px-3 py-2">
          <div className="flex items-center gap-2 text-xs font-medium text-zinc-300">
            <ArrowUpCircle className="h-3.5 w-3.5 text-blue-400" />
            Update Available
          </div>
          {!restarting && (
            <button
              onClick={handleLater}
              className="rounded p-0.5 text-zinc-500 hover:bg-zinc-700 hover:text-zinc-300 transition-colors"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>

        <div className="px-3 py-3">
          {restarting ? (
            <div className="flex items-center gap-2 text-sm text-zinc-300">
              <RefreshCw className="h-3.5 w-3.5 animate-spin" />
              Restarting, please wait…
            </div>
          ) : (
            <>
              <p className="text-sm text-zinc-300">
                A new version is available. Restart to apply the update.
              </p>
              <div className="mt-3 flex items-center gap-2">
                <button
                  onClick={handleRestartNow}
                  className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white hover:bg-blue-500 transition-colors"
                >
                  Restart Now
                </button>
                <button
                  onClick={handleLater}
                  className="rounded-md px-3 py-1 text-xs text-zinc-400 hover:bg-zinc-700 hover:text-zinc-300 transition-colors"
                >
                  Later
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

/** Polls /api/update/status with exponential backoff until the server responds. */
async function waitForServer(maxWaitMs = 60_000): Promise<void> {
  const start = Date.now();
  let delay = 500;
  while (Date.now() - start < maxWaitMs) {
    await sleep(delay);
    try {
      await updateApi.status();
      return; // server is back
    } catch {
      delay = Math.min(delay * 1.5, 3000);
    }
  }
}

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
