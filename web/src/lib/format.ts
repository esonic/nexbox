/**
 * Format a duration in milliseconds to a human-readable short string.
 * e.g. 500 → "500ms", 3200 → "3.2s", 65000 → "1m 5s"
 */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = Math.round(seconds % 60);
  return `${minutes}m ${remainingSeconds}s`;
}

/**
 * Format a total duration in milliseconds to a detailed string with hours.
 * e.g. 3600000 → "1h 0m 0s", 125000 → "2m 5s"
 */
export function formatTotalDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

/**
 * Format elapsed time in ms to a coarse human-readable string.
 * Suitable for "heartbeat X ago" style labels.
 * e.g. 5000 → "5s", 90000 → "1m", 7200000 → "2h", 90000000 → "1d 1h"
 */
export function formatElapsed(ms: number): string {
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  const hr = Math.floor(min / 60);
  const remainMin = min % 60;
  if (hr < 24) return remainMin > 0 ? `${hr}h ${remainMin}m` : `${hr}h`;
  const day = Math.floor(hr / 24);
  return `${day}d ${hr % 24}h`;
}

/**
 * Format a date string to a short localized representation.
 * e.g. "2026-02-15T14:30:00Z" → "Feb 15, 2:30 PM"
 */
export function formatShortDate(dateStr: string): string {
  try {
    return new Date(dateStr).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return dateStr;
  }
}
