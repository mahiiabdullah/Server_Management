import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

vi.mock("@/lib/queries", () => ({
  useGroups: () => ({
    data: {
      items: [
        { id: "g1", name: "puku-bot", description: null, server_count: 1 },
        { id: "g2", name: "puku-web-chat", description: null, server_count: 2 },
      ],
    },
  }),
  useRole: () => ({ isOperator: true, isAdmin: false }),
  useServers: () => ({ data: { items: [] } }),
  useWrite: () => ({ mutate: vi.fn(), isPending: false }),
}));

import { GroupChips, GroupPicker } from "@/components/groups";
import { Server } from "@/lib/schemas";

afterEach(cleanup);

describe("group chips", () => {
  it("links every group of a server", () => {
    render(
      <GroupChips
        groups={[
          { id: "g1", name: "puku-bot" },
          { id: "g2", name: "puku-web-chat" },
        ]}
      />,
    );
    const links = screen.getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["puku-bot", "puku-web-chat"]);
    expect(links[1].getAttribute("href")).toBe("/groups/g2");
  });

  it("shows a placeholder for a server without groups", () => {
    render(<GroupChips groups={[]} empty="only here" />);
    expect(screen.getByText("only here")).toBeTruthy();
  });
});

function Picker({ initial }: { initial: string[] }) {
  const [v, setV] = useState(initial);
  return (
    <>
      <GroupPicker id="p" value={v} onChange={setV} />
      <output data-testid="value">{v.join(",")}</output>
    </>
  );
}

describe("group picker", () => {
  it("selects several groups at once", () => {
    render(<Picker initial={["g1"]} />);
    const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes.map((b) => b.checked)).toEqual([true, false]);
    fireEvent.click(boxes[1]);
    expect(screen.getByTestId("value").textContent).toBe("g1,g2");
    expect(screen.getByText(/2 selected/)).toBeTruthy();
    fireEvent.click(boxes[0]);
    expect(screen.getByTestId("value").textContent).toBe("g2");
  });

  it("offers a new-group box to operators", () => {
    render(<Picker initial={[]} />);
    expect(screen.getByLabelText("New group name")).toBeTruthy();
    expect(screen.getByText(/No group selected/)).toBeTruthy();
  });
});

describe("server schema", () => {
  it("accepts a server in several groups", () => {
    const s = Server.parse({
      id: "s1", name: "bm1", hostname: "bm1", exporter_url: "http://x:9100/metrics", target_addr: "x:9100", environment: "production",
      location: null, groups: [{ id: "g1", name: "puku-bot" }, { id: "g2", name: "puku-web-chat" }], enabled: true, scrape_interval_s: 30,
      cpu_cores: null, memory_bytes: null, disk_bytes: null, notes: null, labels: {}, created_at: "", updated_at: "", status: "online",
      metrics: null, monthly_cost: null,
    });
    expect(s.groups).toHaveLength(2);
  });
});
