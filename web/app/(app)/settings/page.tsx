"use client";

import { Suspense, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { EmptyState, ErrorState, LoadingBlock, NoPermission, PageHeader } from "@/components/states";
import { Badge, Button, Card, Drawer, Field, Input, Pagination, Select, Tabs } from "@/components/ui";
import { api, isApiError } from "@/lib/api";
import { ago, date, dateTime, money } from "@/lib/format";
import { useGroups, useRole, useWrite } from "@/lib/queries";

type Tab = "pricing" | "groups" | "notifications" | "users" | "general" | "audit" | "account";

export default function SettingsPage() {
  return (
    <Suspense fallback={<LoadingBlock rows={6} />}>
      <Settings />
    </Suspense>
  );
}

function Settings() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const { isAdmin } = useRole();
  const tab = (params.get("tab") as Tab) || (isAdmin ? "pricing" : "account");
  const tabs: { id: Tab; label: string }[] = [
    { id: "pricing", label: "Pricing" },
    { id: "groups", label: "Groups" },
    { id: "notifications", label: "Notifications" },
    { id: "users", label: "Users" },
    { id: "general", label: "General" },
    { id: "audit", label: "Audit log" },
    { id: "account", label: "My account" },
  ];
  const adminOnly: Tab[] = ["notifications", "users", "audit"];
  return (
    <>
      <PageHeader title="Settings" />
      <Tabs label="Settings sections" tabs={tabs} value={tab} onChange={(t) => router.replace(`${pathname}?tab=${t}`, { scroll: false })} />
      <div className="mt-5">
        {adminOnly.includes(tab) && !isAdmin ? (
          <NoPermission />
        ) : tab === "pricing" ? (
          <Pricing />
        ) : tab === "groups" ? (
          <Groups />
        ) : tab === "notifications" ? (
          <Notifications />
        ) : tab === "users" ? (
          <Users />
        ) : tab === "general" ? (
          <General />
        ) : tab === "audit" ? (
          <Audit />
        ) : (
          <Account />
        )}
      </div>
    </>
  );
}

function FormError({ msg }: { msg?: string }) {
  return msg ? (
    <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
      {msg}
    </p>
  ) : null;
}

function errs(e: unknown): Record<string, string> {
  return isApiError(e) ? { ...e.fields, _: e.message } : { _: "Something went wrong." };
}

// ---------------- pricing ----------------

type Plan = { id: string; name: string; description: string | null; currency: string; amount: number; unit: string; category: string; archived: boolean; servers_assigned: number; locked: boolean };
type Fixed = {
  id: string;
  name: string;
  currency: string;
  amount: number;
  unit: string;
  category: string;
  server_id: string | null;
  server_name: string | null;
  group_id: string | null;
  group_name: string | null;
  allocation: string;
  effective_from: string;
  effective_to: string | null;
};
const CATEGORIES = ["compute", "storage", "network", "license", "fixed"];

function Pricing() {
  const { isAdmin } = useRole();
  const plans = useQuery({ queryKey: ["plans"], queryFn: () => api<{ items: Plan[]; currency: string }>("/pricing-plans") });
  const fixed = useQuery({ queryKey: ["fixed"], queryFn: () => api<{ items: Fixed[]; currency: string }>("/fixed-costs") });
  const [planEdit, setPlanEdit] = useState<Plan | "new" | null>(null);
  const [fixedEdit, setFixedEdit] = useState<Fixed | "new" | null>(null);
  const cur = plans.data?.currency ?? "USD";
  const delFixed = useWrite((id: string) => api(`/fixed-costs/${id}`, { method: "DELETE" }), [["fixed"], ["costs"], ["cost-trend"], ["fleet"]]);
  return (
    <div className="flex flex-col gap-5">
      <Card title="Pricing plans" actions={isAdmin && <Button size="sm" variant="primary" onClick={() => setPlanEdit("new")}>+ New plan</Button>}>
        <p className="mb-3 text-sm text-foreground-muted">
          A plan is a price per hour or per month in {cur}. Assign a plan to a server on its page, from a date. Once a plan has been assigned its price is fixed,
          so past months never change: to change a price, create a new plan and assign it from the change date.
        </p>
        {plans.error ? (
          <ErrorState error={plans.error} onRetry={() => plans.refetch()} />
        ) : !plans.data ? (
          <LoadingBlock rows={3} />
        ) : plans.data.items.length === 0 ? (
          <EmptyState title="No pricing plans yet">Until you add one, cost pages show “Pricing not configured”.</EmptyState>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 pr-3 font-medium">Plan</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Price</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium sm:table-cell">Category</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Servers</th>
                <th scope="col" className="py-1.5"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {plans.data.items.map((p) => (
                <tr key={p.id} className={p.archived ? "opacity-60" : ""}>
                  <td className="py-2 pr-3">
                    {p.name} {p.archived && <Badge>archived</Badge>} {p.locked && <Badge>price locked</Badge>}
                  </td>
                  <td className="py-2 pr-3 font-mono">
                    {money(p.amount, p.currency)}/{p.unit}
                  </td>
                  <td className="hidden py-2 pr-3 sm:table-cell">{p.category}</td>
                  <td className="py-2 pr-3">{p.servers_assigned}</td>
                  <td className="py-2 text-right">
                    {isAdmin && (
                      <Button size="sm" variant="ghost" onClick={() => setPlanEdit(p)}>
                        Edit
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card title="Fixed costs" actions={isAdmin && <Button size="sm" variant="primary" onClick={() => setFixedEdit("new")}>+ New fixed cost</Button>}>
        <p className="mb-3 text-sm text-foreground-muted">
          Costs not covered by a server&apos;s plan: rack space, a bandwidth contract, licences, an extra IP. They belong to one server, a group, or the whole fleet,
          and are spread over servers only if you choose an allocation rule.
        </p>
        {fixed.error ? (
          <ErrorState error={fixed.error} onRetry={() => fixed.refetch()} />
        ) : !fixed.data ? (
          <LoadingBlock rows={3} />
        ) : fixed.data.items.length === 0 ? (
          <EmptyState title="No fixed costs" />
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 pr-3 font-medium">Name</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Amount</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Scope</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Period</th>
                <th scope="col" className="py-1.5"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {fixed.data.items.map((f) => (
                <tr key={f.id}>
                  <td className="py-2 pr-3">
                    {f.name} <span className="text-xs text-foreground-muted">({f.category})</span>
                  </td>
                  <td className="py-2 pr-3 font-mono">
                    {money(f.amount, f.currency)}/{f.unit}
                  </td>
                  <td className="hidden py-2 pr-3 md:table-cell">
                    {f.server_name ? `Server ${f.server_name}` : f.group_name ? `Group ${f.group_name}` : "Fleet"}
                    {f.allocation !== "none" && <span className="text-xs text-foreground-muted"> · split {f.allocation === "cores" ? "by cores" : "equally"}</span>}
                  </td>
                  <td className="hidden py-2 pr-3 text-xs md:table-cell">
                    {date(f.effective_from)} → {f.effective_to ? date(f.effective_to) : "ongoing"}
                  </td>
                  <td className="py-2 text-right">
                    {isAdmin && (
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setFixedEdit(f)}>
                          Edit
                        </Button>
                        <Button size="sm" variant="ghost" className="text-crit" onClick={() => confirm(`Delete fixed cost "${f.name}"?`) && delFixed.mutate(f.id)}>
                          Delete
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {isAdmin && <InvoiceImport />}
      <PlanForm key={planEdit === "new" ? "new" : (planEdit?.id ?? "none")} plan={planEdit} onClose={() => setPlanEdit(null)} currency={cur} />
      <FixedForm key={fixedEdit === "new" ? "new" : (fixedEdit?.id ?? "none")} item={fixedEdit} onClose={() => setFixedEdit(null)} currency={cur} />
    </div>
  );
}

function PlanForm({ plan, onClose, currency }: { plan: Plan | "new" | null; onClose: () => void; currency: string }) {
  const isNew = plan === "new";
  const [v, setV] = useState(() =>
    plan && plan !== "new"
      ? { name: plan.name, description: plan.description ?? "", amount: String(plan.amount), unit: plan.unit, category: plan.category, archived: plan.archived }
      : { name: "", description: "", amount: "", unit: "month", category: "compute", archived: false },
  );
  const [e, setE] = useState<Record<string, string>>({});
  const locked = plan && plan !== "new" && plan.locked;
  const save = useWrite(
    () => {
      const body: Record<string, unknown> = { name: v.name, description: v.description, archived: v.archived };
      if (!locked) Object.assign(body, { amount: Number(v.amount), unit: v.unit, category: v.category });
      return isNew ? api("/pricing-plans", { method: "POST", body }) : api(`/pricing-plans/${(plan as Plan).id}`, { method: "PATCH", body });
    },
    [["plans"], ["costs"], ["fleet"], ["cost-trend"]],
  );
  return (
    <Drawer
      open={plan !== null}
      onClose={onClose}
      title={isNew ? "New pricing plan" : "Edit pricing plan"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="plan-form" disabled={save.isPending}>
            Save
          </Button>
        </>
      }
    >
      <form id="plan-form" className="flex flex-col gap-4" onSubmit={(ev) => (ev.preventDefault(), save.mutate(undefined, { onSuccess: onClose, onError: (x) => setE(errs(x)) }))}>
        <FormError msg={e._} />
        <Field label="Name" error={e.name} hint="e.g. OVH Advance-2 monthly">
          {(id) => <Input id={id} value={v.name} onChange={(x) => setV({ ...v, name: x.target.value })} />}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={`Price (${currency})`} error={e.amount} hint={locked ? "Locked: plan is assigned" : undefined}>
            {(id) => <Input id={id} type="number" step="0.0001" min={0} value={v.amount} disabled={!!locked} onChange={(x) => setV({ ...v, amount: x.target.value })} />}
          </Field>
          <Field label="Per" error={e.unit}>
            {(id) => (
              <Select id={id} value={v.unit} disabled={!!locked} onChange={(x) => setV({ ...v, unit: x.target.value })}>
                <option value="month">month</option>
                <option value="hour">hour</option>
              </Select>
            )}
          </Field>
        </div>
        <Field label="Category" error={e.category}>
          {(id) => (
            <Select id={id} value={v.category} disabled={!!locked} onChange={(x) => setV({ ...v, category: x.target.value })}>
              {CATEGORIES.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Description" error={e.description}>
          {(id) => <Input id={id} value={v.description} onChange={(x) => setV({ ...v, description: x.target.value })} />}
        </Field>
        {!isNew && (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={v.archived} onChange={(x) => setV({ ...v, archived: x.target.checked })} /> Archived (cannot be assigned anymore)
          </label>
        )}
      </form>
    </Drawer>
  );
}

function FixedForm({ item, onClose, currency }: { item: Fixed | "new" | null; onClose: () => void; currency: string }) {
  const isNew = item === "new";
  const groups = useGroups();
  const servers = useQuery({ queryKey: ["server-names"], queryFn: () => api<{ items: { id: string; name: string }[] }>("/servers?page_size=100") });
  const today = new Date().toISOString().slice(0, 10);
  const blank = { name: "", amount: "", unit: "month", category: "fixed", scope: "", allocation: "none", from: today, to: "" };
  const [e, setE] = useState<Record<string, string>>({});
  const [v, setV] = useState(() =>
    item && item !== "new"
      ? {
        name: item.name,
        amount: String(item.amount),
        unit: item.unit,
        category: item.category,
        scope: item.server_id ? `s:${item.server_id}` : item.group_id ? `g:${item.group_id}` : "",
        allocation: item.allocation,
        from: item.effective_from.slice(0, 10),
          to: item.effective_to?.slice(0, 10) ?? "",
        }
      : blank,
  );
  const save = useWrite(
    () => {
      const body = {
        name: v.name,
        amount: Number(v.amount),
        unit: v.unit,
        category: v.category,
        server_id: v.scope.startsWith("s:") ? v.scope.slice(2) : null,
        group_id: v.scope.startsWith("g:") ? v.scope.slice(2) : null,
        allocation: v.scope.startsWith("s:") ? "none" : v.allocation,
        effective_from: v.from,
        effective_to: v.to || null,
      };
      return isNew ? api("/fixed-costs", { method: "POST", body }) : api(`/fixed-costs/${(item as Fixed).id}`, { method: "PATCH", body });
    },
    [["fixed"], ["costs"], ["fleet"], ["cost-trend"]],
  );
  return (
    <Drawer
      open={item !== null}
      onClose={onClose}
      title={isNew ? "New fixed cost" : "Edit fixed cost"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="fixed-form" disabled={save.isPending}>
            Save
          </Button>
        </>
      }
    >
      <form id="fixed-form" className="flex flex-col gap-4" onSubmit={(ev) => (ev.preventDefault(), save.mutate(undefined, { onSuccess: onClose, onError: (x) => setE(errs(x)) }))}>
        <FormError msg={e._} />
        <Field label="Name" error={e.name} hint="e.g. Rack space DC1, 10 Gbit contract">
          {(id) => <Input id={id} value={v.name} onChange={(x) => setV({ ...v, name: x.target.value })} />}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={`Amount (${currency})`} error={e.amount}>
            {(id) => <Input id={id} type="number" step="0.01" min={0} value={v.amount} onChange={(x) => setV({ ...v, amount: x.target.value })} />}
          </Field>
          <Field label="Per" error={e.unit}>
            {(id) => (
              <Select id={id} value={v.unit} onChange={(x) => setV({ ...v, unit: x.target.value })}>
                <option value="month">month</option>
                <option value="year">year</option>
              </Select>
            )}
          </Field>
        </div>
        <Field label="Category" error={e.category}>
          {(id) => (
            <Select id={id} value={v.category} onChange={(x) => setV({ ...v, category: x.target.value })}>
              {CATEGORIES.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Belongs to" error={e.scope}>
          {(id) => (
            <Select id={id} value={v.scope} onChange={(x) => setV({ ...v, scope: x.target.value })}>
              <option value="">Whole fleet</option>
              <optgroup label="Group">
                {groups.data?.items.map((g) => (
                  <option key={g.id} value={`g:${g.id}`}>
                    {g.name}
                  </option>
                ))}
              </optgroup>
              <optgroup label="Server">
                {servers.data?.items.map((s) => (
                  <option key={s.id} value={`s:${s.id}`}>
                    {s.name}
                  </option>
                ))}
              </optgroup>
            </Select>
          )}
        </Field>
        {!v.scope.startsWith("s:") && (
          <Field label="Spread over servers" error={e.allocation} hint="Leave as 'not allocated' to show it as a shared cost.">
            {(id) => (
              <Select id={id} value={v.allocation} onChange={(x) => setV({ ...v, allocation: x.target.value })}>
                <option value="none">Not allocated</option>
                <option value="equal">Equal split</option>
                <option value="cores">By CPU cores</option>
              </Select>
            )}
          </Field>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label="From" error={e.effective_from}>
            {(id) => <Input id={id} type="date" value={v.from} onChange={(x) => setV({ ...v, from: x.target.value })} />}
          </Field>
          <Field label="Until (optional)" error={e.effective_to}>
            {(id) => <Input id={id} type="date" value={v.to} onChange={(x) => setV({ ...v, to: x.target.value })} />}
          </Field>
        </div>
      </form>
    </Drawer>
  );
}

function InvoiceImport() {
  const [file, setFile] = useState<File | null>(null);
  const [result, setResult] = useState<{ imported: number; errors: string[] } | null>(null);
  const [error, setError] = useState("");
  const up = useWrite(
    async () => {
      const fd = new FormData();
      fd.append("file", file!);
      return api<{ imported: number; errors: string[] }>("/costs/import", { method: "POST", form: fd });
    },
    [["costs"], ["cost-trend"]],
  );
  return (
    <Card title="Import actual invoices (CSV)">
      <p className="mb-3 text-sm text-foreground-muted">
        Columns: <code className="font-mono">server,period,amount</code> (and optionally <code className="font-mono">currency</code>). Server is the name or id, period is YYYY-MM.
        The whole file is rejected if any row is invalid. Imported amounts appear as <Badge tone="ok">Actual</Badge> beside the estimates.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor="csv" className="sr-only">
          CSV file
        </label>
        <input id="csv" type="file" accept=".csv,text/csv" onChange={(e) => (setFile(e.target.files?.[0] ?? null), setResult(null), setError(""))} className="text-sm" />
        <Button
          variant="primary"
          size="sm"
          disabled={!file || up.isPending}
          onClick={() =>
            up.mutate(undefined, {
              onSuccess: (r) => setResult(r),
              onError: (e) => {
                if (isApiError(e) && e.status === 422) setError("The file has errors; nothing was imported.");
                else setError(isApiError(e) ? e.message : "Upload failed.");
              },
            })
          }
        >
          Upload
        </Button>
      </div>
      {result && <p className="mt-3 text-sm text-ok">● Imported {result.imported} row(s).</p>}
      {error && <p className="mt-3 text-sm text-crit">{error}</p>}
    </Card>
  );
}

// ---------------- groups ----------------

function Groups() {
  const { isOperator, isAdmin } = useRole();
  const g = useGroups();
  const [name, setName] = useState("");
  const [e, setE] = useState<Record<string, string>>({});
  const create = useWrite(() => api("/groups", { method: "POST", body: { name } }), [["groups"], ["servers"]]);
  const del = useWrite((id: string) => api(`/groups/${id}`, { method: "DELETE" }), [["groups"], ["servers"]]);
  return (
    <Card title="Server groups">
      {isOperator && (
        <form
          className="mb-4 flex flex-wrap items-end gap-2"
          onSubmit={(ev) => {
            ev.preventDefault();
            setE({});
            create.mutate(undefined, { onSuccess: () => setName(""), onError: (x) => setE(errs(x)) });
          }}
        >
          <div className="w-64">
            <Field label="New group" error={e.name ?? (e._ && !e.name ? e._ : undefined)}>
              {(id) => <Input id={id} value={name} onChange={(x) => setName(x.target.value)} placeholder="e.g. control-plane" />}
            </Field>
          </div>
          <Button type="submit" variant="primary" disabled={!name.trim()}>
            Add group
          </Button>
        </form>
      )}
      {!g.data ? (
        <LoadingBlock rows={3} />
      ) : g.data.items.length === 0 ? (
        <EmptyState title="No groups yet">Groups organise servers for filtering, cost breakdowns and rule scopes.</EmptyState>
      ) : (
        <ul className="divide-y divide-border text-sm">
          {g.data.items.map((x) => (
            <li key={x.id} className="flex items-center justify-between py-2">
              <span>
                {x.name} <span className="text-foreground-muted">· {x.server_count} server(s)</span>
              </span>
              {isAdmin && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-crit"
                  onClick={() =>
                    confirm(`Delete group "${x.name}"? Its servers stay, without a group.`) &&
                    del.mutate(x.id, { onError: (err) => alert(isApiError(err) ? err.message : "Delete failed.") })
                  }
                >
                  Delete
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

// ---------------- notifications ----------------

type Channel = { id: string; kind: string; name: string; config: { to?: string[]; chat_id?: string }; min_severity: string; enabled: boolean; configured: boolean; last_sent_at: string | null; last_error: string | null };

function Notifications() {
  const q = useQuery({ queryKey: ["channels"], queryFn: () => api<{ items: Channel[]; available: Record<string, boolean> }>("/notification-channels") });
  const [edit, setEdit] = useState<Channel | "new" | null>(null);
  const [test, setTest] = useState<Record<string, string>>({});
  const runTest = async (c: Channel) => {
    setTest({ ...test, [c.id]: "Sending…" });
    try {
      const r = await api<{ sent: boolean; error?: string }>(`/notification-channels/${c.id}/test`, { method: "POST" });
      setTest((t) => ({ ...t, [c.id]: r.sent ? "● Test message sent." : `✕ ${r.error}` }));
    } catch (e) {
      setTest((t) => ({ ...t, [c.id]: `✕ ${isApiError(e) ? e.message : "Failed"}` }));
    }
  };
  return (
    <Card title="Where alerts are sent" actions={<Button size="sm" variant="primary" onClick={() => setEdit("new")}>+ New channel</Button>}>
      <p className="mb-3 text-sm text-foreground-muted">
        Secrets (SMTP password, webhook URLs, bot tokens) live only in the server&apos;s environment. A channel here holds the addresses and the minimum severity.
      </p>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap gap-2 text-xs">
            {Object.entries(q.data.available).map(([k, ok]) => (
              <Badge key={k} tone={ok ? "ok" : "muted"}>
                {ok ? "●" : "○"} {k} {ok ? "configured" : "not configured in env"}
              </Badge>
            ))}
          </div>
          {q.data.items.length === 0 ? (
            <EmptyState title="No channels yet">Alerts are recorded but nobody is notified until you add a channel.</EmptyState>
          ) : (
            <ul className="divide-y divide-border">
              {q.data.items.map((c) => (
                <li key={c.id} className="flex flex-wrap items-center gap-3 py-3 text-sm">
                  <div className="min-w-0 flex-1">
                    <p className="font-medium">
                      {c.name} <Badge>{c.kind}</Badge> {!c.enabled && <Badge>disabled</Badge>} {!c.configured && <Badge tone="warn">⚠ env not set</Badge>}
                    </p>
                    <p className="mt-0.5 text-xs text-foreground-muted">
                      {c.config.to?.join(", ")} · {c.min_severity} and above · last sent {c.last_sent_at ? ago(c.last_sent_at) : "never"}
                      {c.last_error && <span className="text-crit"> · last error: {c.last_error}</span>}
                    </p>
                    {test[c.id] && <p className="mt-1 text-xs" role="status">{test[c.id]}</p>}
                  </div>
                  <Button size="sm" onClick={() => runTest(c)} disabled={!c.configured}>
                    Send test
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(c)}>
                    Edit
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <ChannelForm key={edit === "new" ? "new" : (edit?.id ?? "none")} channel={edit} onClose={() => setEdit(null)} />
    </Card>
  );
}

function ChannelForm({ channel, onClose }: { channel: Channel | "new" | null; onClose: () => void }) {
  const isNew = channel === "new";
  const [v, setV] = useState(() =>
    channel && channel !== "new"
      ? { kind: channel.kind, name: channel.name, to: channel.config.to?.join(", ") ?? "", chat_id: channel.config.chat_id ?? "", min_severity: channel.min_severity, enabled: channel.enabled }
      : { kind: "email", name: "", to: "", chat_id: "", min_severity: "warning", enabled: true },
  );
  const [e, setE] = useState<Record<string, string>>({});
  const save = useWrite(
    () => {
      const body = {
        kind: v.kind,
        name: v.name,
        min_severity: v.min_severity,
        enabled: v.enabled,
        config: { to: v.kind === "email" ? v.to.split(",").map((s) => s.trim()).filter(Boolean) : undefined, chat_id: v.kind === "telegram" ? v.chat_id : undefined },
      };
      return isNew ? api("/notification-channels", { method: "POST", body }) : api(`/notification-channels/${(channel as Channel).id}`, { method: "PATCH", body });
    },
    [["channels"]],
  );
  return (
    <Drawer
      open={channel !== null}
      onClose={onClose}
      title={isNew ? "New notification channel" : "Edit channel"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="channel-form" disabled={save.isPending}>
            Save
          </Button>
        </>
      }
    >
      <form id="channel-form" className="flex flex-col gap-4" onSubmit={(ev) => (ev.preventDefault(), save.mutate(undefined, { onSuccess: onClose, onError: (x) => setE(errs(x)) }))}>
        <FormError msg={e._} />
        <Field label="Type" error={e.kind}>
          {(id) => (
            <Select id={id} value={v.kind} disabled={!isNew} onChange={(x) => setV({ ...v, kind: x.target.value })}>
              <option value="email">Email</option>
              <option value="slack">Slack</option>
              <option value="telegram">Telegram</option>
              <option value="discord">Discord</option>
            </Select>
          )}
        </Field>
        <Field label="Name" error={e.name}>
          {(id) => <Input id={id} value={v.name} onChange={(x) => setV({ ...v, name: x.target.value })} placeholder="e.g. Ops email" />}
        </Field>
        {v.kind === "email" && (
          <Field label="Recipients" error={e["config.to"]} hint="Comma-separated email addresses">
            {(id) => <Input id={id} value={v.to} onChange={(x) => setV({ ...v, to: x.target.value })} placeholder="ops@example.com" />}
          </Field>
        )}
        {v.kind === "telegram" && (
          <Field label="Chat id (optional)" hint="Overrides TELEGRAM_CHAT_ID">
            {(id) => <Input id={id} value={v.chat_id} onChange={(x) => setV({ ...v, chat_id: x.target.value })} />}
          </Field>
        )}
        <Field label="Send alerts of severity" error={e.min_severity}>
          {(id) => (
            <Select id={id} value={v.min_severity} onChange={(x) => setV({ ...v, min_severity: x.target.value })}>
              <option value="info">Info and above</option>
              <option value="warning">Warning and above</option>
              <option value="critical">Critical only</option>
            </Select>
          )}
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={v.enabled} onChange={(x) => setV({ ...v, enabled: x.target.checked })} /> Enabled
        </label>
      </form>
    </Drawer>
  );
}

// ---------------- users ----------------

type Account = { id: string; email: string; name: string; role: string; disabled: boolean; has_password: boolean; locked_until: string | null; last_login_at: string | null; created_at: string };

function Users() {
  const q = useQuery({ queryKey: ["users"], queryFn: () => api<{ items: Account[]; sso_enabled: boolean }>("/users") });
  const [adding, setAdding] = useState(false);
  const [v, setV] = useState({ email: "", name: "", role: "viewer", password: "" });
  const [e, setE] = useState<Record<string, string>>({});
  const create = useWrite(() => api("/users", { method: "POST", body: v }), [["users"]]);
  const patch = useWrite(({ id, body }: { id: string; body: Record<string, unknown> }) => api(`/users/${id}`, { method: "PATCH", body }), [["users"]]);
  const onErr = (x: unknown) => alert(isApiError(x) ? Object.values(x.fields)[0] ?? x.message : "Failed.");
  return (
    <Card title="Users and roles" actions={<Button size="sm" variant="primary" onClick={() => setAdding(true)}>+ Add user</Button>}>
      <p className="mb-3 text-sm text-foreground-muted">
        <strong>Viewer</strong> sees everything. <strong>Operator</strong> also adds, edits, enables and disables servers and acknowledges alerts. <strong>Admin</strong> also deletes servers,
        manages pricing, rules, channels, users and settings.
        {q.data?.sso_enabled ? " Google sign-in is on: a user without a password signs in with Google only." : ""}
      </p>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-foreground-muted">
              <tr>
                <th scope="col" className="py-1.5 pr-3 font-medium">User</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">Role</th>
                <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Last sign-in</th>
                <th scope="col" className="py-1.5 pr-3 font-medium">State</th>
                <th scope="col" className="py-1.5"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {q.data.items.map((u) => (
                <tr key={u.id}>
                  <td className="py-2 pr-3">
                    {u.name}
                    <div className="text-xs text-foreground-muted">
                      {u.email}
                      {!u.has_password && " · Google only"}
                    </div>
                  </td>
                  <td className="py-2 pr-3">
                    <Select aria-label={`Role of ${u.email}`} value={u.role} onChange={(x) => patch.mutate({ id: u.id, body: { role: x.target.value } }, { onError: onErr })} className="h-8 w-auto">
                      <option value="viewer">viewer</option>
                      <option value="operator">operator</option>
                      <option value="admin">admin</option>
                    </Select>
                  </td>
                  <td className="hidden py-2 pr-3 text-xs md:table-cell">{u.last_login_at ? dateTime(u.last_login_at) : "never"}</td>
                  <td className="py-2 pr-3">
                    {u.disabled ? <Badge>disabled</Badge> : u.locked_until && new Date(u.locked_until) > new Date() ? <Badge tone="warn">locked</Badge> : <Badge tone="ok">active</Badge>}
                  </td>
                  <td className="py-2 text-right">
                    <div className="flex justify-end gap-1">
                      {u.locked_until && new Date(u.locked_until) > new Date() && (
                        <Button size="sm" variant="ghost" onClick={() => patch.mutate({ id: u.id, body: { unlock: true } }, { onError: onErr })}>
                          Unlock
                        </Button>
                      )}
                      <Button size="sm" variant="ghost" onClick={() => patch.mutate({ id: u.id, body: { disabled: !u.disabled } }, { onError: onErr })}>
                        {u.disabled ? "Enable" : "Disable"}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          const p = prompt(`New password for ${u.email} (at least 12 characters). Their sessions will end.`);
                          if (p) patch.mutate({ id: u.id, body: { password: p } }, { onError: onErr });
                        }}
                      >
                        Reset password
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Drawer
        open={adding}
        onClose={() => setAdding(false)}
        title="Add user"
        footer={
          <>
            <Button onClick={() => setAdding(false)}>Cancel</Button>
            <Button variant="primary" type="submit" form="user-form" disabled={create.isPending}>
              Add user
            </Button>
          </>
        }
      >
        <form
          id="user-form"
          className="flex flex-col gap-4"
          onSubmit={(ev) => {
            ev.preventDefault();
            setE({});
            create.mutate(undefined, { onSuccess: () => (setAdding(false), setV({ email: "", name: "", role: "viewer", password: "" })), onError: (x) => setE(errs(x)) });
          }}
        >
          <FormError msg={e._} />
          <Field label="Email" error={e.email}>
            {(id) => <Input id={id} type="email" value={v.email} onChange={(x) => setV({ ...v, email: x.target.value })} />}
          </Field>
          <Field label="Name" error={e.name}>
            {(id) => <Input id={id} value={v.name} onChange={(x) => setV({ ...v, name: x.target.value })} />}
          </Field>
          <Field label="Role" error={e.role}>
            {(id) => (
              <Select id={id} value={v.role} onChange={(x) => setV({ ...v, role: x.target.value })}>
                <option value="viewer">Viewer</option>
                <option value="operator">Operator</option>
                <option value="admin">Admin</option>
              </Select>
            )}
          </Field>
          <Field label="Initial password" error={e.password} hint={q.data?.sso_enabled ? "Leave empty for a Google-only account" : "At least 12 characters"}>
            {(id) => <Input id={id} type="password" autoComplete="new-password" value={v.password} onChange={(x) => setV({ ...v, password: x.target.value })} />}
          </Field>
        </form>
      </Drawer>
    </Card>
  );
}

// ---------------- general ----------------

type SettingsResp = { values: Record<string, unknown>; editable: string[]; environment: Record<string, unknown> };

function General() {
  const q = useQuery({ queryKey: ["settings"], queryFn: () => api<SettingsResp>("/settings") });
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />;
  if (!q.data) return <LoadingBlock rows={4} />;
  return <GeneralForm key={q.dataUpdatedAt} data={q.data} />;
}

function GeneralForm({ data }: { data: SettingsResp }) {
  const { isAdmin } = useRole();
  const [e, setE] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState(false);
  const [v, setV] = useState<Record<string, string | boolean>>(() => {
    const s = data.values;
    return {
      proration: (s.proration as { enabled?: boolean })?.enabled ?? true,
      underutil_window_days: String(s.underutil_window_days ?? 14),
      underutil_cpu_p95: String(s.underutil_cpu_p95 ?? 10),
      underutil_mem_p95: String(s.underutil_mem_p95 ?? 30),
      underutil_min_coverage: String(s.underutil_min_coverage ?? 80),
    };
  });
  const save = useWrite(
    () =>
      api("/settings", {
        method: "PUT",
        body: {
          proration: { enabled: v.proration },
          underutil_window_days: Number(v.underutil_window_days),
          underutil_cpu_p95: Number(v.underutil_cpu_p95),
          underutil_mem_p95: Number(v.underutil_mem_p95),
          underutil_min_coverage: Number(v.underutil_min_coverage),
        },
      }),
    [["settings"], ["costs"], ["underutilized"], ["fleet"], ["cost-trend"]],
  );
  const env = data.environment;
  const num = (k: string, label: string, hint: string) => (
    <Field label={label} error={e[k]} hint={hint}>
      {(id) => <Input id={id} type="number" disabled={!isAdmin} value={String(v[k] ?? "")} onChange={(x) => setV({ ...v, [k]: x.target.value })} />}
    </Field>
  );
  return (
    <div className="grid gap-5 lg:grid-cols-2">
      <Card title="Cost and insight rules">
        <form
          className="flex flex-col gap-4"
          onSubmit={(ev) => {
            ev.preventDefault();
            setE({});
            setSaved(false);
            save.mutate(undefined, { onSuccess: () => setSaved(true), onError: (x) => setE(errs(x)) });
          }}
        >
          <FormError msg={e._} />
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" disabled={!isAdmin} checked={!!v.proration} onChange={(x) => setV({ ...v, proration: x.target.checked })} className="mt-1" />
            <span>
              Prorate monthly plans by active hours
              <span className="block text-xs text-foreground-muted">Off: a server active on any day of a month is charged the full monthly price.</span>
            </span>
          </label>
          <div className="grid grid-cols-2 gap-3">
            {num("underutil_window_days", "Underutilisation window (days)", "1–90")}
            {num("underutil_min_coverage", "Minimum data coverage (%)", "0–100")}
            {num("underutil_cpu_p95", "p95 CPU below (%)", "1–100")}
            {num("underutil_mem_p95", "p95 memory below (%)", "1–100")}
          </div>
          {isAdmin && (
            <div className="flex items-center gap-3">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                Save
              </Button>
              {saved && <span className="text-sm text-ok" role="status">● Saved</span>}
            </div>
          )}
        </form>
      </Card>
      <Card title="Environment (set on the server)">
        <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
          {Object.entries({
            "Billing timezone": env.billing_timezone,
            "Billing currency": env.billing_currency,
            "Default scrape interval": `${env.default_scrape_interval_s}s`,
            "Allowed target networks": env.allowed_target_cidrs,
            "Allowed exporter ports": (env.allowed_target_ports as number[])?.join(", "),
            "Email alerts": env.email_configured ? "configured" : "not configured",
            "Google sign-in": env.google_sso ? "on" : "off",
          }).map(([k, val]) => (
            <div key={k}>
              <dt className="text-xs text-foreground-muted">{k}</dt>
              <dd className="font-mono text-xs">{String(val)}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-3 text-xs text-foreground-muted">These come from the .env file so they cannot drift from the deployment. Changing them needs a restart.</p>
      </Card>
    </div>
  );
}

// ---------------- audit ----------------

type AuditRow = { id: number; at: string; actor_email: string | null; action: string; target_type: string; target_id: string | null; ip: string | null; before: unknown; after: unknown };

function Audit() {
  const [page, setPage] = useState(1);
  const [action, setAction] = useState("");
  const q = useQuery({
    queryKey: ["audit", page, action],
    queryFn: () => api<{ items: AuditRow[]; page: number; page_size: number; total: number }>(`/audit?page=${page}&page_size=50${action ? `&action=${encodeURIComponent(action)}` : ""}`),
    placeholderData: (p) => p,
  });
  return (
    <Card
      title="Audit log"
      actions={
        <Select aria-label="Filter by action" value={action} onChange={(e) => (setAction(e.target.value), setPage(1))} className="h-8 w-auto">
          <option value="">All actions</option>
          {["server", "group", "pricing", "fixed_cost", "costs", "alert", "channel", "user", "settings"].map((a) => (
            <option key={a} value={a}>
              {a}.*
            </option>
          ))}
        </Select>
      }
    >
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={6} />
      ) : q.data.items.length === 0 ? (
        <EmptyState title="No audit events" />
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-foreground-muted">
                <tr>
                  <th scope="col" className="py-1.5 pr-3 font-medium">When</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Who</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Action</th>
                  <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Target</th>
                  <th scope="col" className="hidden py-1.5 font-medium lg:table-cell">From</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {q.data.items.map((r) => (
                  <tr key={r.id} className="align-top">
                    <td className="py-2 pr-3 text-xs">{dateTime(r.at)}</td>
                    <td className="py-2 pr-3 text-xs">{r.actor_email ?? "system"}</td>
                    <td className="py-2 pr-3">
                      <details>
                        <summary className="cursor-pointer font-mono text-xs">{r.action}</summary>
                        <pre className="mt-1 max-w-xl overflow-x-auto rounded bg-background p-2 text-[11px]">{JSON.stringify({ before: r.before, after: r.after }, null, 2)}</pre>
                      </details>
                    </td>
                    <td className="hidden py-2 pr-3 font-mono text-xs md:table-cell">
                      {r.target_type}
                      {r.target_id && `:${r.target_id.slice(0, 8)}`}
                    </td>
                    <td className="hidden py-2 font-mono text-xs lg:table-cell">{r.ip ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="mt-3">
            <Pagination page={q.data.page} pageSize={q.data.page_size} total={q.data.total} onPage={setPage} />
          </div>
        </>
      )}
    </Card>
  );
}

// ---------------- account ----------------

function Account() {
  const [v, setV] = useState({ current: "", next: "", confirm: "" });
  const [e, setE] = useState<Record<string, string>>({});
  const [done, setDone] = useState(false);
  const save = useWrite(() => api("/me/password", { method: "POST", body: { current_password: v.current, new_password: v.next } }), []);
  return (
    <Card title="Change password" className="max-w-lg">
      <form
        className="flex flex-col gap-4"
        onSubmit={(ev) => {
          ev.preventDefault();
          setE({});
          setDone(false);
          if (v.next !== v.confirm) return setE({ confirm: "Passwords do not match." });
          save.mutate(undefined, { onSuccess: () => (setDone(true), setV({ current: "", next: "", confirm: "" })), onError: (x) => setE(errs(x)) });
        }}
      >
        <FormError msg={e._ && !e.current_password && !e.new_password ? e._ : undefined} />
        <Field label="Current password" error={e.current_password}>
          {(id) => <Input id={id} type="password" autoComplete="current-password" value={v.current} onChange={(x) => setV({ ...v, current: x.target.value })} />}
        </Field>
        <Field label="New password" error={e.new_password} hint="At least 12 characters">
          {(id) => <Input id={id} type="password" autoComplete="new-password" value={v.next} onChange={(x) => setV({ ...v, next: x.target.value })} />}
        </Field>
        <Field label="Repeat new password" error={e.confirm}>
          {(id) => <Input id={id} type="password" autoComplete="new-password" value={v.confirm} onChange={(x) => setV({ ...v, confirm: x.target.value })} />}
        </Field>
        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" disabled={save.isPending}>
            Change password
          </Button>
          {done && <span className="text-sm text-ok" role="status">● Password changed. Other sessions were signed out.</span>}
        </div>
      </form>
    </Card>
  );
}
