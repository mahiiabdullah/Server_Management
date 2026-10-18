"use client";

import { useState } from "react";
import { api, isApiError } from "@/lib/api";
import { useRole, useWrite } from "@/lib/queries";
import type { Server } from "@/lib/schemas";
import { GroupPicker } from "./groups";
import { Button, Dialog, Field, Input, Textarea } from "./ui";

type Probe = { reachable: boolean; node_exporter: boolean; node_exporter_version?: string; latency_ms: number; error?: string };

/** Add or edit a server, with a live Node Exporter endpoint check. */
export function ServerForm({ open, onClose, server }: { open: boolean; onClose: () => void; server?: Server }) {
  const { isAdmin } = useRole();
  const editing = !!server;
  // State starts from props; callers remount the form with a new `key` to reset it.
  const [v, setV] = useState(() =>
    server
      ? {
          name: server.name,
          exporter_url: server.exporter_url,
          hostname: server.hostname,
          environment: server.environment,
          location: server.location ?? "",
          scrape_interval_s: String(server.scrape_interval_s),
          labels: Object.entries(server.labels)
            .map(([k, val]) => `${k}=${val}`)
            .join(", "),
          notes: server.notes ?? "",
        }
      : { name: "", exporter_url: "", hostname: "", environment: "production", location: "", scrape_interval_s: "30", labels: "", notes: "" },
  );
  const [groupIds, setGroupIds] = useState<string[]>(() => server?.groups.map((g) => g.id) ?? []);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [probe, setProbe] = useState<{ state: "idle" | "checking" | "done"; result?: Probe; target?: string; error?: string }>({ state: "idle" });
  const [force, setForce] = useState(false);


  const set = (k: keyof typeof v) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => setV({ ...v, [k]: e.target.value });

  const check = async () => {
    if (!v.exporter_url.trim()) return;
    setProbe({ state: "checking" });
    try {
      const r = await api<{ target_addr: string; validation: Probe }>("/servers/validate", { method: "POST", body: { exporter_url: v.exporter_url } });
      setProbe({ state: "done", result: r.validation, target: r.target_addr });
    } catch (e) {
      setProbe({ state: "done", error: isApiError(e) ? (e.fields.exporter_url ?? e.message) : "Check failed." });
    }
  };

  const parseLabels = (): Record<string, string> | null => {
    const out: Record<string, string> = {};
    for (const part of v.labels.split(",").map((s) => s.trim()).filter(Boolean)) {
      const [k, ...rest] = part.split("=");
      if (!k || rest.length === 0) return null;
      out[k.trim()] = rest.join("=").trim();
    }
    return out;
  };

  const save = useWrite(
    async () => {
      const labels = parseLabels();
      if (labels === null) {
        setFields({ labels: "Use key=value pairs separated by commas." });
        throw new Error("labels");
      }
      const body: Record<string, unknown> = {
        name: v.name,
        environment: v.environment,
        location: v.location,
        group_ids: groupIds,
        scrape_interval_s: Number(v.scrape_interval_s),
        labels,
        notes: v.notes,
      };
      if (v.hostname) body.hostname = v.hostname;
      if (!editing || v.exporter_url !== server?.exporter_url) body.exporter_url = v.exporter_url;
      if (force) body.force = true;
      return editing ? api(`/servers/${server!.id}`, { method: "PATCH", body }) : api("/servers", { method: "POST", body });
    },
    [["servers"], ["fleet"], ["server"], ["groups"], ["costs"]],
  );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setFields({});
    setError("");
    save.mutate(undefined, {
      onSuccess: onClose,
      onError: (err) => {
        if (isApiError(err)) {
          setError(err.message);
          setFields(err.fields);
        } else if ((err as Error).message !== "labels") setError("Save failed.");
      },
    });
  };

  const unreachable = error && fields.exporter_url;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={editing ? `Edit ${server!.name}` : "Add server"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="server-form" disabled={save.isPending}>
            {save.isPending ? "Saving…" : editing ? "Save changes" : "Add server"}
          </Button>
        </>
      }
    >
      <form id="server-form" onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {error && (
          <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
            {error}
          </p>
        )}
        <Field label="Name" error={fields.name} hint="Short unique name, e.g. bm2">
          {(id, d) => <Input id={id} aria-describedby={d} aria-invalid={!!fields.name} value={v.name} onChange={set("name")} required />}
        </Field>
        <Field
          label="Node Exporter URL"
          error={fields.exporter_url}
          hint={<>IP or hostname with port 9100, e.g. http://10.0.0.12:9100/metrics. Only allowed networks are accepted.</>}
        >
          {(id, d) => (
            <div className="flex gap-2">
              <Input id={id} aria-describedby={d} aria-invalid={!!fields.exporter_url} value={v.exporter_url} onChange={set("exporter_url")} onBlur={check} placeholder="http://10.0.0.12:9100/metrics" />
              <Button onClick={check} disabled={probe.state === "checking"}>
                {probe.state === "checking" ? "Checking…" : "Check"}
              </Button>
            </div>
          )}
        </Field>
        {probe.state === "done" && (
          <div role="status" className="rounded-card border border-border px-3 py-2 text-sm">
            {probe.error ? (
              <span className="text-crit">
                <span aria-hidden>✕ </span>
                {probe.error}
              </span>
            ) : probe.result?.node_exporter ? (
              <span className="text-ok">
                <span aria-hidden>● </span>
                Node Exporter {probe.result.node_exporter_version} reachable at {probe.target} ({probe.result.latency_ms} ms)
              </span>
            ) : (
              <span className="text-warn">
                <span aria-hidden>⚠ </span>
                {probe.result?.error}
              </span>
            )}
          </div>
        )}
        {unreachable && isAdmin && (
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} className="mt-1" />
            <span>Save anyway. It will show as unknown until the first successful scrape.</span>
          </label>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label="Environment" error={fields.environment}>
            {(id, d) => <Input id={id} aria-describedby={d} value={v.environment} onChange={set("environment")} />}
          </Field>
          <Field label="Hostname" error={fields.hostname} hint="Defaults to the URL host">
            {(id, d) => <Input id={id} aria-describedby={d} value={v.hostname} onChange={set("hostname")} />}
          </Field>
          <Field label="Scrape interval (s)" error={fields.scrape_interval_s} hint="10–300">
            {(id, d) => <Input id={id} aria-describedby={d} type="number" min={10} max={300} value={v.scrape_interval_s} onChange={set("scrape_interval_s")} />}
          </Field>
          <Field label="Location" error={fields.location}>
            {(id) => <Input id={id} value={v.location} onChange={set("location")} placeholder="e.g. OVH GRA, rack 4" />}
          </Field>
        </div>
        <Field label="Groups" error={fields.group_ids} hint="What this server is used for. Tick every product it serves.">
          {(id, d) => <GroupPicker id={id} describedBy={d} value={groupIds} onChange={setGroupIds} />}
        </Field>
        <Field label="Labels" error={fields.labels} hint="key=value, comma separated (e.g. role=worker, team=platform)">
          {(id, d) => <Input id={id} aria-describedby={d} value={v.labels} onChange={set("labels")} />}
        </Field>
        <Field label="Notes" error={fields.notes}>
          {(id) => <Textarea id={id} value={v.notes} onChange={set("notes")} />}
        </Field>
      </form>
    </Dialog>
  );
}
