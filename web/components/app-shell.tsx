"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, isApiError } from "@/lib/api";
import { useFleetSummary, useMe } from "@/lib/queries";
import { Button, cx } from "./ui";
import { LoadingBlock } from "./states";

const NAV = [
  { href: "/", label: "Overview", icon: "◫" },
  { href: "/servers", label: "Servers", icon: "▤" },
  { href: "/costs", label: "Costs", icon: "¤" },
  { href: "/alerts", label: "Alerts", icon: "△" },
  { href: "/insights", label: "Insights", icon: "◔" },
  { href: "/access", label: "SSH access", icon: "⚿" },
  { href: "/settings", label: "Settings", icon: "⚙" },
];

// The theme choice is a per-viewer convenience kept in localStorage.
const themeListeners = new Set<() => void>();
function readTheme(): "light" | "dark" | "system" {
  try {
    const t = localStorage.getItem("bm-theme");
    return t === "light" || t === "dark" ? t : "system";
  } catch {
    return "system";
  }
}
function subscribeTheme(cb: () => void) {
  themeListeners.add(cb);
  return () => {
    themeListeners.delete(cb);
  };
}

function ThemeToggle() {
  const theme = useSyncExternalStore(subscribeTheme, readTheme, () => "system" as const);
  const next = theme === "system" ? "dark" : theme === "dark" ? "light" : "system";
  const apply = () => {
    try {
      if (next === "system") {
        localStorage.removeItem("bm-theme");
        delete document.documentElement.dataset.theme;
      } else {
        localStorage.setItem("bm-theme", next);
        document.documentElement.dataset.theme = next;
      }
    } catch {}
    themeListeners.forEach((l) => l());
  };
  const icon = theme === "dark" ? "☾" : theme === "light" ? "☀" : "◐";
  return (
    <Button variant="ghost" size="sm" onClick={apply} aria-label={`Theme: ${theme}. Switch to ${next}`} title={`Theme: ${theme}`}>
      <span aria-hidden>{icon}</span>
    </Button>
  );
}

function ServerJump() {
  const router = useRouter();
  const [q, setQ] = useState("");
  return (
    <form
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        router.push(`/servers?q=${encodeURIComponent(q.trim())}`);
      }}
      className="hidden sm:block"
    >
      <label htmlFor="jump" className="sr-only">
        Find a server
      </label>
      <input
        id="jump"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="Find a server…"
        className="h-8 w-56 rounded-card border border-border bg-background px-3 text-sm placeholder:text-foreground-muted/70"
      />
    </form>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  const me = useMe();
  const router = useRouter();
  const pathname = usePathname();
  const qc = useQueryClient();
  const [menuOpen, setMenuOpen] = useState(false);
  const fleet = useFleetSummary();

  useEffect(() => {
    if (me.error && isApiError(me.error) && me.error.status === 401) {
      router.replace(`/login?next=${encodeURIComponent(pathname)}`);
    }
  }, [me.error, router, pathname]);

  if (me.isPending || (me.error && isApiError(me.error) && me.error.status === 401)) {
    return (
      <div className="mx-auto max-w-md p-10">
        <LoadingBlock rows={3} />
      </div>
    );
  }
  if (me.error) {
    return (
      <div role="alert" className="mx-auto max-w-md p-10 text-center">
        <p className="font-medium text-crit">Cannot reach the BM Monitor API.</p>
        <p className="mt-1 text-sm text-foreground-muted">{(me.error as Error).message}</p>
        <Button className="mt-4" onClick={() => me.refetch()}>
          Retry
        </Button>
      </div>
    );
  }

  const user = me.data.user;
  const openAlerts = fleet.data ? fleet.data.alerts.critical + fleet.data.alerts.warning + fleet.data.alerts.info : 0;
  const logout = async () => {
    try {
      await api("/auth/logout", { method: "POST" });
    } finally {
      qc.clear();
      router.replace("/login");
    }
  };

  const nav = (
    <nav aria-label="Main" className="flex flex-col gap-0.5 p-3">
      {NAV.map((n) => {
        const active = n.href === "/" ? pathname === "/" : pathname.startsWith(n.href);
        return (
          <Link
            key={n.href}
            href={n.href}
            onClick={() => setMenuOpen(false)}
            aria-current={active ? "page" : undefined}
            className={cx(
              "flex items-center gap-3 rounded-card px-3 py-2 text-sm",
              active ? "bg-accent/10 font-medium text-accent" : "text-foreground-muted hover:bg-background hover:text-foreground",
            )}
          >
            <span aria-hidden className="w-4 text-center">
              {n.icon}
            </span>
            {n.label}
            {n.href === "/alerts" && openAlerts > 0 && (
              <span className="ml-auto rounded-full bg-crit px-1.5 text-xs font-semibold text-white" aria-label={`${openAlerts} open alerts`}>
                {openAlerts}
              </span>
            )}
          </Link>
        );
      })}
    </nav>
  );

  return (
    <div className="flex min-h-screen">
      <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded focus:bg-surface focus:px-3 focus:py-2">
        Skip to content
      </a>
      <aside className="sticky top-0 hidden h-screen w-56 shrink-0 flex-col border-r border-border bg-surface lg:flex">
        <div className="flex h-14 items-center gap-2 border-b border-border px-5 font-semibold">
          <span aria-hidden className="text-accent">
            ▣
          </span>
          BM Monitor
        </div>
        {nav}
      </aside>
      {menuOpen && (
        <div className="fixed inset-0 z-40 lg:hidden" role="dialog" aria-label="Menu">
          <button className="absolute inset-0 bg-black/40" aria-label="Close menu" onClick={() => setMenuOpen(false)} />
          <div className="relative h-full w-64 border-r border-border bg-surface">{nav}</div>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-surface/95 px-4 backdrop-blur">
          <Button variant="ghost" size="sm" className="lg:hidden" onClick={() => setMenuOpen(true)} aria-label="Open menu">
            ☰
          </Button>
          <span className="font-semibold lg:hidden">BM Monitor</span>
          <ServerJump />
          <div className="ml-auto flex items-center gap-2">
            <ThemeToggle />
            <span className="hidden text-right text-xs leading-tight sm:block">
              <span className="block font-medium">{user.name}</span>
              <span className="text-foreground-muted">
                {user.email} · {user.role}
              </span>
            </span>
            <Button size="sm" onClick={logout}>
              Sign out
            </Button>
          </div>
        </header>
        <main id="main" className="mx-auto w-full max-w-7xl flex-1 px-4 py-6 sm:px-6">
          {children}
        </main>
      </div>
    </div>
  );
}
