"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { api, isApiError } from "@/lib/api";
import { useRole, useWrite } from "@/lib/queries";
import type { Server } from "@/lib/schemas";
import { Button, Field, Input, Modal } from "./ui";

/** Enable/disable toggle (operator+). */
export function EnableToggle({ server, size = "sm" }: { server: Pick<Server, "id" | "enabled" | "name">; size?: "sm" | "md" }) {
  const { isOperator } = useRole();
  const w = useWrite(() => api(`/servers/${server.id}/${server.enabled ? "disable" : "enable"}`, { method: "POST" }), [["servers"], ["fleet"], ["server"]]);
  if (!isOperator) return null;
  return (
    <Button size={size} onClick={() => w.mutate(undefined)} disabled={w.isPending} aria-label={`${server.enabled ? "Disable" : "Enable"} monitoring of ${server.name}`}>
      {server.enabled ? "Disable" : "Enable"}
    </Button>
  );
}

/** Delete with the server name typed to confirm (admin). */
export function DeleteServer({ server, size = "sm", afterDelete }: { server: Pick<Server, "id" | "name">; size?: "sm" | "md"; afterDelete?: () => void }) {
  const { isAdmin } = useRole();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  const w = useWrite(() => api(`/servers/${server.id}`, { method: "DELETE", body: { confirm_name: typed } }), [["servers"], ["fleet"]]);
  if (!isAdmin) return null;
  return (
    <>
      <Button size={size} variant="ghost" className="text-crit" onClick={() => (setOpen(true), setTyped(""), setError(""))}>
        Delete
      </Button>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={`Delete ${server.name}?`}
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="danger"
              disabled={typed !== server.name || w.isPending}
              onClick={() =>
                w.mutate(undefined, {
                  onSuccess: () => {
                    setOpen(false);
                    if (afterDelete) afterDelete();
                    else router.push("/servers");
                  },
                  onError: (e) => setError(isApiError(e) ? e.message : "Delete failed."),
                })
              }
            >
              Delete server
            </Button>
          </>
        }
      >
        <p>
          Monitoring stops and the server leaves the inventory. Its metric history, cost records and audit trail are kept. Pricing ends now.
        </p>
        <div className="mt-4">
          <Field label={`Type ${server.name} to confirm`} error={error}>
            {(id) => <Input id={id} value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />}
          </Field>
        </div>
      </Modal>
    </>
  );
}
