import { describe, expect, it } from "vitest";
import { ago, bytes, duration, money, NONE, pct, rate } from "@/lib/format";

describe("formatters", () => {
  it("never renders missing data as zero", () => {
    for (const f of [pct, bytes, rate, duration]) expect(f(null)).toBe(NONE);
    expect(money(null, "USD")).toBe(NONE);
    expect(ago(null)).toBe(NONE);
  });

  it("formats ratios as percentages", () => {
    expect(pct(0.374)).toBe("37%");
    expect(pct(0.374, 1)).toBe("37.4%");
    expect(pct(0)).toBe("0%");
  });

  it("uses binary byte units", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1536)).toBe("1.5 KiB");
    expect(bytes(64 * 1024 ** 3)).toBe("64.0 GiB");
    expect(rate(52428800)).toBe("50.0 MiB/s");
  });

  it("formats durations compactly", () => {
    expect(duration(42)).toBe("42s");
    expect(duration(3 * 3600 + 5 * 60)).toBe("3h 5m");
    expect(duration(31 * 86400 + 6 * 3600)).toBe("31d 6h");
  });

  it("formats relative times against a given now", () => {
    const now = Date.parse("2026-10-15T08:10:00Z");
    expect(ago("2026-10-15T08:09:30Z", now)).toBe("30s ago");
    expect(ago("2026-10-15T08:05:00Z", now)).toBe("5 min ago");
    expect(ago("2026-10-15T05:10:00Z", now)).toBe("3 h ago");
  });

  it("formats money with exactly two decimals", () => {
    expect(money(1840.5, "USD")).toMatch(/1,840\.50/);
  });
});
