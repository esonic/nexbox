import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import zh from "./zh.json";
import en from "./en.json";

const LANG_KEY = "boxnex_lang";

i18n.use(initReactI18next).init({
  resources: {
    zh: { translation: zh },
    en: { translation: en },
  },
  lng: localStorage.getItem(LANG_KEY) || "zh",
  fallbackLng: "zh",
  interpolation: {
    escapeValue: false,
  },
});

// Persist language changes to localStorage
i18n.on("languageChanged", (lng) => {
  localStorage.setItem(LANG_KEY, lng);
});

export default i18n;
