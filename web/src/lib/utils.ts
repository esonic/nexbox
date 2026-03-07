import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** crypto.randomUUID() polyfill — works in non-secure contexts (plain HTTP) */
export function uuid(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  // fallback using crypto.getRandomValues (available in all modern browsers)
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant 1
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function formatDate(dateStr: string): string {
  const d = new Date(dateStr);
  return d.toLocaleDateString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function formatRelativeDate(dateStr: string): string {
  const now = new Date();
  const d = new Date(dateStr);
  const diffMs = now.getTime() - d.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffSec < 60) return "now";
  if (diffMin < 60) return `${diffMin}m`;
  if (diffHour < 24) return `${diffHour}h`;
  if (diffDay < 30) return `${diffDay}d`;
  const diffMon = Math.floor(diffDay / 30);
  if (diffMon < 12) return `${diffMon}mo`;
  return `${Math.floor(diffMon / 12)}y`;
}

export function assetTypeLabel(type: string): string {
  const labels: Record<string, string> = {
    requirement_doc: "Requirement",
    design_doc: "Design Doc",
    code_change: "Code Change",
    unit_test: "Unit Test",
    test_case: "Test Case",
  };
  return labels[type] || type;
}

export function statusColor(status: string): string {
  switch (status) {
    case "in_progress":
      return "text-blue-500";
    case "reviewing":
      return "text-yellow-500";
    case "completed":
    case "approved":
      return "text-green-500";
    case "draft":
      return "text-gray-500";
    default:
      return "text-gray-400";
  }
}
