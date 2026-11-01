import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CapacityBlock, Network } from "./api/client";
import { conditionText, validateBlockEdit } from "./CapacityBlockDetailPage";
import { validateNetworkEdit } from "./NetworkDetailPage";
import scheduling from "./module";

const BLOCK = "b1c00000-0000-4000-8000-000000000001";
const NET = "e7a00000-0000-4000-8000-000000000001";
const MIX = "3f1c0000-0000-4000-8000-000000000001";

type Reply = { status: number; body?: unknown; headers?: Record<string, string> };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined, headers: new Headers(init.headers) });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: "no fake" } };
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

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

const block: CapacityBlock = {
  id: BLOCK,
  resourceId: MIX,
  code: "MON-WHITE",
  windows: [{ weekday: 1, startMinute: 360, endMinute: 840 }],
  condition: [{ attribute: "color", op: "=", value: "white" }],
  priority: 10,
  active: true,
  version: 2,
  createdAt: "2026-10-01T00:00:00Z",
};

const network: Network = {
  id: NET,
  code: "MIX-FILL",
  name: "Mixing to filling",
  activeFrom: "2026-10-01",
  activeTo: null,
  edges: [],
  version: 1,
  createdAt: "2026-10-01T00:00:00Z",
};

describe("validateBlockEdit", () => {
  const same = { priority: "10", active: true, condition: conditionText(block) };
  it("sends only what changed", () => {
    expect(validateBlockEdit({ ...same, priority: "40" }, block).patch).toEqual({ priority: 40 });
    expect(validateBlockEdit({ ...same, active: false }, block).patch).toEqual({ active: false });
    expect(validateBlockEdit({ ...same, condition: "" }, block).patch).toEqual({ condition: [] });
    expect(validateBlockEdit({ ...same, condition: "color = black\ngrade >= 2" }, block).patch?.condition).toHaveLength(2);
  });
  it("mirrors the service rules", () => {
    expect(validateBlockEdit(same, block).errors.form).toBe("Nothing changed.");
    expect(validateBlockEdit({ ...same, priority: "1001" }, block).errors.priority).toBeTruthy();
    expect(validateBlockEdit({ ...same, priority: "" }, block).errors.priority).toBeTruthy();
    expect(validateBlockEdit({ ...same, condition: "color" }, block).errors.condition).toBeTruthy();
  });
});

describe("validateNetworkEdit", () => {
  const same = { name: network.name, activeTo: "" };
  it("sends only what changed", () => {
    expect(validateNetworkEdit({ ...same, name: " Line " }, network).patch).toEqual({ name: "Line" });
    expect(validateNetworkEdit({ ...same, activeTo: "2026-12-31" }, network).patch).toEqual({ activeTo: "2026-12-31" });
  });
  it("mirrors the service rules", () => {
    expect(validateNetworkEdit(same, network).errors.form).toBe("Nothing changed.");
    expect(validateNetworkEdit({ ...same, name: " " }, network).errors.name).toBeTruthy();
    expect(validateNetworkEdit({ ...same, activeTo: "2026-09-30" }, network).errors.activeTo).toBeTruthy();
    expect(validateNetworkEdit({ ...same, activeTo: "31/12/2026" }, network).errors.activeTo).toBeTruthy();
    expect(validateNetworkEdit(same, { ...network, activeTo: "2026-12-31" }).errors.activeTo).toBeTruthy();
  });
});

describe("Capacity block detail", () => {
  it("loads, saves with If-Match and uses the new version for the next save", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...block, active: false, priority: 40, version: 3, affectedJobs: [] } };
      if (url.pathname.endsWith(`/capacity-blocks/${BLOCK}`)) return { status: 200, body: block };
    };
    const { container } = renderAt(`/scheduling/capacity-blocks/${BLOCK}`);
    const priority = await screen.findByLabelText(/Priority/);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Nothing changed.")).toBeTruthy();
    fireEvent.change(priority, { target: { value: "40" } });
    fireEvent.click(screen.getByLabelText("Active"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/The block was saved \(version 3\)/)).toBeTruthy();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/scheduling/capacity-blocks/${BLOCK}`);
    expect(patch.headers.get("If-Match")).toBe('"2"');
    expect(patch.headers.get("Idempotency-Key")).toBeTruthy();
    expect(patch.body).toEqual({ priority: 40, active: false });
    fireEvent.change(priority, { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"3"');
  });

  it("lists the booked jobs the change keeps out of the block", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH")
        return {
          status: 200,
          body: {
            ...block,
            condition: [{ attribute: "color", op: "=", value: "black" }],
            version: 3,
            affectedJobs: [{ jobId: "j1", orderId: "0a000000-0000-4000-8000-000000000001", operNumber: 10, start: "2027-01-04T08:00:00Z", end: "2027-01-04T10:00:00Z", firmed: true }],
          },
        };
      if (url.pathname.endsWith(`/capacity-blocks/${BLOCK}`)) return { status: 200, body: block };
    };
    renderAt(`/scheduling/capacity-blocks/${BLOCK}`);
    fireEvent.change(await screen.findByLabelText(/^Condition$/), { target: { value: "color = black" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("1 booked job(s) now sit in a block they may not use")).toBeTruthy();
    const table = screen.getByRole("table", { name: "Jobs kept out by the blocks" });
    expect(table.textContent).toContain("Yes");
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ condition: [{ attribute: "color", op: "=", value: "black" }] });
  });

  it("shows a stale version as its own message and reloads the latest version", async () => {
    let current = block;
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 412, body: { code: "SCHED-0127", detail: "capacity block was changed by another user; current version is 4" } };
      if (url.pathname.endsWith(`/capacity-blocks/${BLOCK}`)) return { status: 200, body: current };
    };
    renderAt(`/scheduling/capacity-blocks/${BLOCK}`);
    fireEvent.change(await screen.findByLabelText(/Priority/), { target: { value: "99" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The block changed since you opened it")).toBeTruthy();
    current = { ...block, priority: 77, version: 4 };
    fireEvent.click(screen.getByRole("button", { name: "Load the latest version" }));
    await waitFor(() => expect((screen.getByLabelText(/Priority/) as HTMLInputElement).value).toBe("77"));
  });

  it("shows an error with a retry when the block cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SCHED-0126", detail: "capacity block not found" } });
    renderAt(`/scheduling/capacity-blocks/${BLOCK}`);
    expect(await screen.findByText("Could not load the capacity block")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("Network detail edit", () => {
  it("renames and ends the network with If-Match, then shows the new end date", async () => {
    let current = network;
    handler = (url, init) => {
      if (init.method === "PATCH") {
        current = { ...network, name: "Mixing line", activeTo: "2026-12-31", version: 2 };
        return { status: 200, body: current };
      }
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith(`/networks/${NET}`)) return { status: 200, body: current };
    };
    renderAt(`/scheduling/networks/${NET}`);
    const name = await screen.findByLabelText(/^Name/);
    fireEvent.change(name, { target: { value: "Mixing line" } });
    fireEvent.change(screen.getByLabelText(/Active until/), { target: { value: "2026-12-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The network was saved (version 2).")).toBeTruthy();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/scheduling/networks/${NET}`);
    expect(patch.headers.get("If-Match")).toBe('"1"');
    expect(patch.body).toEqual({ name: "Mixing line", activeTo: "2026-12-31" });
    expect(await screen.findByText(/Active from 2026-10-01 to 2026-12-31/)).toBeTruthy();
  });

  it("shows a stale version as its own message and reloads the latest version into the form", async () => {
    let current = network;
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 412, body: { code: "SCHED-0128", detail: "resource network was changed by another user; current version is 3" } };
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith(`/networks/${NET}`)) return { status: 200, body: current };
    };
    renderAt(`/scheduling/networks/${NET}`);
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "Mine" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The network changed since you opened it")).toBeTruthy();
    current = { ...network, name: "Theirs", version: 3 };
    fireEvent.click(screen.getByRole("button", { name: "Load the latest version" }));
    await waitFor(() => expect((screen.getByLabelText(/^Name/) as HTMLInputElement).value).toBe("Theirs"));
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Mine again" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1].headers.get("If-Match")).toBe('"3"');
  });

  it("shows the service's refusal of an end date before the start", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 422, body: { code: "SCHED-0129", detail: "activeTo must be on or after the network's start date 2026-10-01" } };
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith(`/networks/${NET}`)) return { status: 200, body: network };
    };
    renderAt(`/scheduling/networks/${NET}`);
    fireEvent.change(await screen.findByLabelText(/Active until/), { target: { value: "2026-10-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/SCHED-0129/)).toBeTruthy();
  });
});
