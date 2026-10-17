"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { EmptyState, ErrorState, LoadingBlock, PageHeader } from "@/components/states";
import { Badge, Card, Meter, Select } from "@/components/ui";
import { api } from "@/lib/api";
import { date, money, monthLabel, pct } from "@/lib/format";
import { useGroups, useUnderutilized } from "@/lib/queries";

export default function InsightsPage() {
  const q = useUnderutilized();
  return (
    <>
      <PageHeader title="Insights" subtitle="Evidence-based recommendations. Nothing is changed automatically." />
      <div className="flex flex-col gap-5">
        {q.error ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <LoadingBlock rows={5} />
        ) : (
          <>
            <Card title="Underutilized servers">
              <p className="mb-4 text-sm text-foreground-muted">
                Flagged when, over the last {q.data.rules.window_days} days ({date(q.data.window_start)} to {date(q.data.as_of)}), p95 CPU is below{" "}
                {q.data.rules.cpu_p95_max_pct}% <strong>and</strong> p95 memory is below {q.data.rules.memory_p95_max_pct}%, with at least{" "}
                {q.data.rules.min_coverage_pct}% data coverage. Savings are shown only for servers with pricing. Thresholds are in Settings → General.
              </p>
              {q.data.underutilized.length === 0 ? (
                <EmptyState title="No underutilized servers">
                  {q.data.evaluated} server(s) evaluated
                  {q.data.insufficient_data > 0 && `; ${q.data.insufficient_data} without enough data yet`}.
                </EmptyState>
              ) : (
                <>
                  {q.data.total_possible_savings != null && (
                    <p className="mb-3 text-sm">
                      Possible savings if all flagged servers were consolidated or removed: <strong className="font-mono">{money(q.data.total_possible_savings, q.data.currency)}</strong>
                      /month <Badge tone="accent">Estimated</Badge>
                    </p>
                  )}
                  <ul className="divide-y divide-border">
                    {q.data.underutilized.map((s) => (
                      <li key={s.server_id} className="py-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <Link href={`/servers/${s.server_id}`} className="font-medium text-accent hover:underline">
                            {s.name}
                          </Link>
                          <span className="font-mono text-sm">
                            {s.possible_savings != null ? `${money(s.possible_savings, q.data!.currency)}/mo` : <span className="text-xs text-foreground-muted">not priced, savings unknown</span>}
                          </span>
                        </div>
                        <p className="mt-1 text-sm text-foreground-muted">
                          p95 CPU {pct(s.cpu_p95, 1)} · p95 memory {pct(s.memory_p95, 1)} · average CPU {pct(s.avg_cpu, 1)} · coverage {pct(s.coverage)}
                        </p>
                        {s.recommendation && <p className="mt-1 text-sm">{s.recommendation}</p>}
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </Card>
            <Card title="Data coverage and freshness">
              {q.data.servers.length === 0 ? (
                <EmptyState title="No enabled servers" />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead className="text-left text-xs text-foreground-muted">
                      <tr>
                        <th scope="col" className="py-1.5 pr-3 font-medium">Server</th>
                        <th scope="col" className="py-1.5 pr-3 font-medium">Coverage ({q.data.rules.window_days} d)</th>
                        <th scope="col" className="py-1.5 pr-3 font-medium">p95 CPU</th>
                        <th scope="col" className="py-1.5 pr-3 font-medium">p95 memory</th>
                        <th scope="col" className="hidden py-1.5 font-medium md:table-cell">Assessment</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {q.data.servers.map((s) => (
                        <tr key={s.server_id}>
                          <td className="py-2 pr-3">
                            <Link href={`/servers/${s.server_id}`} className="text-accent hover:underline">
                              {s.name}
                            </Link>
                          </td>
                          <td className="py-2 pr-3">
                            <Meter value={s.coverage} threshold={2} label={`${s.name} coverage`} />
                          </td>
                          <td className="py-2 pr-3 font-mono text-xs">{pct(s.cpu_p95, 1)}</td>
                          <td className="py-2 pr-3 font-mono text-xs">{pct(s.memory_p95, 1)}</td>
                          <td className="hidden py-2 text-xs text-foreground-muted md:table-cell">{s.reason}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )}
        <Compare />
      </div>
    </>
  );
}

type CompareResp = { metric: string; a: string; b: string; note: string; items: { server_id: string; name: string; a: number | null; b: number | null; delta: number | null }[] };

function months(n: number) {
  const d = new Date();
  return Array.from({ length: n }, (_, i) => new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - i, 1)).toISOString().slice(0, 7));
}

function Compare() {
  const opts = useMemo(() => months(12), []);
  const [metric, setMetric] = useState("cpu");
  const [a, setA] = useState(opts[1]);
  const [b, setB] = useState(opts[0]);
  const [group, setGroup] = useState("");
  const groups = useGroups();
  const q = useQuery({
    queryKey: ["compare", metric, a, b, group],
    queryFn: () => api<CompareResp>(`/insights/compare?metric=${metric}&a=${a}&b=${b}${group ? `&group=${group}` : ""}`),
  });
  return (
    <Card
      title="Month-to-month comparison"
      actions={
        <div className="flex flex-wrap gap-2">
          <Select aria-label="Metric" value={metric} onChange={(e) => setMetric(e.target.value)} className="w-auto">
            <option value="cpu">CPU</option>
            <option value="memory">Memory</option>
            <option value="disk">Disk</option>
          </Select>
          <Select aria-label="First month" value={a} onChange={(e) => setA(e.target.value)} className="w-auto">
            {opts.map((m) => (
              <option key={m} value={m}>
                {monthLabel(m)}
              </option>
            ))}
          </Select>
          <Select aria-label="Second month" value={b} onChange={(e) => setB(e.target.value)} className="w-auto">
            {opts.map((m) => (
              <option key={m} value={m}>
                {monthLabel(m)}
              </option>
            ))}
          </Select>
          <Select aria-label="Group" value={group} onChange={(e) => setGroup(e.target.value)} className="w-auto">
            <option value="">All groups</option>
            {groups.data?.items.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </Select>
        </div>
      }
    >
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={4} />
      ) : q.data.items.length === 0 ? (
        <EmptyState title="No servers to compare" />
      ) : (
        <>
          <p className="mb-3 text-xs text-foreground-muted">{q.data.note}</p>
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 pr-3 font-medium">Server</th>
                <th scope="col" className="py-1.5 pr-3 text-right font-medium">{monthLabel(q.data.a)}</th>
                <th scope="col" className="py-1.5 pr-3 text-right font-medium">{monthLabel(q.data.b)}</th>
                <th scope="col" className="py-1.5 text-right font-medium">Change</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {q.data.items.map((r) => (
                <tr key={r.server_id}>
                  <td className="py-2 pr-3">{r.name}</td>
                  <td className="py-2 pr-3 text-right font-mono">{pct(r.a, 1)}</td>
                  <td className="py-2 pr-3 text-right font-mono">{pct(r.b, 1)}</td>
                  <td className="py-2 text-right font-mono">
                    {r.delta == null ? (
                      <span className="text-foreground-muted">no data</span>
                    ) : (
                      <span>
                        <span aria-hidden>{r.delta > 0.005 ? "▲ " : r.delta < -0.005 ? "▼ " : "• "}</span>
                        {r.delta > 0 ? "+" : ""}
                        {(r.delta * 100).toFixed(1)} pts
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </Card>
  );
}
