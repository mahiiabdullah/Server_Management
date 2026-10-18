"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { api, isApiError } from "@/lib/api";
import { useGroups, useRole, useServers, useWrite } from "@/lib/queries";
import type { Group, GroupRef } from "@/lib/schemas";
import { StatusBadge } from "./status";
import { Button, Dialog, Field, Input, Textarea, cx } from "./ui";

/** A server's groups as small links to each group page. */
export function GroupChips({ groups, empty = "—", className }: { groups: GroupRef[]; empty?: string; className?: string }) {
  if (groups.length === 0) return <span className="text-foreground-muted">{empty}</span>;
  return (
    <span className={cx("inline-flex flex-wrap gap-1", className)}>
      {groups.map((g) => (
        <Link
          key={g.id}
          href={`/groups/${g.id}`}
          className="inline-flex items-center rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-xs font-medium whitespace-nowrap text-accent hover:border-accent/60"
        >
          {g.name}
        </Link>
      ))}
    </span>
  );
}

/**
 * Pick any number of groups, with a quick "new group" box. Selection is a list
 * of group ids, kept by the parent.
 */
export function GroupPicker({ id, value, onChange, describedBy }: { id: string; value: string[]; onChange: (ids: string[]) => void; describedBy?: string }) {
  const groups = useGroups();
  const { isOperator } = useRole();
  const [filter, setFilter] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const create = useWrite((n: string) => api<Group>("/groups", { method: "POST", body: { name: n } }), [["groups"]]);

  const items = groups.data?.items ?? [];
  const shown = filter ? items.filter((g) => g.name.toLowerCase().includes(filter.toLowerCase())) : items;
  const toggle = (gid: string) => onChange(value.includes(gid) ? value.filter((x) => x !== gid) : [...value, gid]);

  const add = () => {
    const n = name.trim();
    if (!n) return;
    setError("");
    create.mutate(n, {
      onSuccess: (g) => {
        onChange([...value, g.id]);
        setName("");
      },
      onError: (e) => setError(isApiError(e) ? (e.fields.name ?? e.message) : "Could not create the group."),
    });
  };

  return (
    <div id={id} role="group" aria-describedby={describedBy} className="rounded-card border border-border bg-background">
      {items.length > 6 && (
        <div className="border-b border-border p-2">
          <Input aria-label="Filter groups" placeholder="Filter groups…" value={filter} onChange={(e) => setFilter(e.target.value)} className="h-8" />
        </div>
      )}
      {items.length === 0 ? (
        <p className="px-3 py-2.5 text-sm text-foreground-muted">No groups yet{isOperator ? ". Create the first one below." : "."}</p>
      ) : (
        <ul className="grid max-h-44 grid-cols-1 gap-x-3 overflow-y-auto px-3 py-2 sm:grid-cols-2">
          {shown.map((g) => (
            <li key={g.id}>
              <label className="flex cursor-pointer items-center gap-2 rounded py-1 text-sm">
                <input type="checkbox" checked={value.includes(g.id)} onChange={() => toggle(g.id)} className="accent-accent" />
                <span className="truncate">{g.name}</span>
                <span className="ml-auto text-xs text-foreground-muted">{g.server_count}</span>
              </label>
            </li>
          ))}
          {shown.length === 0 && <li className="py-1 text-sm text-foreground-muted">No group matches.</li>}
        </ul>
      )}
      {isOperator && (
        <div className="flex gap-2 border-t border-border p-2">
          <Input
            aria-label="New group name"
            placeholder="New group, e.g. puku-bot"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                add();
              }
            }}
            className="h-8"
          />
          <Button size="sm" className="shrink-0 whitespace-nowrap" onClick={add} disabled={!name.trim() || create.isPending}>
            {create.isPending ? "Adding…" : "+ Add"}
          </Button>
        </div>
      )}
      {error && (
        <p role="alert" className="px-3 pb-2 text-xs text-crit">
          {error}
        </p>
      )}
      <p className="border-t border-border px-3 py-1.5 text-xs text-foreground-muted">
        {value.length === 0 ? "No group selected" : `${value.length} selected`} · a server can be in several groups
      </p>
    </div>
  );
}

const ALL_SERVERS = new URLSearchParams({ page_size: "100" });

/** Checklist of every server, for choosing a group's members. */
function ServerChecklist({ id, value, onChange }: { id: string; value: string[]; onChange: (ids: string[]) => void }) {
  const servers = useServers(ALL_SERVERS);
  const [filter, setFilter] = useState("");
  const items = useMemo(() => servers.data?.items ?? [], [servers.data]);
  const f = filter.toLowerCase();
  const shown = f ? items.filter((s) => s.name.toLowerCase().includes(f) || s.target_addr.includes(f)) : items;
  const toggle = (sid: string) => onChange(value.includes(sid) ? value.filter((x) => x !== sid) : [...value, sid]);
  return (
    <div id={id} role="group" className="rounded-card border border-border bg-background">
      <div className="flex items-center gap-2 border-b border-border p-2">
        <Input aria-label="Filter servers" placeholder="Filter servers…" value={filter} onChange={(e) => setFilter(e.target.value)} className="h-8" />
        <Button size="sm" variant="ghost" onClick={() => onChange([...new Set([...value, ...shown.map((s) => s.id)])])} disabled={shown.length === 0}>
          All
        </Button>
        <Button size="sm" variant="ghost" onClick={() => onChange(value.filter((v) => !shown.some((s) => s.id === v)))} disabled={value.length === 0}>
          None
        </Button>
      </div>
      {!servers.data ? (
        <p className="px-3 py-2.5 text-sm text-foreground-muted">Loading servers…</p>
      ) : items.length === 0 ? (
        <p className="px-3 py-2.5 text-sm text-foreground-muted">No servers registered yet.</p>
      ) : (
        <ul className="max-h-72 divide-y divide-border overflow-y-auto">
          {shown.map((s) => (
            <li key={s.id}>
              <label className="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm hover:bg-surface">
                <input type="checkbox" checked={value.includes(s.id)} onChange={() => toggle(s.id)} className="accent-accent" />
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{s.name}</span>
                  <span className="ml-2 font-mono text-xs text-foreground-muted">{s.target_addr}</span>
                  {s.groups.length > 0 && <span className="mt-0.5 block truncate text-xs text-foreground-muted">In: {s.groups.map((g) => g.name).join(", ")}</span>}
                </span>
                <StatusBadge status={s.status} />
              </label>
            </li>
          ))}
          {shown.length === 0 && <li className="px-3 py-2 text-sm text-foreground-muted">No server matches.</li>}
        </ul>
      )}
      <p className="border-t border-border px-3 py-1.5 text-xs text-foreground-muted">
        {value.length} of {items.length} selected · servers can stay in other groups too
      </p>
    </div>
  );
}

const INVALIDATE = [["groups"], ["servers"], ["server"], ["costs"]];

/** Create a group (name, description, first servers) or edit its name and description. */
export function GroupForm({ open, onClose, group, onSaved }: { open: boolean; onClose: () => void; group?: Group; onSaved?: (g: Group) => void }) {
  const editing = !!group;
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [serverIds, setServerIds] = useState<string[]>([]);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const save = useWrite(
    () =>
      editing
        ? api<Group>(`/groups/${group!.id}`, { method: "PATCH", body: { name, description } })
        : api<Group>("/groups", { method: "POST", body: { name, description, server_ids: serverIds } }),
    INVALIDATE,
  );
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setFields({});
    setError("");
    save.mutate(undefined, {
      onSuccess: (g) => {
        onSaved?.(g);
        onClose();
      },
      onError: (err) => {
        setError(isApiError(err) ? err.message : "Save failed.");
        if (isApiError(err)) setFields(err.fields);
      },
    });
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={editing ? `Edit ${group!.name}` : "New group"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="group-form" disabled={save.isPending || !name.trim()}>
            {save.isPending ? "Saving…" : editing ? "Save changes" : "Create group"}
          </Button>
        </>
      }
    >
      <form id="group-form" onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {error && (
          <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
            {error}
          </p>
        )}
        <Field label="Name" error={fields.name} hint="The product or purpose, e.g. puku-web-chat or puku-bot">
          {(id, d) => <Input id={id} aria-describedby={d} aria-invalid={!!fields.name} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Description" error={fields.description} hint="Optional. What runs here, who owns it.">
          {(id, d) => <Textarea id={id} aria-describedby={d} value={description} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
        {!editing && (
          <Field label="Servers" error={fields.server_ids} hint="Optional. You can add or remove servers later.">
            {(id) => <ServerChecklist id={id} value={serverIds} onChange={setServerIds} />}
          </Field>
        )}
      </form>
    </Dialog>
  );
}

/** Choose exactly which servers belong to a group. */
export function ManageServers({ open, onClose, group, current }: { open: boolean; onClose: () => void; group: Group; current: string[] }) {
  const [serverIds, setServerIds] = useState<string[]>(current);
  const [error, setError] = useState("");
  const save = useWrite(() => api<Group>(`/groups/${group.id}/servers`, { method: "PUT", body: { server_ids: serverIds } }), INVALIDATE);
  const added = serverIds.filter((x) => !current.includes(x)).length;
  const removed = current.filter((x) => !serverIds.includes(x)).length;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`Servers in ${group.name}`}
      footer={
        <>
          <span className="mr-auto self-center text-xs text-foreground-muted">{added || removed ? `+${added} added · −${removed} removed` : "No changes"}</span>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={save.isPending || (!added && !removed)}
            onClick={() =>
              save.mutate(undefined, {
                onSuccess: onClose,
                onError: (e) => setError(isApiError(e) ? (e.fields.server_ids ?? e.message) : "Save failed."),
              })
            }
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
      <ServerChecklist id="group-servers" value={serverIds} onChange={setServerIds} />
    </Dialog>
  );
}
