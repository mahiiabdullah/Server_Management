"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { api, isApiError } from "@/lib/api";
import { Me } from "@/lib/schemas";
import { Button, Field, Input } from "@/components/ui";

export default function LoginPage() {
  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 text-center">
          <div className="text-2xl font-semibold tracking-tight">
            <span aria-hidden className="text-accent">
              ▣{" "}
            </span>
            BM Monitor
          </div>
          <p className="mt-1 text-sm text-foreground-muted">Bare-metal fleet monitoring and cost management</p>
        </div>
        <Suspense>
          <LoginForm />
        </Suspense>
      </div>
    </main>
  );
}

function LoginForm() {
  const router = useRouter();
  const params = useSearchParams();
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(params.get("error") === "sso" ? "Google sign-in was refused: no matching account, or the attempt expired." : "");
  const [fields, setFields] = useState<Record<string, string>>({});
  const [google, setGoogle] = useState(false);

  useEffect(() => {
    api<{ google: boolean }>("/auth/providers")
      .then((p) => setGoogle(p.google))
      .catch(() => {});
  }, []);

  const next = params.get("next");
  const dest = next && next.startsWith("/") && !next.startsWith("//") ? next : "/";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setFields({});
    try {
      const me = await api("/auth/login", { method: "POST", body: { email, password }, schema: Me });
      qc.setQueryData(["me"], me);
      router.replace(dest);
    } catch (err) {
      if (isApiError(err)) {
        setError(err.message);
        setFields(err.fields);
      } else setError("Sign-in failed.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4 rounded-card border border-border bg-surface p-6" noValidate>
      <h1 className="text-base font-semibold">Sign in</h1>
      {error && (
        <p role="alert" className="rounded-card border border-crit/40 bg-crit/5 px-3 py-2 text-sm text-crit">
          {error}
        </p>
      )}
      <Field label="Email" error={fields.email}>
        {(id, d) => <Input id={id} aria-describedby={d} aria-invalid={!!fields.email} type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />}
      </Field>
      <Field label="Password" error={fields.password}>
        {(id, d) => (
          <Input id={id} aria-describedby={d} aria-invalid={!!fields.password} type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        )}
      </Field>
      <Button type="submit" variant="primary" disabled={busy}>
        {busy ? "Signing in…" : "Sign in"}
      </Button>
      {google && (
        <>
          <div className="flex items-center gap-3 text-xs text-foreground-muted">
            <span className="h-px flex-1 bg-border" /> or <span className="h-px flex-1 bg-border" />
          </div>
          <a href="/api/v1/auth/google" className="inline-flex h-9 items-center justify-center rounded-card border border-border text-sm font-medium hover:bg-background">
            Sign in with Google
          </a>
        </>
      )}
    </form>
  );
}
