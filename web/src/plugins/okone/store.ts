import { create } from "zustand";
import { okoneApi, type CreateChangeOrderResponse } from "./api";

interface ChangeOrderStore {
  loading: boolean;
  result: { success: boolean; data?: CreateChangeOrderResponse; error?: string } | null;
  execute: (projectId: string) => Promise<void>;
  clear: () => void;
}

export const useChangeOrderStore = create<ChangeOrderStore>((set, get) => ({
  loading: false,
  result: null,
  execute: async (projectId: string) => {
    if (get().loading) return;
    set({ loading: true, result: null });
    try {
      const data = await okoneApi.createChangeOrder(projectId);
      set({ result: { success: true, data }, loading: false });
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : "Unknown error";
      set({ result: { success: false, error: msg }, loading: false });
    }
  },
  clear: () => set({ result: null }),
}));
