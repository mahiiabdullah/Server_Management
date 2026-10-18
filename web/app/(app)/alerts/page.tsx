"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { EmptyState, ErrorState, LoadingBlock, PageHeader } from "@/components/states";
import { AlertStateBadge, SeverityBadge } from "@/components/status";
import { Badge, Button, Card, Dialog, Field, Input, Pagination, Select, Tabs } from "@/components/ui";
import { api, isApiError } from "@/lib/api";
import { ago, dateTime, duration } from "@/lib/format";
import { useAlerts, useGroups, useRole, useWrite } from "@/lib/queries";

type Tab = "firing" | "acknowledged" | "resolved" | "rules";

export default function AlertsPage() {
  return (
    <Suspense fallback={<LoadingBlock rows={6} />}>
      <Alerts />
    </Suspense>
  );
}

function Alerts() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const tab = (params.get("tab") as Tab) || "firing";
  const firing = useAlerts("state=firing&page_size=1");
  const acked = useAlerts("state=acknowledged&page_size=1");
  const setTab = (t: Tab) => router.replace(`${pathname}?tab=${t}`, { scroll: false });
  const count = (n?: number) => (n ? ` (${n})` : "");
  return (
    <>
      <PageHeader title="Alerts" subtitle="Incidents from your alert rules. One incident per problem; acknowledging stops reminders." />
      <Tabs
        label="Alert views"
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "firing", label: `Firing${count(firing.data?.total)}` },
          { id: "acknowledged", label: `Acknowledged${count(acked.data?.total)}` },
          { id: "resolved", label: "Resolved" },
          { id: "rules", label: "Rules" },
        ]}
      />
      <div className="mt-4">{tab === "rules" ? <Rules /> : <AlertList key={tab} state={tab} />}</div>
    </>
  );
}

function AlertList({ state }: { state: Exclude<Tab, "rules"> }) {
  const [page, setPage] = useState(1);
  const [severity, setSeverity] = useState("");
  const q = useAlerts(`state=${state}&page=${page}&page_size=25${severity ? `&severity=${severity}` : ""}`, state !== "resolved");
  const { isOperator } = useRole();
  const [shown, setShown] = useState<string | null>(null);
  const ack = useWrite((id: string) => api(`/alerts/${id}/ack`, { method: "POST" }), [["alerts"], ["fleet"]]);
  return (
    <>
      <div className="mb-3">
        <label htmlFor="sev" className="sr-only">
          Severity
        </label>
        <Select id="sev" value={severity} onChange={(e) => (setSeverity(e.target.value), setPage(1))} className="w-auto">
          <option value="">All severities</option>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </Select>
      </div>
      <Card className="[&>div]:p-0">
        {q.error ? (
          <div className="p-4">
            <ErrorState error={q.error} onRetry={() => q.refetch()} />
          </div>
        ) : !q.data ? (
          <div className="p-4">
            <LoadingBlock rows={5} />
          </div>
        ) : q.data.items.length === 0 ? (
          <EmptyState title={state === "firing" ? "Nothing is firing" : state === "acknowledged" ? "No acknowledged alerts" : "No resolved incidents"}>
            {state === "firing" && "All enabled rules are clear right now."}
          </EmptyState>
        ) : (
          <ul className="divide-y divide-border">
            {q.data.items.map((a) => (
              <li key={a.id} className="flex flex-wrap items-start gap-3 px-4 py-3">
                <SeverityBadge severity={a.severity} />
                <div className="min-w-0 flex-1">
                  <p className="text-sm">{a.message}</p>
                  <p className="mt-0.5 text-xs text-foreground-muted">
                    {a.rule_name}
                    {a.server_id && (
                      <>
                        {" · "}
                        <Link href={`/servers/${a.server_id}`} className="text-accent hover:underline">
                          {a.server_name}
                        </Link>
                      </>
                    )}
                    {" · started "}
                    {dateTime(a.started_at)}
                    {a.acknowledged_at && ` · acknowledged by ${a.acknowledged_by ?? "someone"} ${ago(a.acknowledged_at)}`}
                    {a.resolved_at && ` · resolved ${ago(a.resolved_at)}, lasted ${duration((new Date(a.resolved_at).getTime() - new Date(a.started_at).getTime()) / 1000)}`}
                  </p>
                </div>
                <AlertStateBadge state={a.state} />
                {isOperator && (
                  <Button size="sm" variant="ghost" aria-expanded={shown === a.id} onClick={() => setShown(shown === a.id ? null : a.id)}>
                    {shown === a.id ? "Hide recipients" : "Who was notified"}
                  </Button>
                )}
                {a.state === "firing" && isOperator && (
                  <Button size="sm" onClick={() => ack.mutate(a.id)} disabled={ack.isPending}>
                    Acknowledge
                  </Button>
                )}
                {shown === a.id && <Deliveries alertId={a.id} />}
              </li>
            ))}
          </ul>
        )}
      </Card>
      {q.data && q.data.total > q.data.page_size && (
        <div className="mt-3">
          <Pagination page={q.data.page} pageSize={q.data.page_size} total={q.data.total} onPage={setPage} />
        </div>
      )}
    </>
  );
}

type Delivery = { event: string; target: string; kind: string; status: string; attempts: number; sent_at: string | null; last_error: string | null; created_at: string };

/** Who an alert was sent to, and whether it arrived. */
function Deliveries({ alertId }: { alertId: string }) {
  const q = useQuery({ queryKey: ["deliveries", alertId], queryFn: () => api<{ items: Delivery[] }>(`/alerts/${alertId}/deliveries`) });
  return (
    <div className="basis-full rounded-card border border-border bg-background px-3 py-2 text-xs">
      {!q.data ? (
        <p className="text-foreground-muted">Loading…</p>
      ) : q.data.items.length === 0 ? (
        <p className="text-foreground-muted">Nobody was notified: no person or channel matched this alert.</p>
      ) : (
        <table className="w-full">
          <thead className="text-left text-foreground-muted">
            <tr>
              <th scope="col" className="py-1 pr-3 font-medium">Event</th>
              <th scope="col" className="py-1 pr-3 font-medium">Sent to</th>
              <th scope="col" className="py-1 pr-3 font-medium">Status</th>
              <th scope="col" className="hidden py-1 font-medium sm:table-cell">When</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {q.data.items.map((d, i) => (
              <tr key={i}>
                <td className="py-1 pr-3">{d.event}</td>
                <td className="py-1 pr-3">
                  {d.target} <span className="text-foreground-muted">({d.kind})</span>
                </td>
                <td className="py-1 pr-3">
                  <Badge tone={d.status === "sent" ? "ok" : d.status === "failed" ? "crit" : "warn"}>{d.status}</Badge>
                  {d.last_error && <span className="ml-1 text-crit">{d.last_error}</span>}
                </td>
                <td className="hidden py-1 sm:table-cell">{d.sent_at ? dateTime(d.sent_at) : `queued ${ago(d.created_at)}`}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

type Rule = {
  id: string;
  name: string;
  kind: string;
  threshold: number;
  for_seconds: number;
  severity: "info" | "warning" | "critical";
  scope_group_id: string | null;
  scope_server_id: string | null;
  enabled: boolean;
  repeat_after_s: number;
  system: boolean;
};

const KIND_LABEL: Record<string, string> = {
  monitoring: "Prometheus unreachable",
  unreachable: "Server unreachable",
  cpu: "CPU above",
  memory: "Memory above",
  disk: "Disk above",
  disk_forecast: "Disk full within",
  cost_increase: "Projected cost above 3-month average by",
};

function thresholdText(r: Pick<Rule, "kind" | "threshold">) {
  if (r.kind === "monitoring" || r.kind === "unreachable") return "";
  if (r.kind === "disk_forecast") return `${r.threshold} h`;
  return `${r.threshold}%`;
}

function Rules() {
  const { isAdmin } = useRole();
  const q = useQuery({ queryKey: ["rules"], queryFn: () => api<{ items: Rule[] }>("/alert-rules") });
  const groups = useGroups();
  const servers = useQuery({ queryKey: ["server-names"], queryFn: () => api<{ items: { id: string; name: string }[] }>("/servers?page_size=100") });
  const [edit, setEdit] = useState<Rule | "new" | null>(null);
  const del = useWrite((id: string) => api(`/alert-rules/${id}`, { method: "DELETE" }), [["rules"]]);
  const scope = (r: Rule) =>
    r.scope_server_id
      ? `Server ${servers.data?.items.find((s) => s.id === r.scope_server_id)?.name ?? "…"}`
      : r.scope_group_id
        ? `Group ${groups.data?.items.find((g) => g.id === r.scope_group_id)?.name ?? "…"}`
        : "Whole fleet";
  return (
    <Card title="Alert rules" actions={isAdmin && <Button variant="primary" size="sm" onClick={() => setEdit("new")}>+ New rule</Button>}>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={5} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 pr-3 font-medium">Rule</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Condition</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">For</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Scope</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Severity</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">State</th>
                <th scope="col" className="py-1.5">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {q.data.items.map((r) => (
                <tr key={r.id}>
                  <td className="py-2 pr-3">
                    {r.name} {r.system && <Badge>system</Badge>}
                  </td>
                  <td className="py-2 pr-3 text-foreground-muted">
                    {KIND_LABEL[r.kind]} {thresholdText(r)}
                  </td>
                  <td className="hidden py-2 pr-3 md:table-cell">{r.for_seconds ? duration(r.for_seconds) : "immediately"}</td>
                  <td className="hidden py-2 pr-3 md:table-cell">{scope(r)}</td>
                  <td className="py-2 pr-3">
                    <SeverityBadge severity={r.severity} />
                  </td>
                  <td className="py-2 pr-3">{r.enabled ? <Badge tone="ok">Enabled</Badge> : <Badge>Disabled</Badge>}</td>
                  <td className="py-2 text-right">
                    {isAdmin && (
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setEdit(r)}>
                          Edit
                        </Button>
                        {!r.system && (
                          <Button size="sm" variant="ghost" className="text-crit" onClick={() => confirm(`Delete rule "${r.name}"? Rules with past incidents are disabled instead.`) && del.mutate(r.id)}>
                            Delete
                          </Button>
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <RuleForm key={edit === "new" ? "new" : (edit?.id ?? "none")} rule={edit} onClose={() => setEdit(null)} groups={groups.data?.items ?? []} servers={servers.data?.items ?? []} />
    </Card>
  );
}

function RuleForm({ rule, onClose, groups, servers }: { rule: Rule | "new" | null; onClose: () => void; groups: { id: string; name: string }[]; servers: { id: string; name: string }[] }) {
  const isNew = rule === "new";
  const blank = { name: "", kind: "cpu", threshold: "90", for_seconds: "300", severity: "warning", scope: "", enabled: true, repeat_after_s: "14400" };
  const [fields, setFields] = useState<Record<string, string>>({});
  const [v, setV] = useState(() =>
    rule && rule !== "new"
      ? {
        name: rule.name,
        kind: rule.kind,
        threshold: String(rule.threshold),
        for_seconds: String(rule.for_seconds),
        severity: rule.severity,
        scope: rule.scope_server_id ? `s:${rule.scope_server_id}` : rule.scope_group_id ? `g:${rule.scope_group_id}` : "",
        enabled: rule.enabled,
          repeat_after_s: String(rule.repeat_after_s),
        }
      : blank,
  );
  const save = useWrite(
    () => {
      const body = {
        name: v.name,
        kind: v.kind,
        threshold: Number(v.threshold),
        for_seconds: Number(v.for_seconds),
        severity: v.severity,
        scope_server_id: v.scope.startsWith("s:") ? v.scope.slice(2) : null,
        scope_group_id: v.scope.startsWith("g:") ? v.scope.slice(2) : null,
        enabled: v.enabled,
        repeat_after_s: Number(v.repeat_after_s),
      };
      return isNew ? api("/alert-rules", { method: "POST", body }) : api(`/alert-rules/${(rule as Rule).id}`, { method: "PATCH", body });
    },
    [["rules"], ["alerts"]],
  );
  const kind = v.kind;
  const fleetOnly = kind === "cost_increase" || kind === "monitoring";
  const thresholdLabel = kind === "disk_forecast" ? "Hours until full" : kind === "cost_increase" ? "Increase over 3-month average (%)" : "Threshold (%)";
  return (
    <Dialog
      open={rule !== null}
      onClose={onClose}
      title={isNew ? "New alert rule" : "Edit alert rule"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="rule-form" disabled={save.isPending}>
            Save rule
          </Button>
        </>
      }
    >
      <form
        id="rule-form"
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(undefined, { onSuccess: onClose, onError: (err) => setFields(isApiError(err) ? { ...err.fields, _: err.message } : { _: "Save failed." }) });
        }}
      >
        {fields._ && <p role="alert" className="text-sm text-crit">{fields._}</p>}
        <Field label="Name" error={fields.name}>
          {(id) => <Input id={id} value={v.name} onChange={(e) => setV({ ...v, name: e.target.value })} />}
        </Field>
        <Field label="Condition" error={fields.kind} hint={isNew ? undefined : "The condition type cannot change; create a new rule instead."}>
          {(id) => (
            <Select id={id} value={v.kind} disabled={!isNew} onChange={(e) => setV({ ...v, kind: e.target.value })}>
              {Object.entries(KIND_LABEL)
                .filter(([k]) => k !== "monitoring" || !isNew)
                .map(([k, l]) => (
                  <option key={k} value={k}>
                    {l}
                  </option>
                ))}
            </Select>
          )}
        </Field>
        {kind !== "unreachable" && kind !== "monitoring" && (
          <Field label={thresholdLabel} error={fields.threshold}>
            {(id) => <Input id={id} type="number" value={v.threshold} onChange={(e) => setV({ ...v, threshold: e.target.value })} />}
          </Field>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label="Must hold for (seconds)" error={fields.for_seconds} hint="0 = fire immediately">
            {(id) => <Input id={id} type="number" min={0} value={v.for_seconds} onChange={(e) => setV({ ...v, for_seconds: e.target.value })} />}
          </Field>
          <Field label="Remind every (seconds)" error={fields.repeat_after_s} hint="While firing and not acknowledged; 0 = never">
            {(id) => <Input id={id} type="number" min={0} value={v.repeat_after_s} onChange={(e) => setV({ ...v, repeat_after_s: e.target.value })} />}
          </Field>
        </div>
        <Field label="Severity" error={fields.severity}>
          {(id) => (
            <Select id={id} value={v.severity} onChange={(e) => setV({ ...v, severity: e.target.value })}>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </Select>
          )}
        </Field>
        {!fleetOnly && (
          <Field label="Applies to" error={fields.scope}>
            {(id) => (
              <Select id={id} value={v.scope} onChange={(e) => setV({ ...v, scope: e.target.value })}>
                <option value="">Whole fleet</option>
                <optgroup label="Group">
                  {groups.map((g) => (
                    <option key={g.id} value={`g:${g.id}`}>
                      {g.name}
                    </option>
                  ))}
                </optgroup>
                <optgroup label="Server">
                  {servers.map((s) => (
                    <option key={s.id} value={`s:${s.id}`}>
                      {s.name}
                    </option>
                  ))}
                </optgroup>
              </Select>
            )}
          </Field>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={v.enabled} onChange={(e) => setV({ ...v, enabled: e.target.checked })} /> Enabled
        </label>
      </form>
    </Dialog>
  );
}
