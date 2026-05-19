/**
 * Shared task configuration: status/priority definitions, icons, helpers.
 * Used by TasksPage, IssueRow, and any other component that displays tasks.
 */
import type { Task } from "@/types";

// ── Types ──

export type TaskStatus = Task["status"];
export type TaskPriority = Task["priority"];

export interface CfgItem<V> {
  value: V;
  label: string;
  color: string;
}

// ── Status definitions ──

export const STATUS_LIST: CfgItem<TaskStatus>[] = [
  { value: "open",   label: "Open",   color: "text-blue-500" },
  { value: "closed", label: "Closed", color: "text-[#8b95a2]" },
];

export const PRIORITY_LIST: CfgItem<TaskPriority>[] = [
  { value: "low",      label: "Low",      color: "text-[#8b95a2]" },
  { value: "medium",   label: "Medium",   color: "text-blue-500" },
  { value: "high",     label: "High",     color: "text-[#f2994a]" },
  { value: "critical", label: "Critical", color: "text-[#f2555a]" },
];

export const STATUS_MAP  = new Map(STATUS_LIST.map((s) => [s.value, s]));
export const PRIORITY_MAP = new Map(PRIORITY_LIST.map((p) => [p.value, p]));
export const STATUS_ORDER: TaskStatus[] = STATUS_LIST.map((s) => s.value);
export const PRIORITY_ORDER: TaskPriority[] = PRIORITY_LIST.map((p) => p.value);

// ── Normalizers ──

export function normalizeStatus(s: string): TaskStatus {
  return STATUS_MAP.has(s as TaskStatus) ? (s as TaskStatus) : "open";
}

export function normalizePriority(p: string): TaskPriority {
  return PRIORITY_MAP.has(p as TaskPriority) ? (p as TaskPriority) : "medium";
}

// ── Helpers ──

export function shortId(id: string): string {
  if (id.startsWith("gl:")) {
    const iid = id.slice(id.lastIndexOf(":") + 1);
    return `#${iid}`;
  }
  return id.slice(0, 6).toUpperCase();
}
