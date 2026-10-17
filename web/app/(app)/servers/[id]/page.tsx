"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { TimeSeries, type ChartSeries } from "@/components/charts";
import { DeleteServer, EnableToggle } from "@/components/server-actions";
import { ServerForm } from "@/components/server-form";
import { EmptyState, ErrorState, LoadingBlock, StaleBanner } from "@/components/states";
import { AlertStateBadge, KindBadge, SeverityBadge, StatusBadge } from "@/components/status";
import { Badge, Button, Card, Field, Input, Meter, Select, Tabs } from "@/components/ui";
import { api, isApiError } from "@/lib/api";
import { ago, bytes, date, dateTime, duration, money, num, pct, rate } from "@/lib/format";
import { useHistory, useRole, useServer, useWrite } from "@/lib/queries";
import type { History, ServerDetail } from "@/lib/schemas";

const RANGES = [
  { id: "1h", label: "1h", hours: 1 },
  { id: "6h", label: "6h", hours: 6 },
  { id: "24h", label: "24h", hours: 24 },
  { id: "7d", label: "7d", hours: 168 },
  { id: "30d", label: "30d", hours: 720 },
] as const;

type Rule = { id: string; name: string; kind: string; threshold: number; severity: string; enabled: boolean; scope_group_id: string | null; scope_server_id: string | null };

export default function ServerDetailPage() {
  const { id } = useParams<{ id: string }>();
  const q = useServer(id);
  const { isOperator } = useRole();
  const [editing, setEditing] = useState(false);

  if (q.error) {
    if (isApiError(q.error) && q.error.status === 404) {
      return <EmptyState title="Server not found" action={<Link href="/servers" className="text-accent hover:underline">Back to servers</Link>}>It may have been removed.</EmptyState>;
    }
    return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  }
  if (!q.data) return <LoadingBlock rows={8} />;
  const d = q.data;
  const s = d.server;
  const m = s.metrics;

  return (
    <div className="flex flex-col gap-5">
      <div>
        <Link href="/servers" className="text-sm text-accent hover:underline">
          ← Servers
        </Link>
        <div className="mt-2 flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex flex-wrap items-center gap-3">
              <h1 className="text-xl font-semibold tracking-tight">{s.name}</h1>
              <StatusBadge status={s.status} />
            </div>
            <p className="mt-1 text-sm text-foreground-muted">
              <span className="font-mono">{s.target_addr}</span> · {s.hostname} · {s.environment}
              {s.group_name && ` · ${s.group_name}`}
              {s.location && ` · ${s.location}`}
            </p>
            <p className="mt-1 text-xs text-foreground-muted">
              Last scrape {ago(m?.last_scrape)} · uptime {duration(m?.uptime_seconds)} · every {s.scrape_interval_s}s
              {m?.exporter_version && ` · Node Exporter ${m.exporter_version}`}
            </p>
            {Object.keys(s.labels).length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {Object.entries(s.labels).map(([k, v]) => (
                  <Badge key={k}>
                    {k}={v}
                  </Badge>
                ))}
              </div>
            )}
          </div>
          <div className="flex gap-2">
            {isOperator && <Button onClick={() => setEditing(true)}>Edit</Button>}
            <EnableToggle server={s} size="md" />
            <DeleteServer server={s} size="md" />
          </div>
        </div>
      </div>

      <StaleBanner available={d.metrics_available} asOf={d.as_of} />
      {s.status === "offline" && (
        <div role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-4 py-2.5 text-sm text-crit">
          <span aria-hidden>✕ </span>The last scrape failed. Check that Node Exporter is running and port {s.target_addr.split(":").pop()} is open to the monitoring host.
        </div>
      )}
      {!s.enabled && (
        <div role="status" className="rounded-card border border-border bg-background px-4 py-2.5 text-sm text-foreground-muted">
          Monitoring is disabled for this server: it is not scraped and no billing time accrues.
        </div>
      )}

      <ValueCards d={d} />
      <Charts id={s.id} />

      <div className="grid gap-5 lg:grid-cols-2">
        <Card title="Filesystems" id="fs">
          {!m?.filesystems?.length ? (
            <EmptyState title="No filesystem data" />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-foreground-muted">
                  <tr>
                    <th scope="col" className="py-1.5 pr-3 font-medium">Mount</th>
                    <th scope="col" className="py-1.5 pr-3 font-medium">Type</th>
                    <th scope="col" className="py-1.5 pr-3 font-medium">Used</th>
                    <th scope="col" className="py-1.5 font-medium">Free / size</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {m.filesystems.map((f) => (
                    <tr key={f.mountpoint}>
                      <td className="py-2 pr-3 font-mono text-xs">{f.mountpoint}</td>
                      <td className="py-2 pr-3 text-xs text-foreground-muted">{f.fstype}</td>
                      <td className="py-2 pr-3">
                        <Meter value={f.used_ratio} label={`${f.mountpoint} used`} />
                      </td>
                      <td className="py-2 font-mono text-xs">
                        {bytes(f.avail_bytes)} / {bytes(f.size_bytes)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
        <Card title="Alerts and incidents">
          {d.open_alerts.length === 0 && d.recent_incidents.length === 0 ? (
            <EmptyState title="No alerts for this server" />
          ) : (
            <ul className="divide-y divide-border">
              {[...d.open_alerts, ...d.recent_incidents].map((a) => (
                <li key={a.id} className="flex items-start gap-3 py-2.5">
                  <SeverityBadge severity={a.severity} />
                  <div className="min-w-0 flex-1 text-sm">
                    <p>{a.message}</p>
                    <p className="mt-0.5 text-xs text-foreground-muted">
                      Started {dateTime(a.started_at)}
                      {a.resolved_at && ` · resolved ${ago(a.resolved_at)}`}
                    </p>
                  </div>
                  <AlertStateBadge state={a.state} />
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        <PricingCard d={d} />
        <Thresholds serverId={s.id} groupId={s.group_id} />
      </div>
      <ServerForm key={editing ? "edit-open" : "edit-closed"} open={editing} onClose={() => setEditing(false)} server={s} />
    </div>
  );
}

function ValueCards({ d }: { d: ServerDetail }) {
  const m = d.server.metrics;
  const cap = d.server;
  const cards = [
    { label: "CPU", value: pct(m?.cpu, 1), meter: m?.cpu, sub: m?.cores != null ? `${m.cores} cores` : cap.cpu_cores ? `${cap.cpu_cores} cores` : "" },
    { label: "Memory", value: pct(m?.memory, 1), meter: m?.memory, sub: m?.memory_total_bytes != null ? `${bytes(m.memory_used_bytes)} of ${bytes(m.memory_total_bytes)}` : "" },
    { label: "Disk (fullest)", value: pct(m?.disk, 1), meter: m?.disk, sub: m?.disk_total_bytes != null ? `${bytes(m.disk_used_bytes)} of ${bytes(m.disk_total_bytes)} total` : "" },
    { label: "Load 1 / 5 / 15", value: `${num(m?.load1)} / ${num(m?.load5)} / ${num(m?.load15)}`, sub: m?.cores ? `${m.cores} cores` : "" },
    { label: "Network in / out", value: `${rate(m?.net_rx)} / ${rate(m?.net_tx)}`, sub: "physical interfaces" },
  ];
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
      {cards.map((c) => (
        <div key={c.label} className="rounded-card border border-border bg-surface px-4 py-3">
          <div className="text-xs text-foreground-muted">{c.label}</div>
          <div className={`mt-1 font-mono font-semibold tabular-nums ${"meter" in c ? "text-lg" : "text-sm leading-7 whitespace-nowrap"}`}>{c.value}</div>
          {"meter" in c && <div className="mt-1.5"><Meter value={c.meter} label={c.label} showValue={false} /></div>}
          {c.sub && <div className="mt-1 text-xs text-foreground-muted">{c.sub}</div>}
        </div>
      ))}
    </div>
  );
}

function pick(h: History | undefined, name: string): ChartSeries[] {
  return (h?.series ?? [])
    .filter((s) => s.name === name)
    .map((s, i) => ({ key: `${name}${i}`, label: s.labels?.mountpoint ?? name, points: s.points }));
}

function Charts({ id }: { id: string }) {
  const [range, setRange] = useState<(typeof RANGES)[number]["id"]>("24h");
  const h = useHistory(id, range, "cpu,memory,disk,network,load");
  const spanH = RANGES.find((r) => r.id === range)!.hours;
  const data = h.data;
  const disk = pick(data, "disk").sort((a, b) => last(b) - last(a));
  return (
    <Card
      title="History"
      actions={
        <div className="flex items-center gap-3">
          {data && <span className="hidden text-xs text-foreground-muted sm:inline">Data coverage {pct(data.coverage)} · step {data.step_s}s</span>}
          <Tabs label="Time range" tabs={RANGES.map((r) => ({ id: r.id, label: r.label }))} value={range} onChange={setRange} />
        </div>
      }
    >
      {h.error ? (
        <ErrorState error={h.error} onRetry={() => h.refetch()} />
      ) : !data ? (
        <LoadingBlock rows={6} />
      ) : (
        <div className="grid gap-6 lg:grid-cols-2">
          <ChartBlock title="CPU used">
            <TimeSeries series={pick(data, "cpu").map((s) => ({ ...s, label: "CPU" }))} unit="ratio" threshold={0.9} spanHours={spanH} summary={`CPU utilisation over ${range}; dashed line is the 90% alert threshold.`} area />
          </ChartBlock>
          <ChartBlock title="Memory used">
            <TimeSeries series={pick(data, "memory").map((s) => ({ ...s, label: "Memory" }))} unit="ratio" threshold={0.9} spanHours={spanH} summary={`Memory utilisation over ${range}.`} area />
          </ChartBlock>
          <ChartBlock title={disk.length > 3 ? `Disk used (3 fullest of ${disk.length} filesystems)` : "Disk used per filesystem"}>
            <TimeSeries series={disk} unit="ratio" threshold={0.85} spanHours={spanH} summary={`Disk utilisation per filesystem over ${range}; dashed line is the 85% threshold.`} />
          </ChartBlock>
          <ChartBlock title="Network">
            <TimeSeries
              series={[...pick(data, "network_rx").map((s) => ({ ...s, label: "In" })), ...pick(data, "network_tx").map((s) => ({ ...s, label: "Out" }))]}
              unit="bytes_per_second"
              spanHours={spanH}
              summary={`Network traffic in and out over ${range}.`}
            />
          </ChartBlock>
          <ChartBlock title="Load average">
            <TimeSeries
              series={[...pick(data, "load1").map((s) => ({ ...s, label: "1 min" })), ...pick(data, "load5").map((s) => ({ ...s, label: "5 min" })), ...pick(data, "load15").map((s) => ({ ...s, label: "15 min" }))]}
              unit="load"
              spanHours={spanH}
              summary={`Load average (1, 5 and 15 minutes) over ${range}.`}
            />
          </ChartBlock>
        </div>
      )}
    </Card>
  );
}

function last(s: ChartSeries) {
  for (let i = s.points.length - 1; i >= 0; i--) if (s.points[i][1] != null) return s.points[i][1] as number;
  return -1;
}

function ChartBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h3 className="mb-2 text-xs font-medium text-foreground-muted">{title}</h3>
      {children}
    </div>
  );
}

function PricingCard({ d }: { d: ServerDetail }) {
  const { isAdmin } = useRole();
  const plans = useQuery({
    queryKey: ["plans"],
    queryFn: () => api<{ items: { id: string; name: string; amount: number; unit: string; archived: boolean }[] }>("/pricing-plans"),
    enabled: isAdmin,
  });
  const [plan, setPlan] = useState("");
  const [from, setFrom] = useState(() => new Date().toISOString().slice(0, 10));
  const [error, setError] = useState("");
  const assign = useWrite(
    () => api(`/servers/${d.server.id}/pricing`, { method: "PUT", body: { plan_id: plan || null, effective_from: from } }),
    [["server", d.server.id], ["servers"], ["costs"], ["fleet"], ["cost-trend"]],
  );
  const current = d.pricing.find((p) => !p.effective_to || new Date(p.effective_to) > new Date());
  const c = d.cost;
  return (
    <Card title="Pricing and cost this month" actions={<KindBadge kind="estimated" />}>
      {current ? (
        <p className="text-sm">
          Plan <strong>{current.plan_name}</strong>: {money(current.amount, current.currency)} per {current.unit} · since {date(current.effective_from)}
        </p>
      ) : (
        <p className="text-sm text-warn">
          <span aria-hidden>⚠ </span>Pricing not configured for this server. No cost is calculated for it.
        </p>
      )}
      {c && c.priced && (
        <dl className="mt-3 grid grid-cols-3 gap-3 text-sm">
          <div>
            <dt className="text-xs text-foreground-muted">To date</dt>
            <dd className="font-mono font-semibold">{money(c.to_date, d.currency)}</dd>
          </div>
          <div>
            <dt className="text-xs text-foreground-muted">Projected</dt>
            <dd className="font-mono font-semibold">{money(c.projected, d.currency)}</dd>
          </div>
          <div>
            <dt className="text-xs text-foreground-muted">Active hours</dt>
            <dd className="font-mono font-semibold">{num(c.active_hours, 1)}</dd>
          </div>
        </dl>
      )}
      {c && c.lines.length > 0 && (
        <ul className="mt-3 space-y-1 text-xs text-foreground-muted">
          {c.lines.map((l, i) => (
            <li key={i}>
              {l.name} ({l.category}): {money(l.rate, d.currency)}/{l.unit} × {l.unit === "hour" ? `${num(l.active_hours, 1)} h` : `${(l.fraction * 100).toFixed(1)}% of month`} = {money(l.to_date, d.currency)}
              {l.note && ` · ${l.note}`}
            </li>
          ))}
        </ul>
      )}
      {d.pricing.length > 1 && (
        <details className="mt-3 text-xs">
          <summary className="cursor-pointer text-foreground-muted">Pricing history ({d.pricing.length})</summary>
          <ul className="mt-2 space-y-1">
            {d.pricing.map((p) => (
              <li key={p.id}>
                {p.plan_name} · {money(p.amount, p.currency)}/{p.unit} · {date(p.effective_from)} → {p.effective_to ? date(p.effective_to) : "now"}
              </li>
            ))}
          </ul>
        </details>
      )}
      {isAdmin && plans.data && plans.data.items.filter((p) => !p.archived).length === 0 && (
        <p className="mt-4 border-t border-border pt-4 text-sm text-foreground-muted">
          No pricing plans exist yet.{" "}
          <Link href="/settings?tab=pricing" className="text-accent hover:underline">
            Create one in Settings → Pricing
          </Link>{" "}
          to assign it here.
        </p>
      )}
      {isAdmin && plans.data && plans.data.items.some((p) => !p.archived) && (
        <form
          className="mt-4 flex flex-wrap items-end gap-2 border-t border-border pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError("");
            assign.mutate(undefined, { onError: (err) => setError(isApiError(err) ? Object.values(err.fields)[0] ?? err.message : "Failed.") });
          }}
        >
          <div className="min-w-48 flex-1">
            <Field label="Assign plan" error={error}>
              {(id) => (
                <Select id={id} value={plan} onChange={(e) => setPlan(e.target.value)}>
                  <option value="">No plan (end pricing)</option>
                  {plans.data?.items
                    .filter((p) => !p.archived)
                    .map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name} · {money(p.amount, d.currency)}/{p.unit}
                      </option>
                    ))}
                </Select>
              )}
            </Field>
          </div>
          <Field label="From">
            {(id) => <Input id={id} type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="w-40" />}
          </Field>
          <Button type="submit" variant="primary" disabled={assign.isPending}>
            Apply
          </Button>
        </form>
      )}
    </Card>
  );
}

function Thresholds({ serverId, groupId }: { serverId: string; groupId: string | null }) {
  const rules = useQuery({ queryKey: ["rules"], queryFn: () => api<{ items: Rule[] }>("/alert-rules") });
  const applies = (rules.data?.items ?? []).filter(
    (r) => r.enabled && r.kind !== "cost_increase" && r.kind !== "monitoring" && (r.scope_server_id ? r.scope_server_id === serverId : r.scope_group_id ? r.scope_group_id === groupId : true),
  );
  const unit = (r: Rule) => (r.kind === "disk_forecast" ? ` (forecast horizon ${r.threshold} h)` : r.kind === "unreachable" ? "" : ` > ${r.threshold}%`);
  return (
    <Card title="Thresholds for this server" actions={<Link href="/alerts?tab=rules" className="text-xs text-accent hover:underline">Edit rules →</Link>}>
      {!rules.data ? (
        <LoadingBlock rows={3} />
      ) : applies.length === 0 ? (
        <EmptyState title="No enabled rules apply" />
      ) : (
        <ul className="divide-y divide-border text-sm">
          {applies.map((r) => (
            <li key={r.id} className="flex items-center justify-between gap-2 py-2">
              <span>
                {r.name}
                <span className="text-foreground-muted">{unit(r)}</span>
                {r.scope_server_id && <span className="ml-1 text-xs text-accent">(this server only)</span>}
              </span>
              <SeverityBadge severity={r.severity as "info" | "warning" | "critical"} />
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
