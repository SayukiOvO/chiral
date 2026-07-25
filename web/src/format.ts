export function bytes(n: number): string {
  const [v, u] = splitBytes(n);
  return `${v} ${u}`;
}

/** Split a byte count into [value, unit] so the two can be styled apart. */
export function splitBytes(n: number): [string, string] {
  if (!n) return ["0", "B"];
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const value = n / 1024 ** i;
  return [value.toFixed(i === 0 ? 0 : 1), units[i]];
}

export function bitrate(bytesPerSec: number): string {
  return `${bytes(bytesPerSec)}/s`;
}

export function splitBitrate(bytesPerSec: number): [string, string] {
  const [v, u] = splitBytes(bytesPerSec);
  return [v, `${u}/s`];
}

export function relativeTime(unixSec?: number): string {
  if (!unixSec) return "—";
  const diff = Date.now() / 1000 - unixSec;
  if (diff < 5) return "刚刚";
  if (diff < 60) return `${Math.floor(diff)} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

export function percent(n: number): string {
  return `${n.toFixed(0)}%`;
}

/** Absolute date for an expiry timestamp; 0 means "no expiry". */
export function expiryLabel(unixSec: number): string {
  if (!unixSec) return "永不过期";
  const d = new Date(unixSec * 1000);
  const iso = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return unixSec * 1000 < Date.now() ? `${iso} 已过期` : iso;
}

/** Renewal period as a human interval; 0 means no auto-renewal. */
export function periodLabel(seconds: number): string {
  if (!seconds) return "不续期";
  const days = Math.round(seconds / 86400);
  if (days >= 1) return `每 ${days} 天`;
  return `每 ${Math.round(seconds / 3600)} 小时`;
}

/** Quota usage as a fraction, or null when the quota is unlimited. */
export function quotaFraction(used: number, quota: number): number | null {
  if (!quota) return null;
  return Math.min(used / quota, 1);
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** Seconds since the epoch for a yyyy-mm-dd input, or 0 when blank. */
export function dateToUnix(value: string): number {
  if (!value) return 0;
  const ms = Date.parse(`${value}T23:59:59`);
  return Number.isNaN(ms) ? 0 : Math.floor(ms / 1000);
}

/** yyyy-mm-dd for a date input, or "" when there is no expiry. */
export function unixToDate(unixSec: number): string {
  if (!unixSec) return "";
  const d = new Date(unixSec * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
