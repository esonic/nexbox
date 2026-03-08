import { useTranslation } from "react-i18next";
import { GeneralSettings } from "./settings/GeneralSettings";

export function SettingsPage() {
  const { t } = useTranslation();
  return (
    <div className="flex h-full">
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-2xl mx-auto px-6 py-8">
          <h2 className="text-lg font-semibold mb-6">{t("settings.title")}</h2>
          <GeneralSettings />
        </div>
      </div>
    </div>
  );
}
