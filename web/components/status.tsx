import type { ReactNode } from "react";
import { Badge } from "./ui";

// Status is never shown by colour alone: every badge has an icon and a word.
const STATUS = {
  online: { tone: "ok", icon: "●", label: "Online" },
  offline: { tone: "crit", icon: "✕", label: "Offline" },
  unknown: { tone: "warn", icon: "?", label: "Unknown" },
  disabled: { tone: "muted", icon: "○", label: "Disabled" },
} as const;

export function StatusBadge({ status }: { status: keyof typeof STATUS }) {
  const s = STATUS[status];
  return (
    <Badge tone={s.tone}>
      <span aria-hidden>{s.icon}</span>
      {s.label}
    </Badge>
  );
}

const SEVERITY = {
  critical: { tone: "crit", icon: "▲", label: "Critical" },
  warning: { tone: "warn", icon: "◆", label: "Warning" },
  info: { tone: "accent", icon: "i", label: "Info" },
} as const;

export function SeverityBadge({ severity }: { severity: keyof typeof SEVERITY }) {
  const s = SEVERITY[severity];
  return (
    <Badge tone={s.tone}>
      <span aria-hidden>{s.icon}</span>
      {s.label}
    </Badge>
  );
}

const STATE = {
  pending: { tone: "muted", label: "Pending" },
  firing: { tone: "crit", label: "Firing" },
  acknowledged: { tone: "warn", label: "Acknowledged" },
  resolved: { tone: "ok", label: "Resolved" },
} as const;

export function AlertStateBadge({ state }: { state: keyof typeof STATE }) {
  const s = STATE[state];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

export function KindBadge({ kind }: { kind: "estimated" | "actual" }): ReactNode {
  return <Badge tone={kind === "actual" ? "ok" : "accent"}>{kind === "actual" ? "Actual" : "Estimated"}</Badge>;
}
