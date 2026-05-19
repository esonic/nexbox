const API_BASE = "/api";

export interface CreateChangeOrderResponse {
  order_id: number;
  order_url: string;
}

export const okoneApi = {
  createChangeOrder: (projectId: string) =>
    fetch(`${API_BASE}/okone/${projectId}/change-order`, { method: "POST" })
      .then(async (res) => {
        if (!res.ok) {
          const err = await res.json().catch(() => ({ error: res.statusText }));
          throw new Error(err.error || res.statusText);
        }
        return res.json() as Promise<CreateChangeOrderResponse>;
      }),
};
