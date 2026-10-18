"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { api, isApiError } from "@/lib/api";
import { dateTime } from "@/lib/format";
import { Button, Card } from "@/components/ui";

// Opened from the pause links in alert emails. No sign-in: the signed link is the
// permission, so alert-only contacts can pause their own alerts too.

type Info = { name: string; action: "pause1d" | "pause7d" | "resume"; paused_until: string | null };

const WHAT = { pause1d: "Pause your alert emails for 1 day", pause7d: "Pause your alert emails for 7 days", resume: "Resume your alert emails" } as const;

export default function AlertLinkPage() {
  return (
    <main className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-md">
        <div className="mb-6 text-center text-2xl font-semibold tracking-tight">
          <span aria-hidden className="text-accent">
            ▣{" "}
          </span>
          BM Monitor
        </div>
        <Suspense>
          <Confirm />
        </Suspense>
      </div>
    </main>
  );
}

function Confirm() {
  const token = useSearchParams().get("t") ?? "";
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState("");
  const [done, setDone] = useState<Info | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!token) return;
    api<Info>(`/alert-links?t=${encodeURIComponent(token)}`)
      .then(setInfo)
      .catch((e) => setError(isApiError(e) ? e.message : "This link could not be checked."));
  }, [token]);

  const apply = async () => {
    setBusy(true);
    try {
      setDone(await api<Info>("/alert-links", { method: "POST", body: { t: token } }));
    } catch (e) {
      setError(isApiError(e) ? e.message : "That did not work. Try again.");
    } finally {
      setBusy(false);
    }
  };

  const problem = token ? error : "This link is incomplete. Open it again from the email.";
  if (problem) {
    return (
      <Card>
        <p role="alert" className="text-sm text-crit">
          <span aria-hidden>✕ </span>
          {problem}
        </p>
        <p className="mt-2 text-sm text-foreground-muted">Links in alert emails work for 30 days. A newer alert email has fresh links.</p>
      </Card>
    );
  }
  if (!info) return <Card><p className="text-sm text-foreground-muted">Checking the link…</p></Card>;
  if (done) {
    return (
      <Card>
        <p role="status" className="text-sm text-ok">
          <span aria-hidden>● </span>
          {done.paused_until ? `Done. You get no alert emails until ${dateTime(done.paused_until)}.` : "Done. Your alert emails are back on."}
        </p>
        <p className="mt-2 text-sm text-foreground-muted">You can close this page. To stop alerts for a group for good, ask an admin.</p>
      </Card>
    );
  }
  return (
    <Card>
      <h1 className="text-base font-semibold">Hi {info.name},</h1>
      <p className="mt-1 text-sm">{WHAT[info.action]}?</p>
      {info.paused_until && <p className="mt-2 text-sm text-foreground-muted">Right now your alerts are paused until {dateTime(info.paused_until)}.</p>}
      <div className="mt-4 flex gap-2">
        <Button variant="primary" onClick={apply} disabled={busy}>
          {busy ? "Saving…" : info.action === "resume" ? "Resume alerts" : "Pause alerts"}
        </Button>
      </div>
      <p className="mt-4 text-xs text-foreground-muted">Nothing changes until you press the button.</p>
    </Card>
  );
}
