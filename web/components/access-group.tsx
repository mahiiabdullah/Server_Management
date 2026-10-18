"use client";

import Link from "next/link";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, isApiError } from "@/lib/api";
import { dateTime } from "@/lib/format";
import { useRole, useWrite } from "@/lib/queries";
import { ErrorState, LoadingBlock } from "./states";
import { Badge, Button, Card } from "./ui";

// SSH access groups live in access/policy.yaml on GitHub, so every change to
// who a group grant reaches is reviewed. This card compares the dashboard group
// with its access group and lets an admin propose the change as a pull request.

type Proposal = { id: string; group_name: string; servers: string[] | null; status: "pr_open" | "merged" | "rejected"; pr_number: number | null; pr_url: string | null; created_at: string };

type GroupAccess = {
  enabled: boolean;
  in_policy: boolean;
  policy_servers: string[];
  wanted: string[];
  not_ready: string[];
  production: string[];
  in_sync: boolean;
  grants: string[];
  proposal: Proposal | null;
  name_allowed: boolean;
};

const list = (xs: string[]) => (xs.length ? xs.join(", ") : "none");

export function AccessGroupCard({ groupId }: { groupId: string }) {
  const { isAdmin } = useRole();
  const q = useQuery({ queryKey: ["groups", "access", groupId], queryFn: () => api<GroupAccess>(`/groups/${groupId}/access`), refetchInterval: 60_000 });
  const [error, setError] = useState("");
  const propose = useWrite(() => api<Proposal>(`/groups/${groupId}/access/propose`, { method: "POST" }), [["groups", "access", groupId]]);

  if (q.error) {
    return (
      <Card title="SSH access">
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      </Card>
    );
  }
  if (!q.data) {
    return (
      <Card title="SSH access">
        <LoadingBlock rows={2} />
      </Card>
    );
  }
  const d = q.data;
  if (!d.enabled) return null; // the access workflow is not configured
  const open = d.proposal?.status === "pr_open";
  const state = d.in_sync ? (
    <Badge tone="ok">● in sync</Badge>
  ) : !d.in_policy ? (
    <Badge>not an access group yet</Badge>
  ) : (
    <Badge tone="warn">differs from this group</Badge>
  );

  return (
    <Card title="SSH access" actions={state}>
      <div className="flex flex-col gap-3 text-sm">
        <p className="text-foreground-muted">
          People can request SSH to every server of an access group at once (
          <Link href="/access" className="text-accent hover:underline">
            SSH access
          </Link>
          ). The access group is kept in GitHub, so changing its servers is a pull request another admin approves.
        </p>
        <dl className="grid gap-x-4 gap-y-1.5 sm:grid-cols-[10rem_1fr]">
          <dt className="text-foreground-muted">Access group now</dt>
          <dd>{d.in_policy ? list(d.policy_servers) : "—"}</dd>
          <dt className="text-foreground-muted">This group&apos;s servers</dt>
          <dd>
            {list(d.wanted)}
            {d.production.length > 0 && (
              <span className="ml-2">
                <Badge tone="warn">production: {d.production.join(", ")}</Badge>
              </span>
            )}
          </dd>
          {d.not_ready.length > 0 && (
            <>
              <dt className="text-foreground-muted">Not included</dt>
              <dd className="text-warn">{d.not_ready.join(", ")}: SSH setup not done on these servers yet</dd>
            </>
          )}
          <dt className="text-foreground-muted">Grants using it</dt>
          <dd>{list(d.grants)}</dd>
          {d.proposal && (
            <>
              <dt className="text-foreground-muted">Last change</dt>
              <dd>
                {d.proposal.pr_url ? (
                  <a href={d.proposal.pr_url} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                    PR #{d.proposal.pr_number}
                  </a>
                ) : (
                  "pull request"
                )}{" "}
                <Badge tone={d.proposal.status === "merged" ? "ok" : d.proposal.status === "rejected" ? "crit" : "warn"}>
                  {{ pr_open: "waiting for approval", merged: "merged", rejected: "closed" }[d.proposal.status]}
                </Badge>{" "}
                <span className="text-xs text-foreground-muted">{dateTime(d.proposal.created_at)}</span>
              </dd>
            </>
          )}
        </dl>
        {!d.name_allowed && (
          <p className="text-xs text-warn">This group&apos;s name cannot be used for an access group. Use letters, digits, spaces, dots, dashes or underscores.</p>
        )}
        {isAdmin && !d.in_sync && (
          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="primary"
              disabled={propose.isPending || open || !d.name_allowed}
              onClick={() => {
                setError("");
                propose.mutate(undefined, { onError: (e) => setError(isApiError(e) ? (Object.values(e.fields)[0] ?? e.message) : "Could not open the pull request.") });
              }}
            >
              {propose.isPending ? "Opening pull request…" : d.in_policy ? "Propose access change" : "Make this an access group"}
            </Button>
            <span className="text-xs text-foreground-muted">
              {open ? "A change is already waiting for approval." : `Opens a pull request setting the access group to: ${list(d.wanted)}.`}
            </span>
          </div>
        )}
        {error && (
          <p role="alert" className="text-crit">
            {error}
          </p>
        )}
      </div>
    </Card>
  );
}
