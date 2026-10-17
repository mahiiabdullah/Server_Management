"use client";

import Link from "next/link";
import { CostTrendChart } from "@/components/charts";
import { EmptyState, ErrorState, LoadingBlock, PageHeader, StaleBanner } from "@/components/states";
import { AlertStateBadge, KindBadge, SeverityBadge } from "@/components/status";
import { Card, Meter, Skeleton, cx } from "@/components/ui";
import { ago, bytes, money, pct, rate } from "@/lib/format";
import { useAlerts, useCostTrend, useFleetSummary, useUnderutilized } from "@/lib/queries";
import type { FleetSummary } from "@/lib/schemas";

export default function OverviewPage() {
  const fleet = useFleetSummary();
  return (
    <>
      <PageHeader title="Overview" subtitle={fleet.data?.as_of ? `Live data as of ${ago(fleet.data.as_of)} · refreshes every 30 s` : "Fleet health, utilisation, alerts and cost"} />
      {fleet.error ? (
        <ErrorState error={fleet.error} onRetry={() => fleet.refetch()} />
      ) : !fleet.data ? (
        <LoadingBlock rows={6} />
      ) : (
        <div className="flex flex-col gap-5">
          <StaleBanner available={fleet.data.monitoring.available} asOf={fleet.data.as_of} />
          {fleet.data.servers.registered === 0 ? (
            <Card>
              <EmptyState
                title="No servers registered yet"
                action={
                  <Link href="/servers?add=1" className="inline-flex h-9 items-center rounded-card bg-accent px-3.5 text-sm font-medium text-white">
                    Add your first server
                  </Link>
                }
              >
                Install Node Exporter on a server (deploy/node-exporter/install.sh), then register its address here. Every number on this page
                comes from your inventory and live telemetry.
              </EmptyState>
            </Card>
          ) : (
            <>
              <StatusCards s={fleet.data.servers} />
              <Utilisation f={fleet.data} />
            </>
          )}
          <div className="grid gap-5 lg:grid-cols-2">
            <ActiveAlerts />
            <RecentIncidents />
          </div>
          <div className="grid gap-5 lg:grid-cols-5">
            <CostCard f={fleet.data} />
            <TopUnderutilized />
          </div>
        </div>
      )}
    </>
  );
}

function StatusCards({ s }: { s: FleetSummary["servers"] }) {
  const cards = [
    { label: "Registered", value: s.registered, href: "/servers", tone: "" },
    { label: "Enabled", value: s.enabled, href: "/servers?enabled=true", tone: "" },
    { label: "Disabled", value: s.disabled, href: "/servers?status=disabled", tone: "text-muted", icon: "○" },
    { label: "Online", value: s.online, href: "/servers?status=online", tone: "text-ok", icon: "●" },
    { label: "Offline", value: s.offline, href: "/servers?status=offline", tone: s.offline > 0 ? "text-crit" : "", icon: "✕" },
    { label: "Unknown", value: s.unknown, href: "/servers?status=unknown", tone: s.unknown > 0 ? "text-warn" : "", icon: "?" },
  ];
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
      {cards.map((c) => (
        <Link key={c.label} href={c.href} className="rounded-card border border-border bg-surface px-4 py-3 hover:border-accent/50">
          <div className="flex items-center gap-1.5 text-xs text-foreground-muted">
            {c.icon && (
              <span aria-hidden className={c.tone}>
                {c.icon}
              </span>
            )}
            {c.label}
          </div>
          <div className={cx("mt-1 font-mono text-2xl font-semibold tabular-nums", c.tone)}>{c.value}</div>
        </Link>
      ))}
    </div>
  );
}

function UtilTile({ label, value, detail }: { label: string; value: number | null; detail: string }) {
  return (
    <div className="rounded-card border border-border bg-surface px-4 py-3">
      <div className="text-xs text-foreground-muted">{label}</div>
      <div className="mt-1 font-mono text-2xl font-semibold tabular-nums">{pct(value)}</div>
      <div className="mt-2">
        <Meter value={value} label={label} showValue={false} />
      </div>
      <div className="mt-2 text-xs text-foreground-muted">{detail}</div>
    </div>
  );
}

function Utilisation({ f }: { f: FleetSummary }) {
  const u = f.utilization;
  const of = (n: number) => `${n} of ${f.servers.enabled} enabled server${f.servers.enabled === 1 ? "" : "s"} reporting`;
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <UtilTile label="Fleet CPU" value={u.cpu.value} detail={`${u.capacity.cores} cores · ${of(u.cpu.servers_with_data)}`} />
      <UtilTile label="Fleet memory" value={u.memory.value} detail={`${bytes(u.capacity.memory_bytes)} total · ${of(u.memory.servers_with_data)}`} />
      <UtilTile label="Fleet disk" value={u.disk.value} detail={`${bytes(u.capacity.disk_bytes)} total · ${of(u.disk.servers_with_data)}`} />
      <div className="rounded-card border border-border bg-surface px-4 py-3">
        <div className="text-xs text-foreground-muted">Fleet network</div>
        <dl className="mt-1 grid grid-cols-2 gap-2">
          <div>
            <dt className="text-xs text-foreground-muted">
              <span aria-hidden>↓</span> In
            </dt>
            <dd className="font-mono text-lg font-semibold tabular-nums">{rate(u.network.rx)}</dd>
          </div>
          <div>
            <dt className="text-xs text-foreground-muted">
              <span aria-hidden>↑</span> Out
            </dt>
            <dd className="font-mono text-lg font-semibold tabular-nums">{rate(u.network.tx)}</dd>
          </div>
        </dl>
        <div className="mt-2 text-xs text-foreground-muted">{of(u.network.servers_with_data)}</div>
      </div>
    </div>
  );
}

function ActiveAlerts() {
  const q = useAlerts("state=firing,acknowledged&page_size=6");
  return (
    <Card title="Active alerts" actions={<Link href="/alerts" className="text-xs text-accent hover:underline">All alerts →</Link>}>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : q.data.items.length === 0 ? (
        <EmptyState title="No active alerts">
          <span aria-hidden className="text-ok">
            ●{" "}
          </span>
          Every enabled rule is currently clear.
        </EmptyState>
      ) : (
        <ul className="divide-y divide-border">
          {q.data.items.map((a) => (
            <li key={a.id} className="flex items-start gap-3 py-2.5">
              <SeverityBadge severity={a.severity} />
              <div className="min-w-0 flex-1">
                <p className="text-sm">{a.message}</p>
                <p className="mt-0.5 text-xs text-foreground-muted">
                  {a.rule_name} · started {ago(a.started_at)}
                  {a.server_id && (
                    <>
                      {" · "}
                      <Link href={`/servers/${a.server_id}`} className="text-accent hover:underline">
                        {a.server_name}
                      </Link>
                    </>
                  )}
                </p>
              </div>
              <AlertStateBadge state={a.state} />
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function RecentIncidents() {
  const q = useAlerts("state=resolved&page_size=5", false);
  return (
    <Card title="Recent incidents">
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : q.data.items.length === 0 ? (
        <EmptyState title="No resolved incidents yet" />
      ) : (
        <ul className="divide-y divide-border">
          {q.data.items.map((a) => (
            <li key={a.id} className="py-2.5 text-sm">
              <div className="flex items-center gap-2">
                <SeverityBadge severity={a.severity} />
                <span className="truncate">{a.rule_name}</span>
                {a.server_name && <span className="text-foreground-muted">· {a.server_name}</span>}
              </div>
              <p className="mt-0.5 text-xs text-foreground-muted">
                Resolved {ago(a.resolved_at)}
                {a.resolved_at && ` · lasted ${Math.max(1, Math.round((new Date(a.resolved_at).getTime() - new Date(a.started_at).getTime()) / 60000))} min`}
              </p>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function CostCard({ f }: { f: FleetSummary }) {
  const trend = useCostTrend(12);
  const c = f.cost;
  return (
    <Card title="Cost this month" className="lg:col-span-3" actions={<Link href="/costs" className="text-xs text-accent hover:underline">Cost analysis →</Link>}>
      {c.status === "not_configured" ? (
        <EmptyState
          title="Pricing not configured"
          action={
            <Link href="/settings?tab=pricing" className="text-sm text-accent hover:underline">
              Add pricing plans in Settings →
            </Link>
          }
        >
          No price has been entered, so no cost is shown. Costs are never guessed.
        </EmptyState>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-end gap-6">
            <div>
              <div className="text-xs text-foreground-muted">To date</div>
              <div className="font-mono text-2xl font-semibold tabular-nums">{money(c.to_date, c.currency)}</div>
            </div>
            <div>
              <div className="text-xs text-foreground-muted">Projected month</div>
              <div className="font-mono text-2xl font-semibold tabular-nums">{money(c.projected, c.currency)}</div>
            </div>
            <KindBadge kind="estimated" />
          </div>
          {c.servers_without_pricing > 0 && (
            <p className="text-xs text-warn">
              <span aria-hidden>⚠ </span>
              {c.servers_without_pricing} active server{c.servers_without_pricing === 1 ? " has" : "s have"} no pricing and {c.servers_without_pricing === 1 ? "is" : "are"} not included.
            </p>
          )}
          {trend.data?.status === "configured" ? <CostTrendChart points={trend.data.points} currency={trend.data.currency} height={180} /> : <Skeleton className="h-44 w-full" />}
        </div>
      )}
    </Card>
  );
}

function TopUnderutilized() {
  const q = useUnderutilized();
  return (
    <Card title="Underutilized servers" className="lg:col-span-2" actions={<Link href="/insights" className="text-xs text-accent hover:underline">Insights →</Link>}>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : q.data.underutilized.length === 0 ? (
        <EmptyState title="Nothing flagged">
          {q.data.insufficient_data > 0
            ? `${q.data.insufficient_data} server(s) do not have enough data yet (${q.data.rules.window_days}-day window, ≥${q.data.rules.min_coverage_pct}% coverage).`
            : `No server has p95 CPU under ${q.data.rules.cpu_p95_max_pct}% and p95 memory under ${q.data.rules.memory_p95_max_pct}% over ${q.data.rules.window_days} days.`}
        </EmptyState>
      ) : (
        <ul className="divide-y divide-border">
          {q.data.underutilized.slice(0, 5).map((s) => (
            <li key={s.server_id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
              <Link href={`/servers/${s.server_id}`} className="font-medium text-accent hover:underline">
                {s.name}
              </Link>
              <span className="text-xs text-foreground-muted">
                p95 CPU {pct(s.cpu_p95)} · mem {pct(s.memory_p95)}
                {s.possible_savings != null && ` · up to ${money(s.possible_savings, q.data.currency)}/mo`}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
