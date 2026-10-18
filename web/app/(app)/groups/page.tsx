"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { GroupForm } from "@/components/groups";
import { EmptyState, ErrorState, LoadingBlock, PageHeader, StaleBanner } from "@/components/states";
import { Badge, Button, Card, cx } from "@/components/ui";
import { money } from "@/lib/format";
import { useGroupSummaries, useRole } from "@/lib/queries";
import type { GroupSummaries } from "@/lib/schemas";

type Summary = GroupSummaries["items"][number];

export default function GroupsPage() {
  const q = useGroupSummaries();
  const { isOperator } = useRole();
  const router = useRouter();
  const [creating, setCreating] = useState(false);

  return (
    <>
      <PageHeader
        title="Groups"
        subtitle="Products and the servers that run them. A server can be in several groups."
        actions={
          isOperator && (
            <Button variant="primary" onClick={() => setCreating(true)}>
              + New group
            </Button>
          )
        }
      />
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={6} />
      ) : (
        <div className="flex flex-col gap-4">
          <StaleBanner available={q.data.metrics_available} asOf={q.data.as_of} />
          {q.data.items.length === 0 ? (
            <Card>
              <EmptyState
                title="No groups yet"
                action={
                  isOperator && (
                    <Button variant="primary" onClick={() => setCreating(true)}>
                      Create the first group
                    </Button>
                  )
                }
              >
                A group is a product or purpose, such as puku-web-chat or puku-bot. Put every server it runs on in it to see their health, alerts and cost
                together.
              </EmptyState>
            </Card>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {q.data.items.map((g) => (
                <GroupCard key={g.id} g={g} currency={q.data.currency} costsConfigured={q.data.costs_configured} />
              ))}
            </div>
          )}
        </div>
      )}
      <GroupForm key={creating ? "new-open" : "new-closed"} open={creating} onClose={() => setCreating(false)} onSaved={(g) => router.push(`/groups/${g.id}`)} />
    </>
  );
}

function GroupCard({ g, currency, costsConfigured }: { g: Summary; currency: string; costsConfigured: boolean }) {
  const down = g.status.offline + g.status.unknown;
  const alerts = g.open_alerts.critical + g.open_alerts.warning + g.open_alerts.info;
  const health = g.server_count === 0 ? "empty" : g.status.offline > 0 || g.open_alerts.critical > 0 ? "crit" : down > 0 || alerts > 0 ? "warn" : "ok";
  return (
    <Link
      href={`/groups/${g.id}`}
      className={cx(
        "group flex flex-col rounded-card border bg-surface p-4 transition-colors hover:border-accent/60",
        health === "crit" ? "border-crit/50" : health === "warn" ? "border-warn/50" : "border-border",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <h2 className="truncate text-base font-semibold group-hover:text-accent">{g.name}</h2>
        <span
          aria-label={{ ok: "Healthy", warn: "Needs attention", crit: "Problem", empty: "No servers" }[health]}
          className={cx(
            "mt-1.5 inline-block size-2.5 shrink-0 rounded-full",
            { ok: "bg-ok", warn: "bg-warn", crit: "bg-crit", empty: "bg-border" }[health],
          )}
        />
      </div>
      <p className="mt-1 line-clamp-2 min-h-10 text-sm text-foreground-muted">{g.description || "No description."}</p>

      <dl className="mt-4 grid grid-cols-3 gap-3 border-t border-border pt-3 text-sm">
        <div>
          <dt className="text-xs text-foreground-muted">Servers</dt>
          <dd className="mt-0.5 font-mono text-lg font-semibold tabular-nums">{g.server_count}</dd>
        </div>
        <div>
          <dt className="text-xs text-foreground-muted">Online</dt>
          <dd className={cx("mt-0.5 font-mono text-lg font-semibold tabular-nums", g.status.online < g.server_count - g.status.disabled && "text-warn")}>
            {g.status.online}
            <span className="text-sm font-normal text-foreground-muted">/{g.server_count - g.status.disabled}</span>
          </dd>
        </div>
        <div>
          <dt className="text-xs text-foreground-muted">This month</dt>
          <dd className="mt-0.5 font-mono text-lg font-semibold tabular-nums">
            {!costsConfigured ? <span className="text-sm font-normal text-foreground-muted">—</span> : money(g.cost_projected ?? 0, currency)}
          </dd>
        </div>
      </dl>

      <div className="mt-3 flex flex-wrap gap-1.5">
        {g.open_alerts.critical > 0 && <Badge tone="crit">{g.open_alerts.critical} critical</Badge>}
        {g.open_alerts.warning > 0 && <Badge tone="warn">{g.open_alerts.warning} warning</Badge>}
        {g.status.offline > 0 && <Badge tone="crit">{g.status.offline} offline</Badge>}
        {g.status.unknown > 0 && <Badge tone="warn">{g.status.unknown} unknown</Badge>}
        {g.status.disabled > 0 && <Badge>{g.status.disabled} disabled</Badge>}
        {g.shared_servers > 0 && <Badge tone="accent">{g.shared_servers} shared</Badge>}
        {alerts === 0 && down === 0 && g.server_count > 0 && <Badge tone="ok">All healthy</Badge>}
        {g.server_count === 0 && <Badge>No servers yet</Badge>}
      </div>
    </Link>
  );
}
