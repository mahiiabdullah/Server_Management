"use client";

import { useEffect, useId, useRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

type Variant = "primary" | "secondary" | "ghost" | "danger";
const variants: Record<Variant, string> = {
  primary: "bg-accent text-white hover:opacity-90 border-transparent",
  secondary: "bg-surface text-foreground border-border hover:bg-background",
  ghost: "bg-transparent border-transparent text-foreground-muted hover:text-foreground hover:bg-background",
  danger: "bg-crit text-white border-transparent hover:opacity-90",
};

export function Button({ variant = "secondary", size = "md", className, ...p }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md" }) {
  return (
    <button
      type="button"
      {...p}
      className={cx(
        "inline-flex items-center justify-center gap-1.5 rounded-card border font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed",
        size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3.5 text-sm",
        variants[variant],
        className,
      )}
    />
  );
}

export function Card({ title, actions, children, className, id }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string; id?: string }) {
  return (
    <section aria-labelledby={title && id ? id : undefined} className={cx("rounded-card border border-border bg-surface", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
          {title && (
            <h2 id={id} className="text-sm font-semibold">
              {title}
            </h2>
          )}
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function Field({ label, error, hint, children }: { label: string; error?: string; hint?: ReactNode; children: (id: string, describedBy?: string) => ReactNode }) {
  const id = useId();
  const descId = error || hint ? `${id}-desc` : undefined;
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-xs font-medium text-foreground-muted">
        {label}
      </label>
      {children(id, descId)}
      {error ? (
        <p id={descId} className="text-xs text-crit" role="alert">
          {error}
        </p>
      ) : hint ? (
        <p id={descId} className="text-xs text-foreground-muted">
          {hint}
        </p>
      ) : null}
    </div>
  );
}

const inputCls =
  "h-9 w-full rounded-card border border-border bg-background px-3 text-sm text-foreground placeholder:text-foreground-muted/70 aria-[invalid=true]:border-crit";

export function Input(p: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...p} className={cx(inputCls, p.className)} />;
}

export function Select(p: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...p} className={cx(inputCls, "pr-8", p.className)} />;
}

export function Textarea(p: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...p} className={cx(inputCls, "h-auto min-h-20 py-2", p.className)} />;
}

/**
 * Centered form dialog with focus handling and Escape to close. The body scrolls
 * when the form is taller than the screen; on phones it fills the screen.
 */
export function Dialog({ open, onClose, title, children, footer, size = "md" }: { open: boolean; onClose: () => void; title: string; children: ReactNode; footer?: ReactNode; size?: "md" | "lg" }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      className={cx(
        "m-auto w-[calc(100%-2rem)] overflow-hidden rounded-card border border-border bg-surface p-0 text-foreground shadow-2xl backdrop:bg-black/50 backdrop:backdrop-blur-[1px]",
        size === "lg" ? "max-w-3xl" : "max-w-2xl",
        "max-sm:h-dvh max-sm:max-h-none max-sm:w-full max-sm:max-w-none max-sm:rounded-none max-sm:border-0",
      )}
      aria-label={title}
    >
      {open && (
        <div className="flex max-h-[min(88vh,56rem)] flex-col max-sm:h-full max-sm:max-h-none">
          <header className="flex items-center justify-between border-b border-border px-6 py-4">
            <h2 className="text-base font-semibold">{title}</h2>
            <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
              ✕
            </Button>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">{children}</div>
          {footer && <footer className="flex justify-end gap-2 border-t border-border bg-background/40 px-6 py-3">{footer}</footer>}
        </div>
      )}
    </dialog>
  );
}

/** Centered confirmation dialog. */
export function Modal({ open, onClose, title, children, footer }: { open: boolean; onClose: () => void; title: string; children: ReactNode; footer: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      className="w-[calc(100%-2rem)] max-w-md rounded-card border border-border bg-surface p-0 text-foreground backdrop:bg-black/40"
      aria-label={title}
    >
      {open && (
        <div>
          <h2 className="border-b border-border px-5 py-4 text-base font-semibold">{title}</h2>
          <div className="px-5 py-4 text-sm">{children}</div>
          <footer className="flex justify-end gap-2 border-t border-border px-5 py-3">{footer}</footer>
        </div>
      )}
    </dialog>
  );
}

export function Tabs<T extends string>({ tabs, value, onChange, label }: { tabs: { id: T; label: ReactNode }[]; value: T; onChange: (v: T) => void; label: string }) {
  return (
    <div role="tablist" aria-label={label} className="flex gap-1 overflow-x-auto border-b border-border">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          type="button"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className={cx(
            "-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm",
            value === t.id ? "border-accent font-medium text-foreground" : "border-transparent text-foreground-muted hover:text-foreground",
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function Pagination({ page, pageSize, total, onPage }: { page: number; pageSize: number; total: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(total, page * pageSize);
  return (
    <nav aria-label="Pagination" className="flex items-center justify-between gap-3 text-sm text-foreground-muted">
      <span>
        {from}–{to} of {total}
      </span>
      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => onPage(page - 1)} disabled={page <= 1}>
          Previous
        </Button>
        <span className="font-mono text-xs">
          {page} / {pages}
        </span>
        <Button size="sm" onClick={() => onPage(page + 1)} disabled={page >= pages}>
          Next
        </Button>
      </div>
    </nav>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cx("animate-pulse rounded bg-border/60", className)} />;
}

export function Badge({ tone = "muted", children }: { tone?: "ok" | "warn" | "crit" | "muted" | "accent"; children: ReactNode }) {
  const tones = {
    ok: "text-ok border-ok/40 bg-ok/10",
    warn: "text-warn border-warn/40 bg-warn/10",
    crit: "text-crit border-crit/40 bg-crit/10",
    muted: "text-muted border-border bg-background",
    accent: "text-accent border-accent/40 bg-accent/10",
  };
  return <span className={cx("inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap", tones[tone])}>{children}</span>;
}

/** A small horizontal meter for a 0..1 ratio, with an optional threshold tick. */
export function Meter({ value, threshold = 0.85, label, showValue = true }: { value: number | null | undefined; threshold?: number; label: string; showValue?: boolean }) {
  if (value == null) return <span className="text-foreground-muted">—</span>;
  const tone = value >= threshold ? "bg-crit" : value >= threshold - 0.15 ? "bg-warn" : "bg-ok";
  return (
    <div className="flex items-center gap-2" title={`${label}: ${(value * 100).toFixed(1)}%`}>
      <div className={cx("h-1.5 overflow-hidden rounded-full bg-border", showValue ? "w-16" : "w-full max-w-48")} role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(value * 100)}>
        <div className={cx("h-full rounded-full", tone)} style={{ width: `${Math.min(100, value * 100)}%` }} />
      </div>
      {showValue && <span className="w-10 text-right font-mono text-xs tabular-nums">{(value * 100).toFixed(0)}%</span>}
    </div>
  );
}
