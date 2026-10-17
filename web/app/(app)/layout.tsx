import { Suspense } from "react";
import { AppShell } from "@/components/app-shell";
import { LoadingBlock } from "@/components/states";

// Every app page is signed-in and live, so the shell (which reads the URL) streams
// in at request time behind this boundary; the static fallback is a skeleton.
export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <Suspense
      fallback={
        <div className="mx-auto max-w-md p-10">
          <LoadingBlock rows={3} />
        </div>
      }
    >
      <AppShell>{children}</AppShell>
    </Suspense>
  );
}
