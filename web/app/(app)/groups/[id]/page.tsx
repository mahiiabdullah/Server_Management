"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useState } from "react";
import { GroupChips, GroupForm, ManageServers } from "@/components/groups";
import { EmptyState, ErrorState, LoadingBlock, StaleBanner } from "@/components/states";
import { AlertStateBadge, SeverityBadge, StatusBadge } from "@/components/status";
import { Badge, Button, Card, Meter, Modal, cx } from "@/components/ui";
import { api, isApiError } from "@/lib/api";
import { ago, dateTime, money } from "@/lib/format";
import { useGroup, useRole, useWrite } from "@/lib/queries";
import type { GroupDetail, Server } from "@/lib/schemas";

export default function GroupPage() {
  const { id } = useParams<{ id: string }>();
  const q = useGroup(id);
  const { isOperator, isAdmin } = useRole();
  const [editing, setEditing] = useState(false);
  const [managing, setManaging] = useState(false);
  const [deleting, setDeleting] = useState(false);

  if (q.error) {
    if (isApiError(q.error) && q.error.status === 404) {
      return (
        <EmptyState title="Group not found" action={<Link href="/groups" className="text-accent hover:underline">Back to groups</Link>}>
          It may have been deleted.
        </EmptyState>
      );
    }
    return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  }
  if (!q.data) return <LoadingBlock rows={8} />;
  const d = q.data;
  const g = d.group;

  return (
    <div className="flex flex-col gap-5">
      <div>
        <Link href="/groups" className="text-sm text-accent hover:underline">
          ← Groups
        </Link>
        <div className="mt-2 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-xl font-semibold tracking-tight">{g.name}</h1>
            <p className="mt-1 max-w-3xl text-sm text-foreground-muted">{g.description || "No description."}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            {isOperator && (
              <>
                <Button variant="primary" onClick={() => setManaging(true)}>
                  Add or remove servers
                </Button>
                <Button onClick={() => setEditing(true)}>Edit</Button>
              </>
            )}
            {isAdmin && (
              <Button variant="ghost" className="text-crit! hover:bg-crit/10" onClick={() => setDeleting(true)}>
                Delete
              </Button>
            )}
          </div>
        </div>
      </div>

      <StaleBanner available={d.metrics_available} asOf={d.as_of} />
      <Tiles d={d} />
      <ServersCard d={d} canEdit={isOperator} onManage={() => setManaging(true)} />
      <div className="grid gap-5 lg:grid-cols-2">
        <AlertsCard d={d} />
        <CostCard d={d} />
      </div>

      <GroupForm key={editing ? "edit-open" : "edit-closed"} open={editing} onClose={() => setEditing(false)} group={g} />
      <ManageServers
        key={managing ? "manage-open" : "manage-closed"}
        open={managing}
        onClose={() => setManaging(false)}
        group={g}
        current={d.servers.map((s) => s.id)}
      />
      <DeleteGroup open={deleting} onClose={() => setDeleting(false)} d={d} />
    </div>
  );
}

function avg(servers: Server[], pick: (s: Server) => number | null | undefined) {
  const vals = servers.map(pick).filter((v): v is number => v != null);
  return vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : null;
}

function Tiles({ d }: { d: GroupDetail }) {
  const live = d.servers.filter((s) => s.enabled);
  const online = d.servers.filter((s) => s.status === "online").length;
  const firing = d.open_alerts.filter((a) => a.state !== "pending");
  const crit = firing.filter((a) => a.severity === "critical").length;
  const tiles = [
    { label: "Servers", value: String(d.servers.length), sub: d.servers.length - live.length ? `${d.servers.length - live.length} disabled` : "", tone: "" },
    { label: "Online", value: `${online}/${live.length}`, sub: "", tone: online < live.length ? "text-warn" : live.length ? "text-ok" : "" },
    { label: "Open alerts", value: String(firing.length), sub: crit ? `${crit} critical` : "", tone: crit ? "text-crit" : firing.length ? "text-warn" : "" },
    {
      label: "Cost this month",
      value: !d.costs_configured ? "—" : money(d.cost?.projected ?? 0, d.currency),
      sub: d.costs_configured ? `${money(d.cost?.to_date ?? 0, d.currency)} so far · projected` : "pricing not set up",
      tone: "",
    },
  ];
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
      {tiles.map((t) => (
        <div key={t.label} className="rounded-card border border-border bg-surface px-4 py-3">
          <div className="text-xs text-foreground-muted">{t.label}</div>
          <div className={cx("mt-1 font-mono text-2xl font-semibold tabular-nums", t.tone)}>{t.value}</div>
          {t.sub && <div className="mt-0.5 text-xs text-foreground-muted">{t.sub}</div>}
        </div>
      ))}
    </div>
  );
}

function ServersCard({ d, canEdit, onManage }: { d: GroupDetail; canEdit: boolean; onManage: () => void }) {
  const others = (s: Server) => s.groups.filter((x) => x.id !== d.group.id);
  const cpu = avg(d.servers, (s) => s.metrics?.cpu);
  const mem = avg(d.servers, (s) => s.metrics?.memory);
  return (
    <Card
      title={`Servers (${d.servers.length})`}
      actions={
        d.servers.length > 0 && (
          <span className="flex items-center gap-3 text-xs text-foreground-muted">
            <span className="flex items-center gap-1.5">
              avg CPU <Meter value={cpu} label="Average CPU" threshold={0.9} />
            </span>
            <span className="hidden items-center gap-1.5 sm:flex">
              avg memory <Meter value={mem} label="Average memory" threshold={0.9} />
            </span>
          </span>
        )
      }
      className="overflow-hidden [&>div]:p-0"
    >
      {d.servers.length === 0 ? (
        <EmptyState
          title="No servers in this group yet"
          action={
            canEdit && (
              <Button variant="primary" onClick={onManage}>
                Add servers
              </Button>
            )
          }
        >
          A server can be in this group and in others at the same time.
        </EmptyState>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-border bg-background/60 text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">Status</th>
                <th scope="col" className="px-3 py-2 font-medium">Server</th>
                <th scope="col" className="px-3 py-2 font-medium">CPU</th>
                <th scope="col" className="hidden px-3 py-2 font-medium sm:table-cell">Memory</th>
                <th scope="col" className="hidden px-3 py-2 font-medium sm:table-cell">Disk</th>
                <th scope="col" className="hidden px-3 py-2 font-medium lg:table-cell">Last scrape</th>
                <th scope="col" className="hidden px-3 py-2 font-medium md:table-cell">Also in</th>
                <th scope="col" className="hidden px-3 py-2 text-right font-medium xl:table-cell">Monthly cost</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {d.servers.map((s) => (
                <tr key={s.id} className="hover:bg-background/50">
                  <td className="px-3 py-2.5">
                    <StatusBadge status={s.status} />
                  </td>
                  <td className="px-3 py-2.5">
                    <Link href={`/servers/${s.id}`} className="font-medium text-accent hover:underline">
                      {s.name}
                    </Link>
                    <div className="font-mono text-xs text-foreground-muted">{s.target_addr}</div>
                  </td>
                  <td className="px-3 py-2.5">
                    <Meter value={s.metrics?.cpu} label={`${s.name} CPU`} threshold={0.9} />
                  </td>
                  <td className="hidden px-3 py-2.5 sm:table-cell">
                    <Meter value={s.metrics?.memory} label={`${s.name} memory`} threshold={0.9} />
                  </td>
                  <td className="hidden px-3 py-2.5 sm:table-cell">
                    <Meter value={s.metrics?.disk} label={`${s.name} disk`} threshold={0.85} />
                  </td>
                  <td className="hidden px-3 py-2.5 text-xs lg:table-cell">{ago(s.metrics?.last_scrape)}</td>
                  <td className="hidden max-w-56 px-3 py-2.5 md:table-cell">
                    <GroupChips groups={others(s)} empty="only here" />
                  </td>
                  <td className="hidden px-3 py-2.5 text-right font-mono text-xs xl:table-cell">
                    {s.monthly_cost != null ? money(s.monthly_cost, d.currency) : <span className="text-foreground-muted">not priced</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function AlertsCard({ d }: { d: GroupDetail }) {
  return (
    <Card title="Open alerts" actions={<Link href="/alerts" className="text-xs text-accent hover:underline">All alerts →</Link>}>
      {d.open_alerts.length === 0 ? (
        <EmptyState title="No open alerts in this group" />
      ) : (
        <ul className="divide-y divide-border">
          {d.open_alerts.map((a) => (
            <li key={a.id} className="flex items-start gap-3 py-2.5">
              <SeverityBadge severity={a.severity} />
              <div className="min-w-0 flex-1 text-sm">
                <p>{a.message}</p>
                <p className="mt-0.5 text-xs text-foreground-muted">
                  {a.server_name && `${a.server_name} · `}started {dateTime(a.started_at)}
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

function CostCard({ d }: { d: GroupDetail }) {
  const c = d.cost;
  const shared = c ? d.servers.filter((s) => c.shared_server_ids.includes(s.id)) : [];
  return (
    <Card title="Cost this month" actions={<Link href={`/costs?group=${d.group.id}`} className="text-xs text-accent hover:underline">Cost details →</Link>}>
      {!d.costs_configured ? (
        <EmptyState title="Pricing is not set up">Add pricing plans in Settings → Pricing and assign them to servers.</EmptyState>
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          <div className="flex items-baseline justify-between">
            <span className="text-foreground-muted">So far</span>
            <span className="font-mono text-lg font-semibold">{money(c?.to_date ?? 0, d.currency)}</span>
          </div>
          <div className="flex items-baseline justify-between">
            <span className="text-foreground-muted">Projected to month end</span>
            <span className="font-mono">{money(c?.projected ?? 0, d.currency)}</span>
          </div>
          {c && c.shared.length > 0 && (
            <div className="border-t border-border pt-3">
              <p className="mb-1 text-xs text-foreground-muted">Group costs not split over servers</p>
              {c.shared.map((l) => (
                <div key={l.name} className="flex justify-between">
                  <span>{l.name}</span>
                  <span className="font-mono">{money(l.projected, d.currency)}</span>
                </div>
              ))}
            </div>
          )}
          {shared.length > 0 && (
            <p className="rounded-card border border-accent/30 bg-accent/5 px-3 py-2 text-xs">
              <Badge tone="accent">shared</Badge> {shared.map((s) => s.name).join(", ")} {shared.length === 1 ? "is" : "are"} also in other groups. Their full
              cost is counted here and in each of those groups, until a split rule is decided.
            </p>
          )}
        </div>
      )}
    </Card>
  );
}

function DeleteGroup({ open, onClose, d }: { open: boolean; onClose: () => void; d: GroupDetail }) {
  const router = useRouter();
  const [error, setError] = useState("");
  const del = useWrite(() => api(`/groups/${d.group.id}`, { method: "DELETE" }), [["groups"], ["servers"], ["costs"]]);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Delete ${d.group.name}?`}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            disabled={del.isPending}
            onClick={() =>
              del.mutate(undefined, {
                onSuccess: () => router.push("/groups"),
                onError: (e) => setError(isApiError(e) ? e.message : "Delete failed."),
              })
            }
          >
            {del.isPending ? "Deleting…" : "Delete group"}
          </Button>
        </>
      }
    >
      <p>
        The group is removed. Its {d.servers.length} server{d.servers.length === 1 ? "" : "s"} stay registered and keep their other groups; nothing on any
        server changes.
      </p>
      {error && (
        <p role="alert" className="mt-3 text-crit">
          {error}
        </p>
      )}
    </Modal>
  );
}
