import { useState } from "react";
import { authCloudApi, setToken } from "@/lib/api";

interface LoginModalProps {
  onSuccess: (token: string) => void;
}

export default function LoginModal({ onSuccess }: LoginModalProps) {
  const [tab, setTab]           = useState<"login" | "register">("login");
  const [email, setEmail]       = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode]         = useState("");
  const [codeSent, setCodeSent] = useState(false);
  const [codeCountdown, setCodeCountdown] = useState(0);
  const [error, setError]       = useState("");
  const [loading, setLoading]   = useState(false);

  function startCountdown() {
    setCodeCountdown(60);
    const t = setInterval(() => {
      setCodeCountdown(prev => {
        if (prev <= 1) { clearInterval(t); return 0; }
        return prev - 1;
      });
    }, 1000);
  }

  async function handleSendCode() {
    if (!email || !email.includes("@")) {
      setError("请先填写有效的邮箱地址");
      return;
    }
    setError("");
    setLoading(true);
    try {
      await authCloudApi.sendVerification(email);
      setCodeSent(true);
      startCountdown();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "发送失败");
    } finally {
      setLoading(false);
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      const res = tab === "login"
        ? await authCloudApi.login(email, password)
        : await authCloudApi.register(email, password, code);
      setToken(res.token);
      onSuccess(res.token);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "操作失败");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm">
      <div className="w-full max-w-sm rounded-xl border bg-card p-8 shadow-lg">
        <h1 className="mb-6 text-center text-2xl font-bold">BeeHub</h1>

        {/* Tab switcher */}
        <div className="mb-6 flex rounded-lg border p-1">
          {(["login", "register"] as const).map((t) => (
            <button
              key={t}
              type="button"
              onClick={() => { setTab(t); setError(""); setCodeSent(false); setCodeCountdown(0); }}
              className={`flex-1 rounded-md py-1.5 text-sm font-medium transition-colors ${
                tab === t ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t === "login" ? "登录" : "注册"}
            </button>
          ))}
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label className="mb-1 block text-sm font-medium">邮箱</label>
            <input
              type="email"
              required
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
              placeholder="you@example.com"
            />
          </div>

          {tab === "register" && (
            <div>
              <label className="mb-1 block text-sm font-medium">验证码</label>
              <div className="flex gap-2">
                <input
                  type="text"
                  required
                  maxLength={6}
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  className="flex-1 rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
                  placeholder="6位验证码"
                />
                <button
                  type="button"
                  onClick={handleSendCode}
                  disabled={loading || codeCountdown > 0}
                  className="whitespace-nowrap rounded-md border px-3 py-2 text-sm font-medium hover:bg-muted disabled:opacity-50"
                >
                  {codeCountdown > 0 ? `${codeCountdown}s` : codeSent ? "重新发送" : "发送验证码"}
                </button>
              </div>
              {codeSent && codeCountdown > 0 && (
                <p className="mt-1 text-xs text-muted-foreground">验证码已发送至 {email}，10 分钟内有效</p>
              )}
            </div>
          )}

          <div>
            <label className="mb-1 block text-sm font-medium">密码</label>
            <input
              type="password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
              placeholder={tab === "register" ? "至少 8 位" : "password"}
            />
          </div>

          {error && (
            <p className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>
          )}

          <button
            type="submit"
            disabled={loading}
            className="w-full rounded-md bg-primary py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
          >
            {loading ? "请稍候..." : tab === "login" ? "登录" : "注册"}
          </button>
        </form>
      </div>
    </div>
  );
}
