import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { BlockReport, Resource } from "./api/client";
import scheduling from "./module";

const RES = "7e000000-0000-4000-8000-000000000001";
const BLOCK = "b1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: URL[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const resource = { id: RES, code: "PAINT-1", name: "Paint booth" } as Resource;

const report: BlockReport = {
  items: [
    { resourceId: RES, resourceCode: "PAINT-1", resourceName: "Paint booth", blockId: BLOCK, code: "MON-WHITE", priority: 10, windows: 2, blockMinutes: 480, matchedMinutes: 120, unusedMinutes: 360, utilizationPct: 25 },
    { resourceId: RES, resourceCode: "PAINT-1", resourceName: "Paint booth", blockId: "b1000000-0000-4000-8000-000000000002", code: "IDLE", priority: 1, windows: 1, blockMinutes: 0, matchedMinutes: 0, unusedMinutes: 0, utilizationPct: null },
  ],
  truncated: true,
  blockMinutes: 480,
  matchedMinutes: 120,
  unusedMinutes: 360,
  utilizationPct: 25,
};

function serve(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [resource] } };
  if (url.pathname.endsWith("/block-utilization")) return { status: 200, body: report };
  return undefined;
}

const reportCalls = () => calls.filter((c) => c.pathname.endsWith("/block-utilization"));

describe("Block utilization report", () => {
  it("lists the block totals per resource, shows the report totals and filters by range and resource", async () => {
    handler = serve;
    const { container } = renderAt("/scheduling/block-utilization");
    const table = await findTable({ name: "Block utilization" });
    await waitFor(() => expect(table).toHaveTextContent("MON-WHITE"));
    expect(table).toHaveTextContent("PAINT-1 Paint booth");
    expect(table).toHaveTextContent("8.0 h");
    expect(table).toHaveTextContent("25%");
    expect(table).toHaveTextContent("IDLE");
    expect(screen.getByLabelText("Totals")).toHaveTextContent("Held 8.0 h");
    expect(screen.getByLabelText("Totals")).toHaveTextContent("Utilization 25%");
    expect(screen.getByText("Showing the first 200 resources")).toBeInTheDocument();
    expect(reportCalls()[0].searchParams.get("resourceId")).toBeNull();

    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2027-03-07" } });
    fireEvent.change(await screen.findByRole("option", { name: "PAINT-1 Paint booth" }).then(() => screen.getByLabelText("Resource")), { target: { value: RES } });
    await waitFor(() => {
      const last = reportCalls().at(-1);
      expect(last?.searchParams.get("from")).toBe("2027-03-01T00:00:00Z");
      expect(last?.searchParams.get("to")).toBe("2027-03-08T00:00:00Z");
      expect(last?.searchParams.get("resourceId")).toBe(RES);
    });
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("explains an empty result, refuses an empty or over-long range without asking the service, and retries a failure", async () => {
    handler = (url) => (url.pathname.endsWith("/block-utilization") ? { status: 200, body: { items: [], truncated: false, blockMinutes: 0, matchedMinutes: 0, unusedMinutes: 0, utilizationPct: null } } : serve(url));
    const first = renderAt("/scheduling/block-utilization");
    expect(await screen.findByText("No capacity blocks in this range")).toBeInTheDocument();
    expect(screen.queryByLabelText("Totals")).toBeNull();
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2027-03-05" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2027-03-01" } });
    expect(await screen.findByText("Pick a valid range")).toBeInTheDocument();
    expect(screen.queryByText("No capacity blocks in this range")).toBeNull();
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "" } });
    expect(await screen.findByText("Pick both the first and the last day.")).toBeInTheDocument();
    const before = reportCalls().length;
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2027-03-31" } });
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2027-01-01" } });
    expect(await screen.findByText(/covers at most 62/)).toBeInTheDocument();
    expect(reportCalls().slice(before).every((c) => c.searchParams.get("from") !== "2027-01-01T00:00:00Z")).toBe(true);
    first.unmount();

    handler = (url) => (url.pathname.endsWith("/block-utilization") ? { status: 500, body: { code: "COM-0500", detail: "internal error" } } : serve(url));
    renderAt("/scheduling/block-utilization");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    handler = serve;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Block utilization" })).toBeInTheDocument();
  });
});
