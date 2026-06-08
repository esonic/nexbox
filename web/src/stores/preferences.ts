import { create } from "zustand";
import { persist } from "zustand/middleware";

export type ChatFontSize = "sm" | "md" | "lg" | "xl";

export const CHAT_FONT_SIZE_PX: Record<ChatFontSize, number> = {
  sm: 13,
  md: 15,
  lg: 17,
  xl: 19,
};

export const CHAT_FONT_SIZE_LABELS: Record<ChatFontSize, string> = {
  sm: "S",
  md: "M",
  lg: "L",
  xl: "XL",
};

interface PreferencesState {
  chatFontSize: ChatFontSize;
  setChatFontSize: (size: ChatFontSize) => void;
}

export const usePreferences = create<PreferencesState>()(
  persist(
    (set) => ({
      chatFontSize: "md",
      setChatFontSize: (size) => set({ chatFontSize: size }),
    }),
    { name: "nexbox-preferences" },
  ),
);
