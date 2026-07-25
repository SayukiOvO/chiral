import { getLang } from "./lib/i18n";

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
  const zh = getLang() === "zh";
  if (diff < 5) return zh ? "刚刚" : "just now";
  const n = (v: number) => Math.floor(v);
  if (diff < 60) return zh ? `${n(diff)} 秒前` : `${n(diff)}s ago`;
  if (diff < 3600) return zh ? `${n(diff / 60)} 分钟前` : `${n(diff / 60)}m ago`;
  if (diff < 86400) return zh ? `${n(diff / 3600)} 小时前` : `${n(diff / 3600)}h ago`;
  return zh ? `${n(diff / 86400)} 天前` : `${n(diff / 86400)}d ago`;
}

export function percent(n: number): string {
  return `${n.toFixed(0)}%`;
}

/** Absolute date for an expiry timestamp; 0 means "no expiry". */
export function expiryLabel(unixSec: number): string {
  if (!unixSec) return getLang() === "zh" ? "永不过期" : "No expiry";
  const d = new Date(unixSec * 1000);
  const iso = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return unixSec * 1000 < Date.now() ? `${iso} ${getLang() === "zh" ? "已过期" : "(expired)"}` : iso;
}

/** Renewal period as a human interval; 0 means no auto-renewal. */
export function periodLabel(seconds: number): string {
  const zh = getLang() === "zh";
  if (!seconds) return zh ? "不续期" : "No renewal";
  const days = Math.round(seconds / 86400);
  if (days >= 1) return zh ? `每 ${days} 天` : `every ${days}d`;
  const hours = Math.round(seconds / 3600);
  return zh ? `每 ${hours} 小时` : `every ${hours}h`;
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
