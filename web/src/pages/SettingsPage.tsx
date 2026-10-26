import { GeneralSettings } from "./settings/GeneralSettings";
import { PromptTemplatesSettings } from "./settings/PromptTemplatesSettings";

export function SettingsPage() {
  return (
    <div className="flex h-full">
      <div className="flex-1 overflow-y-auto">
        <div className="max-w-2xl mx-auto px-6 py-8">
          <h2 className="text-lg font-semibold mb-6">Settings</h2>
          <GeneralSettings />
          <PromptTemplatesSettings />
        </div>
      </div>
    </div>
  );
}
