import { useState } from "react";
import { useTranslation } from "react-i18next";
import { authCloudApi, setToken } from "@/lib/api";
import { Languages } from "lucide-react";

interface LoginModalProps {
  onSuccess: (token: string) => void;
}

export default function LoginModal({ onSuccess }: LoginModalProps) {
  const { t, i18n } = useTranslation();
  const [tab, setTab]           = useState<"login" | "register">("login");
  const [email, setEmail]       = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode]         = useState("");
  const [codeSent, setCodeSent] = useState(false);
  const [codeCountdown, setCodeCountdown] = useState(0);
  const [error, setError]       = useState("");
  const [loading, setLoading]   = useState(false);

  const toggleLang = () => {
    i18n.changeLanguage(i18n.language === "zh" ? "en" : "zh");
  };

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
      setError(t("login.error_invalid_email"));
      return;
    }
    setError("");
    setLoading(true);
    try {
      await authCloudApi.sendVerification(email);
      setCodeSent(true);
      startCountdown();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t("login.error_send_failed"));
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
      setError(err instanceof Error ? err.message : t("login.error_operation_failed"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm">
      {/* Language toggle — top-right */}
      <button
        onClick={toggleLang}
        className="absolute top-4 right-4 flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
      >
        <Languages className="h-4 w-4" />
        {i18n.language === "zh" ? "EN" : "中文"}
      </button>

      <div className="w-full max-w-sm rounded-xl border bg-card p-8 shadow-lg">
        <h1 className="mb-6 text-center text-2xl font-bold">{t("login.title")}</h1>

        {/* Tab switcher */}
        <div className="mb-6 flex rounded-lg border p-1">
          {(["login", "register"] as const).map((tabKey) => (
            <button
              key={tabKey}
              type="button"
              onClick={() => { setTab(tabKey); setError(""); setCodeSent(false); setCodeCountdown(0); }}
              className={`flex-1 rounded-md py-1.5 text-sm font-medium transition-colors ${
                tab === tabKey ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t(`login.tab_${tabKey}`)}
            </button>
          ))}
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label className="mb-1 block text-sm font-medium">{t("login.email")}</label>
            <input
              type="email"
              required
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
              placeholder={t("login.email_placeholder")}
            />
          </div>

          {tab === "register" && (
            <div>
              <label className="mb-1 block text-sm font-medium">{t("login.verification_code")}</label>
              <div className="flex gap-2">
                <input
                  type="text"
                  required
                  maxLength={6}
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                  className="flex-1 rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
                  placeholder={t("login.code_placeholder")}
                />
                <button
                  type="button"
                  onClick={handleSendCode}
                  disabled={loading || codeCountdown > 0}
                  className="whitespace-nowrap rounded-md border px-3 py-2 text-sm font-medium hover:bg-muted disabled:opacity-50"
                >
                  {codeCountdown > 0 ? `${codeCountdown}s` : codeSent ? t("login.resend_code") : t("login.send_code")}
                </button>
              </div>
              {codeSent && codeCountdown > 0 && (
                <p className="mt-1 text-xs text-muted-foreground">{t("login.code_sent", { email })}</p>
              )}
            </div>
          )}

          <div>
            <label className="mb-1 block text-sm font-medium">{t("login.password")}</label>
            <input
              type="password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
              placeholder={tab === "register" ? t("login.password_placeholder_register") : t("login.password_placeholder_login")}
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
            {loading ? t("login.loading") : t(`login.submit_${tab}`)}
          </button>
        </form>
      </div>
    </div>
  );
}
