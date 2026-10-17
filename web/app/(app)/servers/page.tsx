"use client";

import Link from "next/link";
import { Suspense, useEffect, useMemo, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { DeleteServer, EnableToggle } from "@/components/server-actions";
import { ServerForm } from "@/components/server-form";
import { EmptyState, ErrorState, LoadingBlock, PageHeader, StaleBanner } from "@/components/states";
import { StatusBadge } from "@/components/status";
import { Button, Card, Input, Meter, Pagination, Select, cx } from "@/components/ui";
import { ago, duration, money } from "@/lib/format";
import { useCostSummary, useRole, useServers } from "@/lib/queries";
import type { Server } from "@/lib/schemas";

export default function ServersPage() {
  return (
    <Suspense fallback={<LoadingBlock rows={8} />}>
      <Servers />
    </Suspense>
  );
}

const COLUMNS: { key: string; label: string; sort?: string; className?: string }[] = [
  { key: "status", label: "Status", sort: "status" },
  { key: "name", label: "Name", sort: "name" },
  { key: "group", label: "Group", sort: "group", className: "hidden md:table-cell" },
  { key: "environment", label: "Env", sort: "environment", className: "hidden lg:table-cell" },
  { key: "cpu", label: "CPU", sort: "cpu" },
  { key: "memory", label: "Memory", sort: "memory", className: "hidden sm:table-cell" },
  { key: "disk", label: "Disk", sort: "disk", className: "hidden sm:table-cell" },
  { key: "uptime", label: "Uptime", sort: "uptime", className: "hidden xl:table-cell" },
  { key: "last_scrape", label: "Last scrape", sort: "last_scrape", className: "hidden lg:table-cell" },
  { key: "cost", label: "Monthly cost", sort: "cost", className: "hidden xl:table-cell" },
];

function Servers() {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const { isOperator } = useRole();
  const [adding, setAdding] = useState(params.get("add") === "1");
  const [editing, setEditing] = useState<Server | undefined>();
  const [search, setSearch] = useState(params.get("q") ?? "");
  const groupBy = params.get("groupby") === "group";

  const apiParams = useMemo(() => {
    const p = new URLSearchParams(params.toString());
    p.delete("add");
    p.delete("groupby");
    if (!p.get("page_size")) p.set("page_size", groupBy ? "100" : "25");
    return p;
  }, [params, groupBy]);
  const q = useServers(apiParams);
  const costs = useCostSummary("", "");
  const currency = costs.data?.currency ?? "USD";

  const update = (changes: Record<string, string | null>) => {
    const p = new URLSearchParams(params.toString());
    for (const [k, v] of Object.entries(changes)) {
      if (v === null || v === "") p.delete(k);
      else p.set(k, v);
    }
    if (!("page" in changes)) p.delete("page");
    p.delete("add");
    router.replace(`${pathname}?${p.toString()}`, { scroll: false });
  };

  useEffect(() => {
    const t = setTimeout(() => {
      if ((params.get("q") ?? "") !== search) update({ q: search });
    }, 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);

  const sort = params.get("sort") ?? "name";
  const toggleSort = (key: string) => update({ sort: sort === key ? `-${key}` : key });
  const ariaSort = (key: string) => (sort === key ? "ascending" : sort === `-${key}` ? "descending" : "none");

  const rows = useMemo(() => q.data?.items ?? [], [q.data]);
  const groups = useMemo(() => {
    if (!groupBy) return [{ name: "", items: rows }];
    const m = new Map<string, Server[]>();
    for (const s of rows) {
      const k = s.group_name ?? "No group";
      m.set(k, [...(m.get(k) ?? []), s]);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, items]) => ({ name, items }));
  }, [rows, groupBy]);

  return (
    <>
      <PageHeader
        title="Servers"
        subtitle={q.data ? `${q.data.total} server${q.data.total === 1 ? "" : "s"} · live values refresh every 30 s` : "Inventory and live status"}
        actions={
          isOperator && (
            <Button variant="primary" onClick={() => setAdding(true)}>
              + Add server
            </Button>
          )
        }
      />
      {q.data && <div className="mb-4"><StaleBanner available={q.data.metrics_available} asOf={q.data.as_of} /></div>}

      <div className="mb-4 flex flex-wrap items-end gap-2">
        <div className="w-full sm:w-64">
          <label htmlFor="q" className="sr-only">
            Search servers
          </label>
          <Input id="q" placeholder="Search name, hostname, address…" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <FilterSelect label="Status" value={params.get("status") ?? ""} onChange={(v) => update({ status: v })}>
          <option value="">All statuses</option>
          <option value="online">Online</option>
          <option value="offline">Offline</option>
          <option value="unknown">Unknown</option>
          <option value="disabled">Disabled</option>
          <option value="offline,unknown">Needs attention</option>
        </FilterSelect>
        <FilterSelect label="Environment" value={params.get("environment") ?? ""} onChange={(v) => update({ environment: v })}>
          <option value="">All environments</option>
          {q.data?.facets.environments.map((e) => (
            <option key={e} value={e}>
              {e}
            </option>
          ))}
        </FilterSelect>
        <FilterSelect label="Group" value={params.get("group") ?? ""} onChange={(v) => update({ group: v })}>
          <option value="">All groups</option>
          <option value="none">No group</option>
          {q.data?.facets.groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </FilterSelect>
        <FilterSelect label="Group by" value={groupBy ? "group" : ""} onChange={(v) => update({ groupby: v })}>
          <option value="">No grouping</option>
          <option value="group">Group by group</option>
        </FilterSelect>
        {!groupBy && (
          <FilterSelect label="Rows per page" value={params.get("page_size") ?? "25"} onChange={(v) => update({ page_size: v })}>
            {["10", "25", "50", "100"].map((n) => (
              <option key={n} value={n}>
                {n} / page
              </option>
            ))}
          </FilterSelect>
        )}
      </div>

      <Card className="overflow-hidden [&>div]:p-0">
        {q.error ? (
          <div className="p-4">
            <ErrorState error={q.error} onRetry={() => q.refetch()} />
          </div>
        ) : !q.data ? (
          <div className="p-4">
            <LoadingBlock rows={8} />
          </div>
        ) : q.data.total === 0 ? (
          params.toString() && params.toString() !== "add=1" ? (
            <EmptyState title="No servers match these filters" action={<Button onClick={() => router.replace(pathname)}>Clear filters</Button>} />
          ) : (
            <EmptyState title="No servers registered yet" action={isOperator && <Button variant="primary" onClick={() => setAdding(true)}>Add your first server</Button>}>
              Install Node Exporter on the server, then add its address. No code change or rebuild is needed.
            </EmptyState>
          )
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <caption className="sr-only">Servers, sorted by {sort.replace("-", "")} {sort.startsWith("-") ? "descending" : "ascending"}</caption>
              <thead className="border-b border-border bg-background/60 text-left text-xs text-foreground-muted">
                <tr>
                  {COLUMNS.map((c) => (
                    <th key={c.key} scope="col" aria-sort={c.sort ? ariaSort(c.sort) : undefined} className={cx("px-3 py-2 font-medium", c.className)}>
                      {c.sort ? (
                        <button type="button" onClick={() => toggleSort(c.sort!)} className="inline-flex items-center gap-1 hover:text-foreground">
                          {c.label}
                          <span aria-hidden>{sort === c.sort ? "↑" : sort === `-${c.sort}` ? "↓" : ""}</span>
                        </button>
                      ) : (
                        c.label
                      )}
                    </th>
                  ))}
                  <th scope="col" className="px-3 py-2">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              {groups.map((g) => (
                <tbody key={g.name} className="divide-y divide-border">
                  {groupBy && (
                    <tr className="bg-background/60">
                      <th colSpan={COLUMNS.length + 1} scope="rowgroup" className="px-3 py-1.5 text-left text-xs font-semibold">
                        {g.name} <span className="font-normal text-foreground-muted">({g.items.length})</span>
                      </th>
                    </tr>
                  )}
                  {g.items.map((s) => (
                    <Row key={s.id} s={s} currency={currency} onEdit={() => setEditing(s)} canEdit={isOperator} />
                  ))}
                </tbody>
              ))}
            </table>
          </div>
        )}
      </Card>
      {q.data && !groupBy && q.data.total > 0 && (
        <div className="mt-3">
          <Pagination page={q.data.page} pageSize={q.data.page_size} total={q.data.total} onPage={(p) => update({ page: String(p) })} />
        </div>
      )}
      <ServerForm key={adding ? "add-open" : "add-closed"} open={adding} onClose={() => setAdding(false)} />
      <ServerForm key={editing?.id ?? "edit-none"} open={!!editing} onClose={() => setEditing(undefined)} server={editing} />
    </>
  );
}

function FilterSelect({ label, value, onChange, children }: { label: string; value: string; onChange: (v: string) => void; children: React.ReactNode }) {
  return (
    <div>
      <label className="sr-only" htmlFor={`f-${label}`}>
        {label}
      </label>
      <Select id={`f-${label}`} value={value} onChange={(e) => onChange(e.target.value)} className="w-auto">
        {children}
      </Select>
    </div>
  );
}

function Row({ s, currency, onEdit, canEdit }: { s: Server; currency: string; onEdit: () => void; canEdit: boolean }) {
  const m = s.metrics;
  return (
    <tr className="hover:bg-background/50">
      <td className="px-3 py-2.5">
        <StatusBadge status={s.status} />
      </td>
      <td className="px-3 py-2.5">
        <Link href={`/servers/${s.id}`} className="font-medium text-accent hover:underline">
          {s.name}
        </Link>
        <div className="font-mono text-xs text-foreground-muted">{s.target_addr}</div>
      </td>
      <td className="hidden px-3 py-2.5 md:table-cell">{s.group_name ?? <span className="text-foreground-muted">—</span>}</td>
      <td className="hidden px-3 py-2.5 lg:table-cell">{s.environment}</td>
      <td className="px-3 py-2.5">
        <Meter value={m?.cpu} label={`${s.name} CPU`} threshold={0.9} />
      </td>
      <td className="hidden px-3 py-2.5 sm:table-cell">
        <Meter value={m?.memory} label={`${s.name} memory`} threshold={0.9} />
      </td>
      <td className="hidden px-3 py-2.5 sm:table-cell">
        <Meter value={m?.disk} label={`${s.name} disk (fullest filesystem)`} threshold={0.85} />
      </td>
      <td className="hidden px-3 py-2.5 font-mono text-xs xl:table-cell">{duration(m?.uptime_seconds)}</td>
      <td className="hidden px-3 py-2.5 text-xs lg:table-cell">{ago(m?.last_scrape)}</td>
      <td className="hidden px-3 py-2.5 font-mono text-xs xl:table-cell">
        {s.monthly_cost != null ? money(s.monthly_cost, currency) : <span className="text-foreground-muted">not priced</span>}
      </td>
      <td className="px-3 py-2.5">
        <div className="flex justify-end gap-1">
          {canEdit && (
            <Button size="sm" variant="ghost" onClick={onEdit} aria-label={`Edit ${s.name}`}>
              Edit
            </Button>
          )}
          <EnableToggle server={s} />
          <DeleteServer server={s} afterDelete={() => {}} />
        </div>
      </td>
    </tr>
  );
}
