import { useState, useEffect } from "react";
import { globalWS } from "@/lib/ws";
import { authApi } from "@/lib/api";

type Phase = "idle" | "waiting" | "error";

export function LoginModal() {
  const [phase, setPhase] = useState<Phase>("idle");
  const [loginURL, setLoginURL] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    return globalWS.subscribeGlobal((event) => {
      if (event.type === "auth:success") {
        window.location.reload();
      }
    });
  }, []);

  const handleLogin = async () => {
    setError(null);
    try {
      const data = await authApi.loginUrl();
      setLoginURL(data.url);
      setPhase("waiting");
      window.open(data.url, "nexbox-login", "width=600,height=700,popup=yes");
    } catch (e) {
      setError(String(e));
      setPhase("error");
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background">
      <div className="flex flex-col items-center gap-5 w-full max-w-md px-6">
        <div className="text-center">
          <h2 className="text-lg font-semibold">Sign in to BeeHub Cloud</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Log in with your OKG account to access shared resources.
          </p>
        </div>

        {phase === "idle" && (
          <button
            onClick={handleLogin}
            className="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
          >
            Sign in with OKG
          </button>
        )}

        {phase === "waiting" && (
          <div className="w-full space-y-4 text-center">
            <div className="flex items-center justify-center gap-2 text-sm text-muted-foreground">
              <svg className="h-4 w-4 animate-spin" viewBox="0 0 24 24" fill="none">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
              </svg>
              Waiting for login to complete…
            </div>
            <p className="text-xs text-muted-foreground/70">
              If the login window didn't appear,{" "}
              <a
                href={loginURL || "#"}
                target="_blank"
                rel="noopener noreferrer"
                className="underline hover:text-foreground transition-colors"
              >
                click here to open it manually
              </a>.
            </p>
            <button
              onClick={() => { setPhase("idle"); setLoginURL(null); }}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              ← Back
            </button>
          </div>
        )}

        {phase === "error" && (
          <div className="w-full space-y-3 text-center">
            <div className="rounded-md bg-destructive/10 px-4 py-3 text-sm text-destructive">
              {error}
            </div>
            <button
              onClick={() => { setPhase("idle"); setError(null); }}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              ← Try again
            </button>
          </div>
        )}

      </div>
    </div>
  );
}
