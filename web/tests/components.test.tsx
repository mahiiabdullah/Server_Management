import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SeverityBadge, StatusBadge } from "@/components/status";
import { EmptyState, ErrorState, StaleBanner } from "@/components/states";
import { Meter, Pagination } from "@/components/ui";
import { ApiError } from "@/lib/api";

afterEach(cleanup);

describe("status is never colour alone", () => {
  it.each([
    ["online", "Online", "●"],
    ["offline", "Offline", "✕"],
    ["unknown", "Unknown", "?"],
    ["disabled", "Disabled", "○"],
  ] as const)("%s badge has a word and an icon", (status, word, icon) => {
    const { container } = render(<StatusBadge status={status} />);
    expect(container.textContent).toContain(word);
    expect(container.textContent).toContain(icon);
  });

  it("severity badges carry a label", () => {
    render(<SeverityBadge severity="critical" />);
    expect(screen.getByText("Critical")).toBeTruthy();
  });
});

describe("page states", () => {
  it("shows the monitoring-unavailable banner", () => {
    render(<StaleBanner available={false} asOf={null} />);
    expect(screen.getByRole("status").textContent).toMatch(/Monitoring unavailable/);
  });

  it("shows nothing when data is fresh", () => {
    const { container } = render(<StaleBanner available asOf={new Date().toISOString()} />);
    expect(container.textContent).toBe("");
  });

  it("error state offers a retry", () => {
    const retry = vi.fn();
    render(<ErrorState error={new ApiError(503, "metrics_unavailable", "Prometheus is not reachable")} onRetry={retry} />);
    expect(screen.getByRole("alert").textContent).toMatch(/Prometheus is not reachable/);
    fireEvent.click(screen.getByText("Retry"));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("403 errors render the no-permission state", () => {
    render(<ErrorState error={new ApiError(403, "forbidden", "nope")} />);
    expect(screen.getByRole("alert").textContent).toMatch(/do not have permission/);
  });

  it("empty state names the next action", () => {
    render(<EmptyState title="No servers registered yet" action={<button>Add your first server</button>} />);
    expect(screen.getByText("Add your first server")).toBeTruthy();
  });
});

describe("meter", () => {
  it("renders a dash for missing data, not 0%", () => {
    const { container } = render(<Meter value={null} label="CPU" />);
    expect(container.textContent).toBe("—");
  });

  it("exposes its value accessibly", () => {
    render(<Meter value={0.42} label="CPU" />);
    expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("42");
  });
});

describe("pagination", () => {
  it("disables previous on the first page and next on the last", () => {
    const onPage = vi.fn();
    render(<Pagination page={1} pageSize={25} total={30} onPage={onPage} />);
    expect((screen.getByText("Previous") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByText("Next"));
    expect(onPage).toHaveBeenCalledWith(2);
    expect(screen.getByText("1–25 of 30")).toBeTruthy();
  });
});
