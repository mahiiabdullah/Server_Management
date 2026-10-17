"use client";

import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { bytes, money, monthLabel } from "@/lib/format";

// Validated categorical slots (globals.css). Colour follows the series, never its rank.
export const SERIES = ["var(--series-1)", "var(--series-2)", "var(--series-3)"];

export type Unit = "ratio" | "bytes_per_second" | "load";

function fmtValue(v: number | null | undefined, unit: Unit) {
  if (v == null) return "no data";
  if (unit === "ratio") return `${(v * 100).toFixed(1)}%`;
  if (unit === "bytes_per_second") return `${bytes(v)}/s`;
  return v.toFixed(2);
}

function fmtAxis(v: number, unit: Unit) {
  if (unit === "ratio") return `${Math.round(v * 100)}%`;
  if (unit === "bytes_per_second") return bytes(v, 0);
  return String(Math.round(v * 10) / 10);
}

function fmtTime(t: number, spanH: number) {
  const d = new Date(t);
  if (spanH <= 24) return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export type ChartSeries = { key: string; label: string; points: [string, number | null][] };

/**
 * Time series with gaps for missing samples (connectNulls=false: a gap is never
 * drawn as zero), an optional threshold line, crosshair tooltip and legend.
 */
export function TimeSeries({
  series,
  unit,
  threshold,
  spanHours,
  height = 220,
  summary,
  area = false,
}: {
  series: ChartSeries[];
  unit: Unit;
  threshold?: number;
  spanHours: number;
  height?: number;
  summary: string;
  area?: boolean;
}) {
  const shown = series.slice(0, 3); // at most 3 series per chart (validated palette cap)
  const rows: Record<string, number | null>[] = [];
  const index = new Map<string, number>();
  for (const s of shown) {
    for (const [t, v] of s.points) {
      let i = index.get(t);
      if (i === undefined) {
        i = rows.length;
        index.set(t, i);
        rows.push({ t: new Date(t).getTime() });
      }
      rows[i][s.key] = v;
    }
  }
  rows.sort((a, b) => (a.t as number) - (b.t as number));
  const hasData = rows.some((r) => shown.some((s) => r[s.key] != null));
  const Chart = area ? AreaChart : LineChart;

  return (
    <figure className="m-0">
      <figcaption className="sr-only">{summary}</figcaption>
      {!hasData ? (
        <div className="flex items-center justify-center rounded-card border border-dashed border-border text-sm text-foreground-muted" style={{ height }}>
          No samples in this range.
        </div>
      ) : (
        <div style={{ height }} aria-hidden>
          <ResponsiveContainer width="100%" height="100%">
            <Chart data={rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
              <CartesianGrid stroke="var(--grid)" vertical={false} />
              <XAxis
                dataKey="t"
                type="number"
                scale="time"
                domain={["dataMin", "dataMax"]}
                tickFormatter={(t) => fmtTime(t, spanHours)}
                stroke="var(--foreground-muted)"
                tick={{ fontSize: 11 }}
                tickLine={false}
                axisLine={{ stroke: "var(--border)" }}
                minTickGap={40}
              />
              <YAxis
                width={52}
                tickFormatter={(v) => fmtAxis(v, unit)}
                domain={unit === "ratio" ? [0, 1] : [0, "auto"]}
                stroke="var(--foreground-muted)"
                tick={{ fontSize: 11 }}
                tickLine={false}
                axisLine={false}
              />
              <Tooltip
                cursor={{ stroke: "var(--foreground-muted)", strokeDasharray: "3 3" }}
                contentStyle={{ background: "var(--surface)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 12, color: "var(--foreground)" }}
                labelFormatter={(t) => new Date(t as number).toLocaleString()}
                formatter={(v, name) => [fmtValue(v as number | null, unit), name]}
              />
              {shown.length > 1 && <Legend iconType="plainline" wrapperStyle={{ fontSize: 12, color: "var(--foreground-muted)" }} />}
              {threshold != null && (
                <ReferenceLine
                  y={threshold}
                  stroke="var(--foreground-muted)"
                  strokeDasharray="4 4"
                  label={{ value: `threshold ${fmtAxis(threshold, unit)}`, position: "insideTopRight", fontSize: 10, fill: "var(--foreground-muted)" }}
                />
              )}
              {shown.map((s, i) =>
                area ? (
                  <Area
                    key={s.key}
                    type="monotone"
                    dataKey={s.key}
                    name={s.label}
                    stroke={SERIES[i]}
                    fill={SERIES[i]}
                    fillOpacity={0.12}
                    strokeWidth={2}
                    connectNulls={false}
                    isAnimationActive={false}
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 2, stroke: "var(--surface)" }}
                  />
                ) : (
                  <Line
                    key={s.key}
                    type="monotone"
                    dataKey={s.key}
                    name={s.label}
                    stroke={SERIES[i]}
                    strokeWidth={2}
                    connectNulls={false}
                    isAnimationActive={false}
                    dot={false}
                    activeDot={{ r: 4, strokeWidth: 2, stroke: "var(--surface)" }}
                  />
                ),
              )}
            </Chart>
          </ResponsiveContainer>
        </div>
      )}
    </figure>
  );
}

export type TrendPoint = { period: string; estimated: number; projected?: number | null; actual: number | null; closed: boolean };

/** Monthly cost bars: estimated (to date or closed), with actual beside it when imported. */
export function CostTrendChart({ points, currency, height = 220 }: { points: TrendPoint[]; currency: string; height?: number }) {
  const hasActual = points.some((p) => p.actual != null);
  const data = points.map((p) => ({
    period: p.period,
    estimated: p.closed ? p.estimated : (p.projected ?? p.estimated),
    actual: p.actual,
    open: !p.closed,
  }));
  const last = points[points.length - 1];
  const summary = `Monthly estimated cost for ${points.length} months. ${last ? `${monthLabel(last.period)}: ${money(last.projected ?? last.estimated, currency)} ${last.closed ? "" : "projected"}` : ""}`;
  return (
    <figure className="m-0">
      <figcaption className="sr-only">{summary}</figcaption>
      <div style={{ height }} aria-hidden>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barGap={2}>
            <CartesianGrid stroke="var(--grid)" vertical={false} />
            <XAxis dataKey="period" tickFormatter={monthLabel} stroke="var(--foreground-muted)" tick={{ fontSize: 11 }} tickLine={false} axisLine={{ stroke: "var(--border)" }} />
            <YAxis width={64} tickFormatter={(v) => money(v, currency).replace(/\.00$/, "")} stroke="var(--foreground-muted)" tick={{ fontSize: 11 }} tickLine={false} axisLine={false} />
            <Tooltip
              cursor={{ fill: "var(--grid)", opacity: 0.5 }}
              contentStyle={{ background: "var(--surface)", border: "1px solid var(--border)", borderRadius: 8, fontSize: 12, color: "var(--foreground)" }}
              labelFormatter={(p) => monthLabel(String(p))}
              formatter={(v, name, item) => {
                const open = (item?.payload as { open?: boolean })?.open;
                const label = name === "estimated" ? (open ? "Estimated (projected)" : "Estimated") : "Actual (imported)";
                return [v == null ? "not available" : money(v as number, currency), label];
              }}
            />
            {hasActual && <Legend wrapperStyle={{ fontSize: 12 }} formatter={(v) => (v === "estimated" ? "Estimated" : "Actual")} />}
            <Bar dataKey="estimated" fill="var(--series-1)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
            {hasActual && <Bar dataKey="actual" fill="var(--series-2)" radius={[4, 4, 0, 0]} isAnimationActive={false} />}
          </BarChart>
        </ResponsiveContainer>
      </div>
    </figure>
  );
}
