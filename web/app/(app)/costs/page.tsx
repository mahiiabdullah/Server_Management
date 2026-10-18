"use client";

import Link from "next/link";
import { GroupChips } from "@/components/groups";
import { Suspense, useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { CostTrendChart } from "@/components/charts";
import { EmptyState, ErrorState, LoadingBlock, PageHeader } from "@/components/states";
import { Badge, Card, Select } from "@/components/ui";
import { KindBadge } from "@/components/status";
import { money, monthLabel, num } from "@/lib/format";
import { useCostSummary, useCostTrend, useGroups, useRole } from "@/lib/queries";

export default function CostsPage() {
  return (
    <Suspense fallback={<LoadingBlock rows={8} />}>
      <Costs />
    </Suspense>
  );
}

const CATEGORY_LABEL: Record<string, string> = { compute: "Compute", storage: "Storage", network: "Network", license: "Licences", fixed: "Fixed" };

function lastMonths(n: number) {
  const out: string[] = [];
  const d = new Date();
  for (let i = 0; i < n; i++) {
    const m = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - i, 1));
    out.push(m.toISOString().slice(0, 7));
  }
  return out;
}

function Costs() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const { isAdmin } = useRole();
  const period = params.get("period") ?? "";
  const group = params.get("group") ?? "";
  const q = useCostSummary(period, group);
  const trend = useCostTrend(12);
  const groups = useGroups();
  const months = useMemo(() => lastMonths(13), []);
  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params.toString());
    if (v) p.set(k, v);
    else p.delete(k);
    router.replace(`${pathname}?${p}`, { scroll: false });
  };

  return (
    <>
      <PageHeader
        title="Costs"
        subtitle="Estimated from your configured prices. Actual amounts appear only from imported invoices."
        actions={
          <>
            <label htmlFor="period" className="sr-only">
              Period
            </label>
            <Select id="period" value={period || months[0]} onChange={(e) => set("period", e.target.value === months[0] ? "" : e.target.value)} className="w-auto">
              {months.map((m, i) => (
                <option key={m} value={m}>
                  {monthLabel(m)}
                  {i === 0 ? " (current)" : ""}
                </option>
              ))}
            </Select>
            <label htmlFor="group" className="sr-only">
              Group
            </label>
            <Select id="group" value={group} onChange={(e) => set("group", e.target.value)} className="w-auto">
              <option value="">Whole fleet</option>
              {groups.data?.items.map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                </option>
              ))}
            </Select>
          </>
        }
      />
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={8} />
      ) : q.data.status === "not_configured" ? (
        <div className="flex flex-col gap-5">
          <Card>
            <EmptyState
              title="Pricing not configured"
              action={
                isAdmin ? (
                  <Link href="/settings?tab=pricing" className="inline-flex h-9 items-center rounded-card bg-accent px-3.5 text-sm font-medium text-white">
                    Add a pricing plan
                  </Link>
                ) : (
                  <span className="text-sm text-foreground-muted">Ask an admin to add pricing plans.</span>
                )
              }
            >
              No price or fixed cost has been entered, so no cost is shown for {monthLabel(q.data.period)}. BM Monitor never guesses prices or turns
              CPU usage into money.
            </EmptyState>
          </Card>
          <Formula formula={q.data.formula} />
        </div>
      ) : (
        <Summary data={q.data} trend={trend.data} />
      )}
    </>
  );
}

type Configured = Extract<NonNullable<ReturnType<typeof useCostSummary>["data"]>, { status: "configured" }>;

function Summary({ data, trend }: { data: Configured; trend: ReturnType<typeof useCostTrend>["data"] }) {
  const r = data.result;
  const cur = data.currency;
  const cats = Object.keys(r.by_category_projected).sort((a, b) => r.by_category_projected[b] - r.by_category_projected[a]);
  const maxCat = Math.max(1, ...cats.map((c) => r.by_category_projected[c]));
  const unpriced = r.servers.filter((s) => !s.priced);
  return (
    <div className="flex flex-col gap-5">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Tile label={r.closed ? "Total" : "To date"} value={money(r.to_date, cur)} badge={<KindBadge kind="estimated" />} sub={r.closed ? "Closed month" : `${num(r.hours_elapsed, 0)} of ${num(r.hours_in_month, 0)} hours elapsed`} />
        <Tile label="Projected month" value={r.closed ? money(r.to_date, cur) : money(r.projected, cur)} badge={<KindBadge kind="estimated" />} sub="If current servers and prices stay as they are" />
        <Tile label="Actual (imported)" value={data.actual ? money(data.actual.total, cur) : "Not available"} badge={data.actual ? <KindBadge kind="actual" /> : undefined} sub={data.actual ? "From uploaded invoices" : "Upload invoices in Settings → Pricing"} />
        <Tile label="Servers without pricing" value={String(r.servers_without_pricing.length)} sub={r.servers_without_pricing.length ? "Not included in the totals" : "Every active server is priced"} warn={r.servers_without_pricing.length > 0} />
      </div>

      <div className="grid gap-5 lg:grid-cols-5">
        <Card title="Monthly trend" className="lg:col-span-3">
          {trend?.status === "configured" ? <CostTrendChart points={trend.points} currency={trend.currency} /> : <LoadingBlock rows={4} />}
          <p className="mt-2 text-xs text-foreground-muted">The current month shows the projected total. Closed months use the figure recorded when the month ended.</p>
        </Card>
        <Card title="By category (projected)" className="lg:col-span-2">
          {cats.length === 0 ? (
            <EmptyState title="No costs in this period" />
          ) : (
            <ul className="flex flex-col gap-3">
              {cats.map((c) => (
                <li key={c}>
                  <div className="flex justify-between text-sm">
                    <span>{CATEGORY_LABEL[c] ?? c}</span>
                    <span className="font-mono">{money(r.by_category_projected[c], cur)}</span>
                  </div>
                  <div className="mt-1 h-2 rounded-full bg-border" aria-hidden>
                    <div className="h-2 rounded-full bg-[var(--series-1)]" style={{ width: `${(r.by_category_projected[c] / maxCat) * 100}%` }} />
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      {r.groups.length > 0 && (
        <Card title="By group">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 font-medium">Group</th>
                <th scope="col" className="py-1.5 text-right font-medium">Servers</th>
                <th scope="col" className="py-1.5 text-right font-medium">To date</th>
                <th scope="col" className="py-1.5 text-right font-medium">Projected</th>
                <th scope="col" className="hidden py-1.5 text-right font-medium sm:table-cell">Shared costs</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {r.groups.map((g) => (
                <tr key={g.group_id}>
                  <td className="py-2">
                    <Link href={`/groups/${g.group_id}`} className="text-accent hover:underline">
                      {g.name || "Unnamed group"}
                    </Link>
                  </td>
                  <td className="py-2 text-right font-mono">
                    {g.server_ids.length}
                    {g.shared_server_ids.length > 0 && <span className="ml-1 text-xs text-warn">({g.shared_server_ids.length} shared)</span>}
                  </td>
                  <td className="py-2 text-right font-mono">{money(g.to_date, cur)}</td>
                  <td className="py-2 text-right font-mono">{money(g.projected, cur)}</td>
                  <td className="hidden py-2 text-right text-xs text-foreground-muted sm:table-cell">{g.shared.map((l) => l.name).join(", ") || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {r.groups.some((g) => g.shared_server_ids.length > 0) && (
            <p className="mt-3 text-xs text-foreground-muted">
              A server in several groups counts in full in each of them, so group totals can add up to more than the fleet total. The fleet total counts every
              server once. How to split a shared server between groups is not decided yet.
            </p>
          )}
        </Card>
      )}

      <Card title="Per server">
        {r.servers.length === 0 ? (
          <EmptyState title="No server was active in this period" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-foreground-muted">
                <tr>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Server</th>
                  <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Groups</th>
                  <th scope="col" className="py-1.5 pr-3 text-right font-medium">Active h</th>
                  <th scope="col" className="py-1.5 pr-3 text-right font-medium">To date</th>
                  <th scope="col" className="py-1.5 pr-3 text-right font-medium">Projected</th>
                  <th scope="col" className="py-1.5 pr-3 text-right font-medium">Actual</th>
                  <th scope="col" className="hidden py-1.5 font-medium lg:table-cell">How</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {r.servers.map((s) => (
                  <tr key={s.server_id} className="align-top">
                    <td className="py-2 pr-3">
                      {s.deleted ? (
                        <span>
                          {s.name} <Badge>removed</Badge>
                        </span>
                      ) : (
                        <Link href={`/servers/${s.server_id}`} className="text-accent hover:underline">
                          {s.name}
                        </Link>
                      )}
                    </td>
                    <td className="hidden py-2 pr-3 md:table-cell">
                      <GroupChips groups={s.groups} />
                    </td>
                    <td className="py-2 pr-3 text-right font-mono">{num(s.active_hours, 1)}</td>
                    <td className="py-2 pr-3 text-right font-mono">{s.priced || s.lines.length ? money(s.to_date, cur) : <span className="text-warn">not priced</span>}</td>
                    <td className="py-2 pr-3 text-right font-mono">{s.priced || s.lines.length ? money(s.projected, cur) : "—"}</td>
                    <td className="py-2 pr-3 text-right font-mono">{data.actual?.by_server[s.server_id] != null ? money(data.actual.by_server[s.server_id], cur) : "—"}</td>
                    <td className="hidden py-2 text-xs text-foreground-muted lg:table-cell">
                      {s.lines.map((l, i) => (
                        <div key={i}>
                          {l.name}: {money(l.rate, cur)}/{l.unit} × {l.unit === "hour" ? `${num(l.active_hours, 1)} h` : `${(l.fraction * 100).toFixed(1)}%`}
                          {l.note && ` (${l.note})`}
                        </div>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {unpriced.length > 0 && (
          <p className="mt-3 text-xs text-warn">
            <span aria-hidden>⚠ </span>
            Not priced: {unpriced.map((s) => s.name).join(", ")}. Assign a plan on each server page.
          </p>
        )}
      </Card>
      {r.fleet_shared.length > 0 && (
        <Card title="Fleet-wide fixed costs (not allocated to servers)">
          <ul className="text-sm">
            {r.fleet_shared.map((l, i) => (
              <li key={i} className="flex justify-between py-1">
                <span>
                  {l.name} <span className="text-xs text-foreground-muted">({l.category})</span>
                </span>
                <span className="font-mono">{money(l.projected, cur)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Formula formula={data.formula} />
    </div>
  );
}

function Tile({ label, value, sub, badge, warn }: { label: string; value: string; sub?: string; badge?: React.ReactNode; warn?: boolean }) {
  return (
    <div className="rounded-card border border-border bg-surface px-4 py-3">
      <div className="flex items-center justify-between gap-2 text-xs text-foreground-muted">
        {label}
        {badge}
      </div>
      <div className={`mt-1 font-mono text-2xl font-semibold tabular-nums ${warn ? "text-warn" : ""}`}>{value}</div>
      {sub && <div className="mt-1 text-xs text-foreground-muted">{sub}</div>}
    </div>
  );
}

const FORMULA_LABELS: Record<string, string> = {
  active_hours: "Active hours",
  hourly_plan: "Hourly plan",
  monthly_plan: "Monthly plan",
  fixed_cost: "Fixed costs",
  projected: "Projected month",
  rounding: "Rounding",
  timezone: "Billing timezone",
  currency: "Currency",
  kind: "Estimated vs actual",
  not_money: "Utilisation",
  version: "Formula version",
};

function Formula({ formula }: { formula: Record<string, string | number> }) {
  return (
    <Card title="How this is calculated">
      <dl className="grid gap-x-6 gap-y-3 text-sm md:grid-cols-2">
        {Object.entries(FORMULA_LABELS).map(([k, label]) =>
          formula[k] != null ? (
            <div key={k}>
              <dt className="text-xs font-medium text-foreground-muted">{label}</dt>
              <dd className="mt-0.5">{String(formula[k])}</dd>
            </div>
          ) : null,
        )}
      </dl>
    </Card>
  );
}
