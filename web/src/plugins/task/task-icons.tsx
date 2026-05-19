/**
 * SVG icon components for task statuses and priorities.
 */

// ── Status icons ──

export const StatusOpen = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.5" />
  </svg>
);

export const StatusInProgress = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.5" />
    <path d="M7 1a6 6 0 0 1 0 12z" fill="currentColor" />
  </svg>
);

export const StatusDone = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <circle cx="7" cy="7" r="7" fill="currentColor" />
    <path d="M4.5 7.5L6.25 9.25L9.75 5" stroke="white" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

export const StatusClosed = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.5" />
    <path d="M4 7h6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
  </svg>
);

// ── Priority icons ──

export const PriorityLow = ({ className }: { className?: string }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" className={className} fill="currentColor">
    <rect x="1.5" y="8" width="3" height="6" rx="1" />
    <rect x="6.5" y="5" width="3" height="9" rx="1" opacity="0.2" />
    <rect x="11.5" y="2" width="3" height="12" rx="1" opacity="0.2" />
  </svg>
);

export const PriorityMedium = ({ className }: { className?: string }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" className={className} fill="currentColor">
    <rect x="1.5" y="8" width="3" height="6" rx="1" />
    <rect x="6.5" y="5" width="3" height="9" rx="1" />
    <rect x="11.5" y="2" width="3" height="12" rx="1" opacity="0.2" />
  </svg>
);

export const PriorityHigh = ({ className }: { className?: string }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" className={className} fill="currentColor">
    <rect x="1.5" y="8" width="3" height="6" rx="1" />
    <rect x="6.5" y="5" width="3" height="9" rx="1" />
    <rect x="11.5" y="2" width="3" height="12" rx="1" />
  </svg>
);

export const PriorityCritical = ({ className }: { className?: string }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" className={className} fill="none">
    <rect x="1" y="1" width="14" height="14" rx="3" fill="currentColor" />
    <path d="M8 4v5" stroke="white" strokeWidth="2" strokeLinecap="round" />
    <circle cx="8" cy="12" r="1" fill="white" />
  </svg>
);

// ── Type icons (Issue vs Task) ──

/** Issue icon — like GitLab's issue icon (circle with dot) */
export const TypeIssue = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.5" />
    <circle cx="7" cy="7" r="2" fill="currentColor" />
  </svg>
);

/** Task icon — like GitLab's task icon (checkbox) */
export const TypeTask = ({ className }: { className?: string }) => (
  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" className={className}>
    <rect x="1.5" y="1.5" width="11" height="11" rx="2" stroke="currentColor" strokeWidth="1.5" />
    <path d="M4.5 7.5L6.25 9.25L9.75 4.75" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

// ── Icon maps (status value → component) ──

import type { TaskStatus, TaskPriority } from "./task-config";

export const STATUS_ICONS: Record<TaskStatus, React.FC<{ className?: string }>> = {
  open: StatusOpen,
  closed: StatusClosed,
};

export const PRIORITY_ICONS: Record<TaskPriority, React.FC<{ className?: string }>> = {
  low: PriorityLow,
  medium: PriorityMedium,
  high: PriorityHigh,
  critical: PriorityCritical,
};
