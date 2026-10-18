"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, isApiError } from "@/lib/api";
import { dateTime } from "@/lib/format";
import { useRole, useWrite } from "@/lib/queries";
import { GroupPicker } from "./groups";
import { EmptyState, ErrorState, LoadingBlock } from "./states";
import { Badge, Button, Card, Dialog, Field, Input, Select, cx } from "./ui";

// People are dashboard users and alert-only contacts. An admin tags each person
// with groups; the person then gets email alerts for the servers in those groups.

type AlertGroup = { id: string; name: string; muted: boolean };

export type Person = {
  id: string;
  email: string;
  name: string;
  kind: "user" | "contact";
  role: string;
  disabled: boolean;
  has_password: boolean;
  locked_until: string | null;
  last_login_at: string | null;
  alerts_all: boolean;
  alert_min_severity: string;
  alerts_paused_until: string | null;
  alert_groups: AlertGroup[];
};

type PeopleResp = { items: Person[]; sso_enabled: boolean; email_configured: boolean };

const SEVERITY_LABEL: Record<string, string> = { info: "info and above", warning: "warning and above", critical: "critical only" };

const paused = (until: string | null) => !!until && new Date(until) > new Date();

function usePeople() {
  return useQuery({ queryKey: ["users"], queryFn: () => api<PeopleResp>("/users") });
}

const fieldErrors = (e: unknown): Record<string, string> => (isApiError(e) ? { ...e.fields, _: e.message } : { _: "Save failed." });

/** Alert groups of a person, with muted ones struck through. */
function AlertGroupChips({ p }: { p: Pick<Person, "alerts_all" | "alert_groups"> }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {p.alerts_all && <Badge tone="accent">All alerts</Badge>}
      {p.alert_groups.map((g) => (
        <Link
          key={g.id}
          href={`/groups/${g.id}`}
          title={g.muted ? "Muted by the person" : undefined}
          className={cx(
            "rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
            g.muted ? "border-border text-foreground-muted line-through" : "border-accent/30 bg-accent/10 text-accent hover:border-accent/60",
          )}
        >
          {g.name}
        </Link>
      ))}
      {!p.alerts_all && p.alert_groups.length === 0 && <span className="text-xs text-foreground-muted">no alerts</span>}
    </span>
  );
}

// ---------------- Settings → People ----------------

export function People() {
  const q = usePeople();
  const [edit, setEdit] = useState<Person | "new" | null>(null);
  const patch = useWrite(({ id, body }: { id: string; body: Record<string, unknown> }) => api(`/users/${id}`, { method: "PATCH", body }), [["users"], ["groups"]]);
  const onErr = (x: unknown) => alert(isApiError(x) ? (Object.values(x.fields)[0] ?? x.message) : "Failed.");
  const users = q.data?.items.filter((p) => p.kind === "user").length ?? 0;
  const contacts = (q.data?.items.length ?? 0) - users;
  return (
    <Card title="People" actions={<Button size="sm" variant="primary" onClick={() => setEdit("new")}>+ Add person</Button>}>
      <div className="mb-4 grid gap-2 text-sm text-foreground-muted sm:grid-cols-2">
        <p>
          <strong className="text-foreground">Dashboard users</strong> sign in. Viewer sees everything; operator also manages servers and acknowledges alerts; admin
          also manages pricing, rules, people and settings.
        </p>
        <p>
          <strong className="text-foreground">Alert-only contacts</strong> never sign in; they only get email. Everyone gets alerts for the servers in their{" "}
          <strong className="text-foreground">groups</strong>; <strong className="text-foreground">All alerts</strong> people also get fleet-wide alerts.
        </p>
      </div>
      {q.data && !q.data.email_configured && (
        <p role="status" className="mb-4 rounded-card border border-warn/50 bg-warn/10 px-3 py-2 text-sm text-warn">
          <span aria-hidden>⚠ </span>Email is not configured on the server (SMTP settings), so nobody receives alert emails yet.
        </p>
      )}
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <LoadingBlock rows={3} />
      ) : (
        <>
          <p className="mb-2 text-xs text-foreground-muted">
            {users} user{users === 1 ? "" : "s"} · {contacts} alert-only contact{contacts === 1 ? "" : "s"}
          </p>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-foreground-muted">
                <tr>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Person</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Role</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Gets alerts for</th>
                  <th scope="col" className="hidden py-1.5 pr-3 font-medium lg:table-cell">Severity</th>
                  <th scope="col" className="hidden py-1.5 pr-3 font-medium md:table-cell">Last sign-in</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">State</th>
                  <th scope="col" className="py-1.5"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {q.data.items.map((u) => {
                  const locked = !!u.locked_until && new Date(u.locked_until) > new Date();
                  return (
                    <tr key={u.id} className={cx(u.disabled && "opacity-60")}>
                      <td className="py-2 pr-3">
                        <span className="font-medium">{u.name}</span> {u.kind === "contact" && <Badge>alert-only</Badge>}
                        <div className="text-xs text-foreground-muted">
                          {u.email}
                          {u.kind === "user" && !u.has_password && " · Google only"}
                        </div>
                      </td>
                      <td className="py-2 pr-3">
                        {u.kind === "contact" ? (
                          <span className="text-xs text-foreground-muted">no sign-in</span>
                        ) : (
                          <Select aria-label={`Role of ${u.email}`} value={u.role} onChange={(x) => patch.mutate({ id: u.id, body: { role: x.target.value } }, { onError: onErr })} className="h-8 w-auto">
                            <option value="viewer">viewer</option>
                            <option value="operator">operator</option>
                            <option value="admin">admin</option>
                          </Select>
                        )}
                      </td>
                      <td className="max-w-72 py-2 pr-3">
                        <AlertGroupChips p={u} />
                      </td>
                      <td className="hidden py-2 pr-3 text-xs lg:table-cell">
                        {SEVERITY_LABEL[u.alert_min_severity]}
                        {paused(u.alerts_paused_until) && (
                          <div className="mt-0.5">
                            <Badge tone="warn">paused until {dateTime(u.alerts_paused_until!)}</Badge>
                          </div>
                        )}
                      </td>
                      <td className="hidden py-2 pr-3 text-xs md:table-cell">{u.kind === "contact" ? "—" : u.last_login_at ? dateTime(u.last_login_at) : "never"}</td>
                      <td className="py-2 pr-3">{u.disabled ? <Badge>disabled</Badge> : locked ? <Badge tone="warn">locked</Badge> : <Badge tone="ok">active</Badge>}</td>
                      <td className="py-2 text-right">
                        <div className="flex justify-end gap-1">
                          <Button size="sm" variant="ghost" onClick={() => setEdit(u)}>
                            Edit
                          </Button>
                          {locked && (
                            <Button size="sm" variant="ghost" onClick={() => patch.mutate({ id: u.id, body: { unlock: true } }, { onError: onErr })}>
                              Unlock
                            </Button>
                          )}
                          <Button size="sm" variant="ghost" onClick={() => patch.mutate({ id: u.id, body: { disabled: !u.disabled } }, { onError: onErr })}>
                            {u.disabled ? "Enable" : "Disable"}
                          </Button>
                          {u.kind === "user" && (
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
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      <PersonForm key={edit === "new" ? "new" : (edit?.id ?? "none")} person={edit} sso={!!q.data?.sso_enabled} onClose={() => setEdit(null)} />
    </Card>
  );
}

function PersonForm({ person, sso, onClose }: { person: Person | "new" | null; sso: boolean; onClose: () => void }) {
  const isNew = person === "new";
  const p = person && person !== "new" ? person : null;
  const [v, setV] = useState(() => ({
    kind: p?.kind ?? ("user" as "user" | "contact"),
    email: p?.email ?? "",
    name: p?.name ?? "",
    role: p?.role ?? "viewer",
    password: "",
    alerts_all: p?.alerts_all ?? false,
    alert_min_severity: p?.alert_min_severity ?? "warning",
    resume: false,
  }));
  const [groupIds, setGroupIds] = useState<string[]>(() => p?.alert_groups.map((g) => g.id) ?? []);
  const [e, setE] = useState<Record<string, string>>({});
  const save = useWrite(
    () => {
      const alertsBody = { alerts_all: v.alerts_all, alert_min_severity: v.alert_min_severity, alert_group_ids: groupIds };
      if (isNew) {
        const body: Record<string, unknown> = { kind: v.kind, email: v.email, name: v.name, ...alertsBody };
        if (v.kind === "user") Object.assign(body, { role: v.role, password: v.password });
        return api("/users", { method: "POST", body });
      }
      return api(`/users/${p!.id}`, { method: "PATCH", body: { name: v.name, ...alertsBody, ...(v.resume && { resume_alerts: true }) } });
    },
    [["users"], ["groups"]],
  );
  const contact = v.kind === "contact";
  return (
    <Dialog
      open={person !== null}
      onClose={onClose}
      title={isNew ? "Add person" : `Edit ${p?.name ?? ""}`}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="person-form" disabled={save.isPending}>
            {save.isPending ? "Saving…" : isNew ? (contact ? "Add contact" : "Add user") : "Save changes"}
          </Button>
        </>
      }
    >
      <form
        id="person-form"
        className="flex flex-col gap-5"
        onSubmit={(ev) => {
          ev.preventDefault();
          setE({});
          save.mutate(undefined, { onSuccess: onClose, onError: (x) => setE(fieldErrors(x)) });
        }}
      >
        {e._ && (
          <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
            {e._}
          </p>
        )}
        {isNew && (
          <fieldset className="grid gap-2 sm:grid-cols-2">
            <legend className="mb-1 text-xs font-medium text-foreground-muted">Type</legend>
            {(
              [
                ["user", "Dashboard user", "Signs in to the dashboard, and can get alerts."],
                ["contact", "Alert-only contact", "Never signs in. Only gets alert emails."],
              ] as const
            ).map(([k, label, hint]) => (
              <label key={k} className={cx("flex cursor-pointer gap-2 rounded-card border p-3 text-sm", v.kind === k ? "border-accent bg-accent/5" : "border-border")}>
                <input type="radio" name="kind" checked={v.kind === k} onChange={() => setV({ ...v, kind: k })} className="mt-0.5 accent-accent" />
                <span>
                  <span className="font-medium">{label}</span>
                  <span className="block text-xs text-foreground-muted">{hint}</span>
                </span>
              </label>
            ))}
          </fieldset>
        )}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Name" error={e.name}>
            {(id) => <Input id={id} value={v.name} onChange={(x) => setV({ ...v, name: x.target.value })} />}
          </Field>
          <Field label="Email" error={e.email} hint={isNew ? undefined : "The address cannot be changed; add a new person instead."}>
            {(id, d) => <Input id={id} aria-describedby={d} type="email" value={v.email} disabled={!isNew} onChange={(x) => setV({ ...v, email: x.target.value })} />}
          </Field>
          {isNew && !contact && (
            <>
              <Field label="Role" error={e.role}>
                {(id) => (
                  <Select id={id} value={v.role} onChange={(x) => setV({ ...v, role: x.target.value })}>
                    <option value="viewer">Viewer</option>
                    <option value="operator">Operator</option>
                    <option value="admin">Admin</option>
                  </Select>
                )}
              </Field>
              <Field label="Initial password" error={e.password} hint={sso ? "Leave empty for a Google-only account" : "At least 12 characters"}>
                {(id, d) => <Input id={id} aria-describedby={d} type="password" autoComplete="new-password" value={v.password} onChange={(x) => setV({ ...v, password: x.target.value })} />}
              </Field>
            </>
          )}
        </div>

        <section className="flex flex-col gap-3 rounded-card border border-border p-4">
          <h3 className="text-sm font-semibold">Alerts</h3>
          <Field label="Alert groups" error={e.alert_group_ids} hint="Alerts on any server in these groups are emailed to this person.">
            {(id, d) => <GroupPicker id={id} describedBy={d} value={groupIds} onChange={setGroupIds} allowCreate={false} note="add or remove groups any time" />}
          </Field>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={v.alerts_all} onChange={(x) => setV({ ...v, alerts_all: x.target.checked })} className="mt-0.5 accent-accent" />
            <span>
              <span className="font-medium">All alerts</span>
              <span className="block text-xs text-foreground-muted">Every server, plus fleet-wide alerts such as &quot;monitoring unavailable&quot;. For on-call people.</span>
            </span>
          </label>
          <Field label="Minimum severity" error={e.alert_min_severity} hint="The person can change this themselves.">
            {(id, d) => (
              <Select id={id} aria-describedby={d} value={v.alert_min_severity} onChange={(x) => setV({ ...v, alert_min_severity: x.target.value })} className="sm:w-64">
                <option value="info">Info and above</option>
                <option value="warning">Warning and above</option>
                <option value="critical">Critical only</option>
              </Select>
            )}
          </Field>
          {p && paused(p.alerts_paused_until) && (
            <label className="flex items-center gap-2 rounded-card border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
              <input type="checkbox" checked={v.resume} onChange={(x) => setV({ ...v, resume: x.target.checked })} className="accent-accent" />
              Paused by the person until {dateTime(p.alerts_paused_until!)}. Resume now
            </label>
          )}
        </section>
      </form>
    </Dialog>
  );
}

// ---------------- My account → My alerts ----------------

type OwnPrefs = { alerts_all: boolean; min_severity: string; paused_until: string | null; groups: AlertGroup[]; mailboxes: string[] };

export function MyAlerts() {
  const q = useQuery({ queryKey: ["my-alerts"], queryFn: () => api<OwnPrefs>("/me/alerts") });
  const [error, setError] = useState("");
  const save = useWrite((body: Record<string, unknown>) => api<OwnPrefs>("/me/alerts", { method: "PATCH", body }), [["my-alerts"]]);
  const run = (body: Record<string, unknown>) => {
    setError("");
    save.mutate(body, { onError: (e) => setError(isApiError(e) ? (Object.values(e.fields)[0] ?? e.message) : "Save failed.") });
  };
  const d = q.data;
  return (
    <Card title="My alerts" className="max-w-2xl">
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !d ? (
        <LoadingBlock rows={3} />
      ) : (
        <div className="flex flex-col gap-5 text-sm">
          <div>
            <p className="mb-2 text-xs font-medium text-foreground-muted">You get email alerts for</p>
            {d.alerts_all && (
              <p className="mb-2">
                <Badge tone="accent">All alerts</Badge> <span className="text-foreground-muted">every server and fleet-wide alerts</span>
              </p>
            )}
            {d.groups.length === 0 ? (
              !d.alerts_all && <p className="text-foreground-muted">No groups yet. An admin adds you to the groups you work on.</p>
            ) : (
              <ul className="divide-y divide-border rounded-card border border-border">
                {d.groups.map((g) => (
                  <li key={g.id} className="flex items-center justify-between gap-3 px-3 py-2">
                    <Link href={`/groups/${g.id}`} className={cx("font-medium hover:underline", g.muted ? "text-foreground-muted line-through" : "text-accent")}>
                      {g.name}
                    </Link>
                    <label className="flex items-center gap-2 text-xs">
                      <input type="checkbox" checked={g.muted} onChange={(x) => run({ muted: { [g.id]: x.target.checked } })} className="accent-accent" />
                      Mute
                    </label>
                  </li>
                ))}
              </ul>
            )}
            <p className="mt-1 text-xs text-foreground-muted">Only an admin adds or removes your groups. You can mute any of them.</p>
            {d.mailboxes.length > 0 && (
              <p className="mt-2 rounded-card border border-border bg-background px-3 py-2 text-xs">
                Your address is also on the team mailbox{d.mailboxes.length === 1 ? "" : "es"} <strong>{d.mailboxes.join(", ")}</strong> (Settings →
                Notifications). Those emails follow the mailbox&apos;s settings, not the ones below; you never get the same alert twice.
              </p>
            )}
          </div>
          <Field label="Send me" hint="Lower-severity alerts are skipped.">
            {(id, h) => (
              <Select id={id} aria-describedby={h} value={d.min_severity} onChange={(x) => run({ min_severity: x.target.value })} className="sm:w-64">
                <option value="info">Info and above</option>
                <option value="warning">Warning and above</option>
                <option value="critical">Critical only</option>
              </Select>
            )}
          </Field>
          <div>
            <p className="mb-2 text-xs font-medium text-foreground-muted">Pause</p>
            {paused(d.paused_until) ? (
              <div className="flex flex-wrap items-center gap-3">
                <Badge tone="warn">Paused until {dateTime(d.paused_until!)}</Badge>
                <Button size="sm" variant="primary" onClick={() => run({ pause: "resume" })}>
                  Resume now
                </Button>
              </div>
            ) : (
              <div className="flex flex-wrap gap-2">
                <Button size="sm" onClick={() => run({ pause: "1d" })}>
                  Pause 1 day
                </Button>
                <Button size="sm" onClick={() => run({ pause: "7d" })}>
                  Pause 7 days
                </Button>
              </div>
            )}
            <p className="mt-1 text-xs text-foreground-muted">While paused you get no alert emails. Every alert email also has pause links.</p>
          </div>
          {error && (
            <p role="alert" className="text-crit">
              {error}
            </p>
          )}
        </div>
      )}
    </Card>
  );
}

// ---------------- Group page → People ----------------

export type GroupPerson = { id: string; name: string; email?: string; kind: string; disabled: boolean; muted: boolean; min_severity: string; paused_until: string | null };

export function GroupPeopleCard({ groupId, groupName, people }: { groupId: string; groupName: string; people: GroupPerson[] }) {
  const { isAdmin } = useRole();
  const [managing, setManaging] = useState(false);
  const active = people.filter((p) => !p.disabled && !p.muted && !paused(p.paused_until)).length;
  return (
    <Card
      title={`People who get alerts (${people.length})`}
      actions={
        isAdmin && (
          <Button size="sm" variant="primary" onClick={() => setManaging(true)}>
            Add or remove people
          </Button>
        )
      }
    >
      {people.length === 0 ? (
        <EmptyState title="Nobody gets this group's alerts yet" action={isAdmin && <Button onClick={() => setManaging(true)}>Add people</Button>}>
          People tagged with a group get email for alerts on its servers.
        </EmptyState>
      ) : (
        <>
          <p className="mb-2 text-xs text-foreground-muted">
            {active} of {people.length} receiving now
          </p>
          <ul className="divide-y divide-border">
            {people.map((p) => (
              <li key={p.id} className={cx("flex flex-wrap items-center gap-2 py-2 text-sm", p.disabled && "opacity-60")}>
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{p.name}</span>
                  {p.email && <span className="ml-2 text-xs text-foreground-muted">{p.email}</span>}
                </span>
                {p.kind === "contact" && <Badge>alert-only</Badge>}
                {p.min_severity !== "warning" && <Badge>{SEVERITY_LABEL[p.min_severity]}</Badge>}
                {p.muted && <Badge tone="warn">muted</Badge>}
                {paused(p.paused_until) && <Badge tone="warn">paused until {dateTime(p.paused_until!)}</Badge>}
                {p.disabled && <Badge>disabled</Badge>}
              </li>
            ))}
          </ul>
        </>
      )}
      {isAdmin && (
        <ManagePeople key={managing ? "open" : "closed"} open={managing} onClose={() => setManaging(false)} groupId={groupId} groupName={groupName} current={people.map((p) => p.id)} />
      )}
    </Card>
  );
}

function ManagePeople({ open, onClose, groupId, groupName, current }: { open: boolean; onClose: () => void; groupId: string; groupName: string; current: string[] }) {
  const q = usePeople();
  const [ids, setIds] = useState<string[]>(current);
  const [filter, setFilter] = useState("");
  const [error, setError] = useState("");
  const save = useWrite(() => api(`/groups/${groupId}/people`, { method: "PUT", body: { user_ids: ids } }), [["groups"], ["users"]]);
  const items = useMemo(() => q.data?.items ?? [], [q.data]);
  const f = filter.toLowerCase();
  const shown = f ? items.filter((p) => p.name.toLowerCase().includes(f) || p.email.includes(f)) : items;
  const added = ids.filter((x) => !current.includes(x)).length;
  const removed = current.filter((x) => !ids.includes(x)).length;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`People who get ${groupName} alerts`}
      footer={
        <>
          <span className="mr-auto self-center text-xs text-foreground-muted">{added || removed ? `+${added} added · −${removed} removed` : "No changes"}</span>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={save.isPending || (!added && !removed)}
            onClick={() => save.mutate(undefined, { onSuccess: onClose, onError: (e) => setError(isApiError(e) ? (e.fields.user_ids ?? e.message) : "Save failed.") })}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </>
      }
    >
      {error && (
        <p role="alert" className="mb-3 rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
          {error}
        </p>
      )}
      <div className="rounded-card border border-border bg-background">
        <div className="border-b border-border p-2">
          <Input aria-label="Filter people" placeholder="Filter by name or email…" value={filter} onChange={(e) => setFilter(e.target.value)} className="h-8" />
        </div>
        {!q.data ? (
          <p className="px-3 py-2.5 text-sm text-foreground-muted">Loading people…</p>
        ) : (
          <ul className="max-h-80 divide-y divide-border overflow-y-auto">
            {shown.map((p) => (
              <li key={p.id}>
                <label className={cx("flex cursor-pointer items-center gap-3 px-3 py-2 text-sm hover:bg-surface", p.disabled && "opacity-60")}>
                  <input
                    type="checkbox"
                    checked={ids.includes(p.id)}
                    onChange={() => setIds(ids.includes(p.id) ? ids.filter((x) => x !== p.id) : [...ids, p.id])}
                    className="accent-accent"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="font-medium">{p.name}</span>
                    <span className="ml-2 text-xs text-foreground-muted">{p.email}</span>
                  </span>
                  {p.kind === "contact" && <Badge>alert-only</Badge>}
                  {p.disabled && <Badge>disabled</Badge>}
                </label>
              </li>
            ))}
            {shown.length === 0 && <li className="px-3 py-2 text-sm text-foreground-muted">Nobody matches. Add people in Settings → People.</li>}
          </ul>
        )}
        <p className="border-t border-border px-3 py-1.5 text-xs text-foreground-muted">{ids.length} selected · people can be in several groups</p>
      </div>
    </Dialog>
  );
}
