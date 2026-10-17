"use client";

import { useSyncExternalStore, type ReactNode } from "react";
import { isApiError } from "@/lib/api";
import { ago } from "@/lib/format";
import { Button, Skeleton } from "./ui";

// The five page states (plan section 12.3): loading, empty, error, stale, no permission.

export function LoadingBlock({ rows = 4 }: { rows?: number }) {
  return (
    <div role="status" aria-label="Loading" className="flex flex-col gap-3">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  );
}

export function EmptyState({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 py-10 text-center">
      <p className="font-medium">{title}</p>
      {children && <div className="max-w-md text-sm text-foreground-muted">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (isApiError(error) && error.status === 403) return <NoPermission />;
  const msg = isApiError(error) ? error.message : "Something went wrong.";
  return (
    <div role="alert" className="flex flex-col items-center gap-3 rounded-card border border-crit/40 bg-crit/5 py-8 text-center">
      <p className="font-medium text-crit">
        <span aria-hidden>✕ </span>Could not load this data
      </p>
      <p className="max-w-md text-sm text-foreground-muted">{msg}</p>
      {onRetry && (
        <Button size="sm" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

export function NoPermission() {
  return (
    <div role="alert" className="rounded-card border border-border bg-surface py-10 text-center">
      <p className="font-medium">
        <span aria-hidden>🔒 </span>You do not have permission to see this.
      </p>
      <p className="mt-1 text-sm text-foreground-muted">Ask an admin for a role with access.</p>
    </div>
  );
}

// A clock that ticks every 15 s, so "how old is this data" re-renders without impure render calls.
function subscribeClock(cb: () => void) {
  const t = setInterval(cb, 15_000);
  return () => clearInterval(t);
}
const clockNow = () => Math.floor(Date.now() / 15_000) * 15_000;

/** Amber banner when telemetry is unavailable or older than expected. */
export function StaleBanner({ available, asOf, maxAgeS = 120 }: { available: boolean; asOf: string | null | undefined; maxAgeS?: number }) {
  const now = useSyncExternalStore(subscribeClock, clockNow, () => 0);
  if (!available) {
    return (
      <div role="status" className="rounded-card border border-warn/50 bg-warn/10 px-4 py-2.5 text-sm text-warn">
        <span aria-hidden>⚠ </span>
        <strong>Monitoring unavailable.</strong> Prometheus cannot be reached, so live values are missing and server states show as unknown.
      </div>
    );
  }
  if (asOf && now > 0 && (now - new Date(asOf).getTime()) / 1000 > maxAgeS) {
    return (
      <div role="status" className="rounded-card border border-warn/50 bg-warn/10 px-4 py-2.5 text-sm text-warn">
        <span aria-hidden>⚠ </span>Data is {ago(asOf, now)} old.
      </div>
    );
  }
  return null;
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-0.5 text-sm text-foreground-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
