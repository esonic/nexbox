import { useTranslation } from "react-i18next";
import { ChevronDown } from "lucide-react";

const LANG_OPTIONS = [
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
];

export function GeneralSettings() {
  const { t, i18n } = useTranslation();

  return (
    <section className="space-y-4">
      {/* Language */}
      <div className="rounded-lg border bg-card">
        <div className="flex items-center justify-between px-4 py-3">
          <div>
            <div className="text-sm font-medium">{t("settings.language")}</div>
            <div className="text-xs text-muted-foreground mt-0.5">{t("settings.language_desc")}</div>
          </div>
          <div className="relative">
            <select
              value={i18n.language}
              onChange={(e) => i18n.changeLanguage(e.target.value)}
              className="appearance-none bg-background border rounded-md px-3 py-1.5 pr-8 text-sm font-medium cursor-pointer hover:bg-accent/50 transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1"
            >
              {LANG_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground pointer-events-none" />
          </div>
        </div>
      </div>
    </section>
  );
}
