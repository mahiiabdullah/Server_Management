"use client";

import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { z } from "zod";
import { api, isApiError } from "./api";
import * as S from "./schemas";

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 10_000,
            refetchOnWindowFocus: true,
            retry: (count, err) => !(isApiError(err) && [400, 401, 403, 404].includes(err.status)) && count < 2,
          },
        },
      }),
  );
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

/** Live pages poll every 30 s (plan section 12.1). */
export const LIVE = 30_000;

export function useMe() {
  return useQuery({ queryKey: ["me"], queryFn: () => api("/me", { schema: S.Me }), retry: false, staleTime: 60_000 });
}

export function useRole() {
  const me = useMe();
  const role = me.data?.user.role;
  return {
    role,
    isOperator: role === "operator" || role === "admin",
    isAdmin: role === "admin",
  };
}

export function useFleetSummary() {
  return useQuery({ queryKey: ["fleet"], queryFn: () => api("/fleet/summary", { schema: S.FleetSummary }), refetchInterval: LIVE });
}

export function useServers(params: URLSearchParams) {
  const qs = params.toString();
  return useQuery({
    queryKey: ["servers", qs],
    queryFn: () => api(`/servers?${qs}`, { schema: S.ServerList }),
    refetchInterval: LIVE,
    placeholderData: (prev) => prev,
  });
}

export function useServer(id: string) {
  return useQuery({ queryKey: ["server", id], queryFn: () => api(`/servers/${id}`, { schema: S.ServerDetail }), refetchInterval: LIVE });
}

export function useHistory(id: string, range: string, series: string) {
  return useQuery({
    queryKey: ["history", id, range, series],
    queryFn: () => api(`/servers/${id}/metrics?range=${range}&series=${series}`, { schema: S.History }),
    refetchInterval: range === "1h" || range === "6h" ? LIVE : 5 * 60_000,
    placeholderData: (prev) => prev,
  });
}

export function useAlerts(qs: string, live = true) {
  return useQuery({
    queryKey: ["alerts", qs],
    queryFn: () => api(`/alerts?${qs}`, { schema: S.AlertList }),
    refetchInterval: live ? LIVE : false,
    placeholderData: (prev) => prev,
  });
}

export function useCostSummary(period: string, group: string) {
  const qs = new URLSearchParams({ ...(period && { period }), ...(group && { group }) }).toString();
  return useQuery({ queryKey: ["costs", qs], queryFn: () => api(`/costs/summary?${qs}`, { schema: S.CostSummary }) });
}

export function useCostTrend(months = 12) {
  return useQuery({ queryKey: ["cost-trend", months], queryFn: () => api(`/costs/trend?months=${months}`, { schema: S.CostTrend }) });
}

export function useUnderutilized(enabled = true) {
  return useQuery({
    queryKey: ["underutilized"],
    queryFn: () => api("/insights/underutilized", { schema: S.Underutilized }),
    staleTime: 5 * 60_000,
    enabled,
  });
}

// Group query keys all start with "groups", so invalidating ["groups"] refreshes every view.
export function useGroups() {
  return useQuery({ queryKey: ["groups"], queryFn: () => api("/groups", { schema: z.object({ items: z.array(S.Group) }) }) });
}

export function useGroupSummaries() {
  return useQuery({ queryKey: ["groups", "summary"], queryFn: () => api("/groups?summary=1", { schema: S.GroupSummaries }), refetchInterval: LIVE });
}

export function useGroup(id: string) {
  return useQuery({ queryKey: ["groups", "detail", id], queryFn: () => api(`/groups/${id}`, { schema: S.GroupDetail }), refetchInterval: LIVE });
}

/** A mutation that invalidates the given query keys on success. */
export function useWrite<TVars, TOut = unknown>(fn: (v: TVars) => Promise<TOut>, invalidate: string[][]) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => Promise.all(invalidate.map((k) => qc.invalidateQueries({ queryKey: k }))),
  });
}
