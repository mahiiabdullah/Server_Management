"use client";

import { Suspense, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { EmptyState, ErrorState, LoadingBlock, PageHeader } from "@/components/states";
import { Badge, Button, Card, Field, Input, Modal, Tabs, Textarea } from "@/components/ui";
import { api, isApiError } from "@/lib/api";
import { date, dateTime } from "@/lib/format";
import { useRole, useWrite } from "@/lib/queries";

type Config =
  | { enabled: false; reason: string }
  | {
      enabled: true;
      repo: string;
      servers: { name: string; host: string; port?: number; production: boolean; ready: boolean }[];
      groups: { name: string; servers: string[]; production: boolean }[];
      max_days: number;
      max_sudo_days: number;
      production_approvals: number;
      sudo_approvals: number;
      admins: number;
      unix_user: string;
      unix_user_error?: string;
    };

type AccessRequest = {
  id: string;
  kind: "request" | "revoke";
  grant_id: string;
  requester_email: string;
  unix_user: string;
  servers: string[];
  groups: string[];
  sudo: boolean;
  reason: string;
  key_fingerprint: string | null;
  requested_at: string;
  expires_at: string | null;
  status: "pr_open" | "merged" | "active" | "rejected" | "expired" | "revoked" | "failed";
  status_detail: string | null;
  pr_number: number | null;
  pr_url: string | null;
  certificate?: string | null;
};

type GrantView = {
  id: string;
  user: string;
  email: string;
  servers: string[];
  groups?: string[];
  covers: string[];
  sudo: boolean;
  reason: string;
  requested_at: string;
  expires: string;
  status: string;
  status_reason?: string;
  server_state?: Record<string, string>;
};

/** What a request or grant names: servers and "group x" entries. */
function Targets({ servers, groups }: { servers: string[]; groups?: string[] }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {servers.map((s) => (
        <span key={s}>{s}</span>
      ))}
      {(groups ?? []).map((g) => (
        <Badge key={g} tone="accent">
          group {g}
        </Badge>
      ))}
    </span>
  );
}

const STATUS: Record<AccessRequest["status"], { tone: "ok" | "warn" | "crit" | "muted" | "accent"; label: string }> = {
  pr_open: { tone: "warn", label: "Waiting for approval" },
  merged: { tone: "accent", label: "Merged, waiting for apply" },
  active: { tone: "ok", label: "Active" },
  rejected: { tone: "crit", label: "Rejected" },
  expired: { tone: "muted", label: "Expired" },
  revoked: { tone: "muted", label: "Revoked" },
  failed: { tone: "crit", label: "Not applied" },
};

export default function AccessPage() {
  return (
    <Suspense fallback={<LoadingBlock rows={6} />}>
      <Access />
    </Suspense>
  );
}

function Access() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const { isAdmin } = useRole();
  const cfg = useQuery({ queryKey: ["access-config"], queryFn: () => api<Config>("/access/config") });
  const tab = (params.get("tab") as "mine" | "grants") || "mine";

  return (
    <>
      <PageHeader
        title="SSH access"
        subtitle="Request time-limited access to a server. Every request is a GitHub pull request that an admin must approve."
      />
      {cfg.error ? (
        <ErrorState error={cfg.error} onRetry={() => cfg.refetch()} />
      ) : !cfg.data ? (
        <LoadingBlock rows={5} />
      ) : !cfg.data.enabled ? (
        <Card>
          <EmptyState title="SSH access requests are not set up">{cfg.data.reason}</EmptyState>
        </Card>
      ) : (
        <>
          {isAdmin && (
            <div className="mb-4">
              <Tabs
                label="Access views"
                value={tab}
                onChange={(t) => router.replace(`${pathname}?tab=${t}`, { scroll: false })}
                tabs={[
                  { id: "mine", label: "My access" },
                  { id: "grants", label: "All grants" },
                ]}
              />
            </div>
          )}
          {tab === "grants" && isAdmin ? <AllGrants /> : <Mine cfg={cfg.data} />}
        </>
      )}
    </>
  );
}

function Mine({ cfg }: { cfg: Extract<Config, { enabled: true }> }) {
  const q = useQuery({ queryKey: ["access-requests"], queryFn: () => api<{ items: AccessRequest[] }>("/access/requests"), refetchInterval: 30_000 });
  const pending = q.data?.items.some((r) => r.kind === "request" && (r.status === "pr_open" || r.status === "merged"));
  return (
    <div className="grid gap-5 lg:grid-cols-5">
      <div className="lg:col-span-2">
        <RequestForm cfg={cfg} blocked={!!pending} />
      </div>
      <div className="flex flex-col gap-4 lg:col-span-3">
        <h2 className="text-sm font-semibold">My requests</h2>
        {q.error ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <LoadingBlock rows={3} />
        ) : q.data.items.length === 0 ? (
          <Card>
            <EmptyState title="No requests yet">Your approved access, its certificate and the command to connect will appear here.</EmptyState>
          </Card>
        ) : (
          q.data.items.map((r) => <RequestCard key={r.id} r={r} cfg={cfg} />)
        )}
      </div>
    </div>
  );
}

function RequestForm({ cfg, blocked }: { cfg: Extract<Config, { enabled: true }>; blocked: boolean }) {
  const [mode, setMode] = useState<"servers" | "groups">("servers");
  const [servers, setServers] = useState<string[]>([]);
  const [groups, setGroups] = useState<string[]>([]);
  const [sudoChoice, setSudo] = useState(false);
  const sudo = mode === "servers" && sudoChoice;
  const [days, setDays] = useState("7");
  const [reason, setReason] = useState("");
  const [key, setKey] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [done, setDone] = useState<AccessRequest | null>(null);
  const max = sudo ? cfg.max_sudo_days : cfg.max_days;
  const production = mode === "servers" ? servers.some((s) => cfg.servers.find((x) => x.name === s)?.production) : groups.some((g) => cfg.groups.find((x) => x.name === g)?.production);
  const required = Math.max(1, sudo ? cfg.sudo_approvals : 1, production ? cfg.production_approvals : 1);
  const admins = (n: number) => (n === 1 ? "one admin approval" : `${n} admin approvals`);
  const chosen = mode === "servers" ? servers.length : groups.length;

  const create = useWrite(
    () =>
      api<AccessRequest>("/access/requests", {
        method: "POST",
        body: { ...(mode === "servers" ? { servers } : { groups }), sudo, days: Number(days), reason, public_key: key },
      }),
    [["access-requests"]],
  );

  return (
    <Card title="Request access">
      {done ? (
        <div className="flex flex-col gap-3 text-sm" role="status">
          <p className="font-medium text-ok">● Request {done.grant_id} sent.</p>
          <p>
            Pull request{" "}
            <a href={done.pr_url ?? "#"} target="_blank" rel="noreferrer" className="text-accent hover:underline">
              #{done.pr_number}
            </a>{" "}
            is waiting for {admins(required)}. This page updates by itself once access is granted.
          </p>
          <Button onClick={() => (setDone(null), setKey(""), setReason(""), setServers([]), setGroups([]))}>New request</Button>
        </div>
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setErrors({});
            create.mutate(undefined, {
              onSuccess: (r) => setDone(r),
              onError: (err) => setErrors(isApiError(err) ? { ...err.fields, _: Object.keys(err.fields).length ? "" : err.message } : { _: "Request failed." }),
            });
          }}
        >
          {blocked && <p className="rounded-card border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">You already have a request in progress (waiting for approval or for the apply job). You can send a new one when it is finished.</p>}
          {(errors._ || errors.form) && (
            <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
              {errors._ || errors.form}
            </p>
          )}
          <p className="text-sm text-foreground-muted">
            You will log in as <code className="font-mono">{cfg.unix_user}</code>
            {cfg.unix_user_error && <span className="text-crit"> ({cfg.unix_user_error})</span>}.
          </p>
          <div role="radiogroup" aria-label="Request access to" className="grid grid-cols-2 gap-1 rounded-card border border-border bg-background p-1 text-sm">
            {(
              [
                ["servers", "By server"],
                ["groups", "By group"],
              ] as const
            ).map(([m, label]) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={mode === m}
                onClick={() => setMode(m)}
                className={mode === m ? "rounded bg-surface px-3 py-1.5 font-medium shadow-sm" : "rounded px-3 py-1.5 text-foreground-muted hover:text-foreground"}
              >
                {label}
              </button>
            ))}
          </div>
          {mode === "servers" ? (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1 text-xs font-medium text-foreground-muted">Servers</legend>
              {cfg.servers.map((s) => (
                <label key={s.name} className={`flex items-center gap-2 text-sm ${s.ready ? "" : "opacity-60"}`}>
                  <input
                    type="checkbox"
                    disabled={!s.ready}
                    checked={servers.includes(s.name)}
                    onChange={(e) => setServers(e.target.checked ? [...servers, s.name] : servers.filter((x) => x !== s.name))}
                  />
                  <span className="font-medium">{s.name}</span>
                  <span className="font-mono text-xs text-foreground-muted">
                    {s.host}
                    {s.port ? `:${s.port}` : ""}
                  </span>
                  {s.production ? <Badge tone="warn">production</Badge> : <Badge>testing</Badge>}
                  {!s.ready && <Badge>setup not done</Badge>}
                </label>
              ))}
              {errors.servers && <p className="text-xs text-crit">{errors.servers}</p>}
            </fieldset>
          ) : (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1 text-xs font-medium text-foreground-muted">Access groups</legend>
              {cfg.groups.length === 0 ? (
                <p className="text-sm text-foreground-muted">No access groups yet. An admin creates them from a group page (SSH access card).</p>
              ) : (
                cfg.groups.map((g) => (
                  <label key={g.name} className="flex items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={groups.includes(g.name)}
                      disabled={g.servers.length === 0}
                      onChange={(e) => setGroups(e.target.checked ? [...groups, g.name] : groups.filter((x) => x !== g.name))}
                    />
                    <span>
                      <span className="font-medium">{g.name}</span> {g.production && <Badge tone="warn">has production</Badge>}
                      <span className="block text-xs text-foreground-muted">{g.servers.length ? g.servers.join(", ") : "no servers yet"}</span>
                    </span>
                  </label>
                ))
              )}
              <p className="text-xs text-foreground-muted">Group access follows the group: servers added to it later are covered too, removed ones are not.</p>
              {(errors.groups || errors.servers) && <p className="text-xs text-crit">{errors.groups || errors.servers}</p>}
            </fieldset>
          )}
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" className="mt-1" checked={sudo} disabled={mode === "groups"} onChange={(e) => setSudo(e.target.checked)} />
            <span>
              I need sudo (root) on these servers
              <span className="block text-xs text-foreground-muted">
                {mode === "groups" ? "Not available for groups: request sudo for specific servers." : `Needs ${admins(cfg.sudo_approvals)} and at most ${cfg.max_sudo_days} days.`}
              </span>
              {errors.sudo && <span className="block text-xs text-crit">{errors.sudo}</span>}
            </span>
          </label>
          <Field label={`How many days (1–${max})`} error={errors.days}>
            {(id) => <Input id={id} type="number" min={1} max={max} value={days} onChange={(e) => setDays(e.target.value)} className="w-32" />}
          </Field>
          <Field label="Reason" error={errors.reason} hint="What you will do. Admins read this before approving.">
            {(id) => <Textarea id={id} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. Debug the pandastack sandbox on the testing box" />}
          </Field>
          <Field
            label="Your public SSH key"
            error={errors.public_key}
            hint={
              <>
                Paste the contents of <code className="font-mono">~/.ssh/id_ed25519.pub</code>. No key yet? Run{" "}
                <code className="font-mono">ssh-keygen -t ed25519</code>. Never paste your private key.
              </>
            }
          >
            {(id) => <Textarea id={id} value={key} onChange={(e) => setKey(e.target.value)} className="font-mono text-xs" placeholder="ssh-ed25519 AAAA… you@laptop" />}
          </Field>
          <p className="text-xs text-foreground-muted">
            This request needs {admins(required)}. You cannot approve your own request.
          </p>
          <Button type="submit" variant="primary" disabled={create.isPending || blocked || chosen === 0}>
            {create.isPending ? "Opening pull request…" : "Send request"}
          </Button>
        </form>
      )}
    </Card>
  );
}

function CopyBlock({ label, text }: { label: string; text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between text-xs text-foreground-muted">
        <span>{label}</span>
        <button
          type="button"
          className="text-accent hover:underline"
          onClick={() =>
            navigator.clipboard
              ?.writeText(text)
              .then(() => (setCopied(true), setTimeout(() => setCopied(false), 1500)))
              .catch(() => {})
          }
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre className="overflow-x-auto rounded-card border border-border bg-background p-2 font-mono text-xs">{text}</pre>
    </div>
  );
}

function RequestCard({ r, cfg }: { r: AccessRequest; cfg: Extract<Config, { enabled: true }> }) {
  const s = STATUS[r.status];
  const covered = [...new Set([...r.servers, ...r.groups.flatMap((g) => cfg.groups.find((x) => x.name === g)?.servers ?? [])])];
  const logins = covered.map((n) => {
    const s = cfg.servers.find((x) => x.name === n);
    return { key: n, cmd: `ssh ${s?.port ? `-p ${s.port} ` : ""}${r.unix_user}@${s?.host ?? n}` };
  });
  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="font-medium">
            {r.kind === "revoke" ? "Revoke of " : ""}
            <Targets servers={r.servers} groups={r.groups} /> {r.sudo && <Badge tone="warn">sudo</Badge>}
          </p>
          <p className="mt-0.5 text-xs text-foreground-muted">
            Grant {r.grant_id} · requested {dateTime(r.requested_at)}
            {r.expires_at && ` · until ${date(r.expires_at)}`}
            {r.pr_url && (
              <>
                {" · "}
                <a href={r.pr_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                  PR #{r.pr_number}
                </a>
              </>
            )}
          </p>
        </div>
        <Badge tone={s.tone}>{s.label}</Badge>
      </div>
      {r.status_detail && <p className="mt-2 text-sm text-foreground-muted">{r.status_detail}</p>}
      {r.kind === "request" && r.status === "active" && r.certificate && (
        <div className="mt-4 flex flex-col gap-3 border-t border-border pt-4">
          <CopyBlock label="1. Save this certificate next to your key, as ~/.ssh/id_ed25519-cert.pub" text={r.certificate} />
          {logins.map((l, i) => (
            <CopyBlock key={l.key} label={i === 0 ? "2. Connect" : " "} text={l.cmd} />
          ))}
          <p className="text-xs text-foreground-muted">The certificate stops working on {r.expires_at ? dateTime(r.expires_at) : "its expiry date"}. Request again if you need more time.</p>
        </div>
      )}
    </Card>
  );
}

function AllGrants() {
  const q = useQuery({ queryKey: ["access-grants"], queryFn: () => api<{ items: GrantView[]; applied: { generated_at: string } | null }>("/access/grants") });
  const reqs = useQuery({ queryKey: ["access-requests-all"], queryFn: () => api<{ items: AccessRequest[] }>("/access/requests?all=1") });
  const [confirm, setConfirm] = useState<GrantView | null>(null);
  const [error, setError] = useState("");
  const revoke = useWrite((id: string) => api<AccessRequest>(`/access/grants/${id}/revoke`, { method: "POST" }), [["access-requests-all"], ["access-grants"]]);
  return (
    <div className="flex flex-col gap-5">
      <Card
        title="Who has access (access/grants.yaml on main)"
        actions={q.data?.applied ? <span className="text-xs text-foreground-muted">applied {dateTime(q.data.applied.generated_at)}</span> : <Badge tone="warn">apply job has not run yet</Badge>}
      >
        {q.error ? (
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        ) : !q.data ? (
          <LoadingBlock rows={3} />
        ) : q.data.items.length === 0 ? (
          <EmptyState title="Nobody has access through this workflow yet" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-foreground-muted">
                <tr>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Grant</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">User</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Servers</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Until</th>
                  <th scope="col" className="py-1.5 pr-3 font-medium">Status</th>
                  <th scope="col" className="py-1.5"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {q.data.items.map((g) => (
                  <tr key={g.id} className="align-top">
                    <td className="py-2 pr-3 font-mono text-xs">{g.id}</td>
                    <td className="py-2 pr-3">
                      <code className="font-mono">{g.user}</code>
                      <div className="text-xs text-foreground-muted">{g.email}</div>
                    </td>
                    <td className="py-2 pr-3">
                      <Targets servers={g.servers} groups={g.groups} /> {g.sudo && <Badge tone="warn">sudo</Badge>}
                      {(g.groups?.length ?? 0) > 0 && <div className="mt-0.5 text-xs text-foreground-muted">covers {g.covers.length ? g.covers.join(", ") : "no servers"}</div>}
                      {g.server_state && Object.entries(g.server_state).some(([, v]) => v !== "ok") && (
                        <div className="mt-0.5 text-xs text-warn">
                          {Object.entries(g.server_state)
                            .filter(([, v]) => v !== "ok")
                            .map(([k, v]) => `${k}: ${v}`)
                            .join("; ")}
                        </div>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-xs">{dateTime(g.expires)}</td>
                    <td className="py-2 pr-3 text-xs">
                      <Badge tone={g.status === "active" ? "ok" : g.status === "awaiting_approval" ? "crit" : "muted"}>{g.status.replaceAll("_", " ")}</Badge>
                      {g.status_reason && <div className="mt-1 text-foreground-muted">{g.status_reason}</div>}
                    </td>
                    <td className="py-2 text-right">
                      <Button size="sm" variant="ghost" className="text-crit!" onClick={() => (setConfirm(g), setError(""))}>
                        Revoke
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Card title="All requests">
        {!reqs.data ? (
          <LoadingBlock rows={3} />
        ) : reqs.data.items.length === 0 ? (
          <EmptyState title="No requests yet" />
        ) : (
          <ul className="divide-y divide-border text-sm">
            {reqs.data.items.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <span>
                  {r.kind === "revoke" ? "Revoke " : ""}
                  <code className="font-mono">{r.unix_user}</code> → <Targets servers={r.servers} groups={r.groups} />{" "}
                  <span className="text-xs text-foreground-muted">({r.requester_email}, grant {r.grant_id})</span>
                </span>
                <span className="flex items-center gap-2">
                  {r.pr_url && (
                    <a href={r.pr_url} target="_blank" rel="noreferrer" className="text-xs text-accent hover:underline">
                      PR #{r.pr_number}
                    </a>
                  )}
                  <Badge tone={STATUS[r.status].tone}>{STATUS[r.status].label}</Badge>
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Modal
        open={!!confirm}
        onClose={() => setConfirm(null)}
        title={`Revoke grant ${confirm?.id}?`}
        footer={
          <>
            <Button onClick={() => setConfirm(null)}>Cancel</Button>
            <Button
              variant="danger"
              disabled={revoke.isPending}
              onClick={() =>
                confirm &&
                revoke.mutate(confirm.id, {
                  onSuccess: () => setConfirm(null),
                  onError: (e) => setError(isApiError(e) ? e.message : "Revoke failed."),
                })
              }
            >
              Open revoke pull request
            </Button>
          </>
        }
      >
        <p>
          This opens a pull request removing <code className="font-mono">{confirm?.user}</code>&apos;s access to {confirm?.covers.join(", ") || "its servers"}. Once it is approved and merged, the
          account is disabled and the certificate is revoked on every server.
        </p>
        {error && <p className="mt-3 text-crit">{error}</p>}
      </Modal>
    </div>
  );
}
