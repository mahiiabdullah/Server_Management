// Formatting helpers. Missing data is rendered as an em dash, never as zero.

export const NONE = "—";

export function pct(ratio: number | null | undefined, digits = 0): string {
  if (ratio == null || Number.isNaN(ratio)) return NONE;
  return `${(ratio * 100).toFixed(digits)}%`;
}

const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

export function bytes(n: number | null | undefined, digits = 1): string {
  if (n == null) return NONE;
  let v = n;
  let i = 0;
  while (Math.abs(v) >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : digits)} ${UNITS[i]}`;
}

export function rate(n: number | null | undefined): string {
  if (n == null) return NONE;
  return `${bytes(n)}/s`;
}

export function duration(seconds: number | null | undefined): string {
  if (seconds == null) return NONE;
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return NONE;
  const sec = Math.round((now - new Date(iso).getTime()) / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`;
  return `${Math.floor(sec / 86400)} d ago`;
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return NONE;
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function date(iso: string | null | undefined): string {
  if (!iso) return NONE;
  return new Date(iso).toLocaleDateString(undefined, { dateStyle: "medium" });
}

export function money(amount: number | null | undefined, currency: string): string {
  if (amount == null) return NONE;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${currency}`;
  }
}

export function num(n: number | null | undefined, digits = 2): string {
  if (n == null) return NONE;
  return n.toFixed(digits);
}

export function monthLabel(period: string): string {
  const [y, m] = period.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, 1)).toLocaleDateString(undefined, { month: "short", year: "2-digit", timeZone: "UTC" });
}
