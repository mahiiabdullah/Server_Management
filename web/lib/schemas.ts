// Zod schemas for the API responses the dashboard depends on. A shape change on
// the server fails loudly in development instead of rendering wrong numbers.
import { z } from "zod";

const n = z.number().nullable();
const iso = z.string();

export const Role = z.enum(["admin", "operator", "viewer"]);
export type Role = z.infer<typeof Role>;

export const Me = z.object({
  user: z.object({ id: z.string(), email: z.string(), name: z.string(), role: Role }),
  csrf_token: z.string(),
});
export type Me = z.infer<typeof Me>;

export const Status = z.enum(["online", "offline", "unknown", "disabled"]);
export type Status = z.infer<typeof Status>;

export const Filesystem = z.object({
  mountpoint: z.string(),
  device: z.string(),
  fstype: z.string(),
  size_bytes: z.number(),
  avail_bytes: z.number(),
  used_ratio: z.number(),
});

export const Metrics = z.object({
  last_scrape: iso.nullable(),
  cpu: n,
  memory: n,
  disk: n,
  load1: n,
  load5: n,
  load15: n,
  net_rx: n,
  net_tx: n,
  uptime_seconds: n,
  cores: n,
  memory_total_bytes: n,
  memory_used_bytes: n,
  disk_total_bytes: n,
  disk_used_bytes: n,
  exporter_version: z.string().optional(),
  filesystems: z.array(Filesystem).optional(),
});
export type Metrics = z.infer<typeof Metrics>;

export const Server = z.object({
  id: z.string(),
  name: z.string(),
  hostname: z.string(),
  exporter_url: z.string(),
  target_addr: z.string(),
  environment: z.string(),
  location: z.string().nullable(),
  group_id: z.string().nullable(),
  group_name: z.string().nullable(),
  enabled: z.boolean(),
  scrape_interval_s: z.number(),
  cpu_cores: n,
  memory_bytes: n,
  disk_bytes: n,
  notes: z.string().nullable(),
  labels: z.record(z.string(), z.string()),
  created_at: iso,
  updated_at: iso,
  status: Status,
  metrics: Metrics.nullable(),
  monthly_cost: n,
});
export type Server = z.infer<typeof Server>;

export const ServerList = z.object({
  items: z.array(Server),
  page: z.number(),
  page_size: z.number(),
  total: z.number(),
  as_of: iso.nullable(),
  metrics_available: z.boolean(),
  facets: z.object({
    environments: z.array(z.string()),
    groups: z.array(z.object({ id: z.string(), name: z.string() })),
  }),
});
export type ServerList = z.infer<typeof ServerList>;

const Ratio = z.object({ value: n, unit: z.literal("ratio"), servers_with_data: z.number() });

export const FleetSummary = z.object({
  as_of: iso.nullable(),
  monitoring: z.object({ available: z.boolean() }),
  servers: z.object({
    registered: z.number(),
    enabled: z.number(),
    disabled: z.number(),
    online: z.number(),
    offline: z.number(),
    unknown: z.number(),
  }),
  utilization: z.object({
    cpu: Ratio,
    memory: Ratio,
    disk: Ratio,
    network: z.object({ rx: n, tx: n, unit: z.string(), servers_with_data: z.number() }),
    capacity: z.object({ cores: z.number(), memory_bytes: z.number(), disk_bytes: z.number() }),
  }),
  alerts: z.object({ critical: z.number(), warning: z.number(), info: z.number() }),
  cost: z.union([
    z.object({ status: z.literal("not_configured"), currency: z.string() }),
    z.object({
      status: z.literal("configured"),
      period: z.string(),
      currency: z.string(),
      to_date: z.number(),
      projected: z.number(),
      kind: z.literal("estimated"),
      servers_without_pricing: z.number(),
    }),
  ]),
});
export type FleetSummary = z.infer<typeof FleetSummary>;

export const Alert = z.object({
  id: z.string(),
  rule_id: z.string(),
  rule_name: z.string(),
  kind: z.string(),
  server_id: z.string().nullable(),
  server_name: z.string().nullable(),
  labels: z.record(z.string(), z.string()).nullable(),
  state: z.enum(["pending", "firing", "acknowledged", "resolved"]),
  severity: z.enum(["info", "warning", "critical"]),
  value: n,
  message: z.string(),
  started_at: iso,
  fired_at: iso.nullable(),
  acknowledged_at: iso.nullable(),
  acknowledged_by: z.string().nullable(),
  resolved_at: iso.nullable(),
});
export type Alert = z.infer<typeof Alert>;

export const AlertList = z.object({ items: z.array(Alert), page: z.number(), page_size: z.number(), total: z.number() });

const CostLine = z.object({
  source: z.string(),
  name: z.string(),
  category: z.string(),
  unit: z.string(),
  rate: z.number(),
  active_hours: z.number(),
  fraction: z.number(),
  to_date: z.number(),
  projected: z.number(),
  note: z.string().optional(),
});
export type CostLine = z.infer<typeof CostLine>;

export const ServerCost = z.object({
  server_id: z.string(),
  name: z.string(),
  group_id: z.string().optional(),
  group_name: z.string().optional(),
  deleted: z.boolean().optional(),
  priced: z.boolean(),
  active_hours: z.number(),
  to_date: z.number(),
  projected: z.number(),
  lines: z.array(CostLine),
});
export type ServerCost = z.infer<typeof ServerCost>;

export const ServerDetail = z.object({
  server: Server,
  as_of: iso.nullable(),
  metrics_available: z.boolean(),
  pricing: z.array(
    z.object({
      id: z.string(),
      plan_id: z.string(),
      plan_name: z.string(),
      amount: z.number(),
      unit: z.string(),
      currency: z.string(),
      category: z.string(),
      effective_from: iso,
      effective_to: iso.nullable(),
    }),
  ),
  cost: ServerCost.nullable(),
  currency: z.string(),
  open_alerts: z.array(Alert),
  recent_incidents: z.array(Alert),
});
export type ServerDetail = z.infer<typeof ServerDetail>;

export const History = z.object({
  range: z.string(),
  step_s: z.number(),
  coverage: z.number(),
  series: z.array(
    z.object({
      name: z.string(),
      unit: z.string(),
      labels: z.record(z.string(), z.string()).optional(),
      points: z.array(z.tuple([z.string(), z.number().nullable()])),
    }),
  ),
});
export type History = z.infer<typeof History>;

const Formula = z.record(z.string(), z.union([z.string(), z.number()]));

export const CostSummary = z.union([
  z.object({ status: z.literal("not_configured"), period: z.string(), currency: z.string(), formula: Formula }),
  z.object({
    status: z.literal("configured"),
    currency: z.string(),
    kind: z.literal("estimated"),
    formula: Formula,
    actual: z.object({ total: z.number(), by_server: z.record(z.string(), z.number()) }).nullable(),
    result: z.object({
      period: z.string(),
      closed: z.boolean(),
      hours_in_month: z.number(),
      hours_elapsed: z.number(),
      to_date: z.number(),
      projected: z.number(),
      by_category: z.record(z.string(), z.number()),
      by_category_projected: z.record(z.string(), z.number()),
      servers: z.array(ServerCost),
      groups: z.array(z.object({ group_id: z.string(), name: z.string(), to_date: z.number(), projected: z.number(), shared: z.array(CostLine) })),
      fleet_shared: z.array(CostLine),
      servers_without_pricing: z.array(z.string()),
      proration: z.boolean(),
    }),
  }),
]);
export type CostSummary = z.infer<typeof CostSummary>;

export const CostTrend = z.object({
  status: z.enum(["configured", "not_configured"]),
  currency: z.string(),
  points: z.array(
    z.object({ period: z.string(), estimated: z.number(), projected: z.number().nullable().optional(), actual: z.number().nullable(), closed: z.boolean() }),
  ),
});
export type CostTrend = z.infer<typeof CostTrend>;

export const Evidence = z.object({
  server_id: z.string(),
  name: z.string(),
  group_name: z.string().nullable(),
  cpu_p95: n,
  memory_p95: n,
  avg_cpu: n,
  avg_memory: n,
  coverage: z.number(),
  underutilized: z.boolean(),
  reason: z.string(),
  monthly_cost: n,
  possible_savings: n,
  recommendation: z.string().optional(),
});
export type Evidence = z.infer<typeof Evidence>;

export const Underutilized = z.object({
  as_of: iso,
  window_start: iso,
  rules: z.object({ window_days: z.number(), cpu_p95_max_pct: z.number(), memory_p95_max_pct: z.number(), min_coverage_pct: z.number() }),
  currency: z.string(),
  evaluated: z.number(),
  insufficient_data: z.number(),
  underutilized: z.array(Evidence),
  servers: z.array(Evidence),
  total_possible_savings: n,
});
export type Underutilized = z.infer<typeof Underutilized>;
