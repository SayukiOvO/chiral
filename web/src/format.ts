export function bytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function bitrate(bytesPerSec: number): string {
  return `${bytes(bytesPerSec)}/s`;
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
