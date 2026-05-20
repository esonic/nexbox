import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authApi } from "@/lib/api";

interface LoginPageProps {
  /** Called after a successful login so the app shell can flip its auth gate
   *  to "authed" without waiting for a re-mount + /auth/me round-trip. */
  onAuthed?: () => void;
}

export function LoginPage({ onAuthed }: LoginPageProps) {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (submitting || !username || !password) return;
    setError(null);
    setSubmitting(true);
    try {
      await authApi.login(username, password);
      onAuthed?.();
      const from = params.get("from");
      navigate(from && from.startsWith("/") && !from.startsWith("/login") ? from : "/", { replace: true });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(msg || "Login failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex h-screen h-dvh items-center justify-center bg-background px-4">
      <form
        onSubmit={handleSubmit}
        className="w-full max-w-sm rounded-lg border bg-card shadow-sm p-6 space-y-4"
      >
        <div className="flex flex-col items-center text-center gap-2">
          <img src="/favicon.svg" alt="Nexbox" className="w-10 h-10" aria-hidden="true" />
          <h1 className="text-lg font-semibold">Sign in to Nexbox</h1>
          <p className="text-xs text-muted-foreground">Enter your username and password to continue.</p>
        </div>

        <div className="space-y-1.5">
          <label htmlFor="login-username" className="text-xs text-muted-foreground">
            Username
          </label>
          <Input
            id="login-username"
            type="text"
            autoComplete="username"
            autoFocus
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            disabled={submitting}
            className="h-9 text-sm"
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor="login-password" className="text-xs text-muted-foreground">
            Password
          </label>
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
            className="h-9 text-sm"
          />
        </div>

        {error && (
          <p className="text-xs text-destructive bg-destructive/10 border border-destructive/30 rounded px-2 py-1.5">
            {error}
          </p>
        )}

        <Button
          type="submit"
          className="w-full"
          disabled={submitting || !username || !password}
        >
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />}
          Sign in
        </Button>
      </form>
    </div>
  );
}
