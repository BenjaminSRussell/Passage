import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Work } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";
const WORK = "0190f0a0-0000-7000-8000-000000000501";
const REMAINDER = "0190f0a0-0000-7000-8000-000000000502";
const LOC = "0190f0a0-0000-7000-8000-000000000101";

const pick: Work = {
  id: WORK,
  warehouseId: WH,
  workType: "SALES_PICK",
  status: "ASSIGNED",
  priority: 20,
  assignedUser: "picker",
  assignedAt: "2026-10-27T09:00:00Z",
  version: 2,
  createdAt: "2026-10-27T08:00:00Z",
  updatedAt: "2026-10-27T09:00:00Z",
  lines: [{ lineNo: 1, itemId: "BOLT", direction: "PICK", qty: "10", confirmedQty: "0", weight: "10", volume: "0", lps: 0, fromLocationId: LOC, status: "OPEN" }],
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("WorkListPage", () => {
  it("lists work and opens a work", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/warehouse/work") return json(200, { items: [{ ...pick, lines: [] }] });
      if (url.pathname === `/api/v1/warehouse/work/${WORK}`) return json(200, pick);
      return undefined;
    };
    const { container } = renderAt(`/warehouse/work?warehouse=${WH}`);
    const table = await findTable({ name: "Work" });
    await waitFor(() => expect(table).toHaveTextContent("Sales pick"));
    expect(table).toHaveTextContent("Assigned");
    expect(table).toHaveTextContent("picker");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "0190f0a0" }));
    expect(await screen.findByRole("heading", { name: "Work 0190f0a0" })).toBeInTheDocument();
  });

  it("filters by status and shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt(`/warehouse/work?warehouse=${WH}`);
    expect(await screen.findByText("No work")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "BLOCKED_NO_LOCATION" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("BLOCKED_NO_LOCATION"));
  });

  it("assigns the next work and opens it, or says nothing is waiting", async () => {
    let next: Work | null = null;
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/warehouse/work:assign") return json(200, { work: next });
      if (url.pathname === "/api/v1/warehouse/work" && init.method !== "POST") return json(200, { items: [] });
      if (url.pathname === `/api/v1/warehouse/work/${WORK}`) return json(200, pick);
      return undefined;
    };
    renderAt(`/warehouse/work?warehouse=${WH}`);
    await screen.findByText("No work");
    fireEvent.click(screen.getByRole("button", { name: "Assign next work to me" }));
    expect(await screen.findByText("No work is waiting in this warehouse.")).toBeInTheDocument();
    const first = server.calls.find((c) => c.url.pathname.endsWith("work:assign"));
    expect(first?.body).toEqual({ warehouseId: WH });
    expect(first && idempotencyKey(first)).toBeTruthy();

    next = pick;
    fireEvent.click(screen.getByRole("button", { name: "Assign next work to me" }));
    expect(await screen.findByRole("heading", { name: "Work 0190f0a0" })).toBeInTheDocument();
    const assigns = server.calls.filter((c) => c.url.pathname.endsWith("work:assign"));
    expect(idempotencyKey(assigns[1])).not.toBe(idempotencyKey(assigns[0]));
  });

  it("shows why assignment failed", async () => {
    server.handler = (url) => (url.pathname.endsWith("work:assign") ? json(403, { code: "AUTH-0003", detail: "missing duty warehouse.work.execute" }) : json(200, { items: [] }));
    renderAt(`/warehouse/work?warehouse=${WH}`);
    await screen.findByText("No work");
    fireEvent.click(screen.getByRole("button", { name: "Assign next work to me" }));
    expect(await screen.findByText("missing duty warehouse.work.execute (AUTH-0003)")).toBeInTheDocument();
  });
});

describe("NewWorkPage", () => {
  it("validates the lines, then creates the work and shows where each line goes", async () => {
    const created: Work = { ...pick, status: "BLOCKED_NO_LOCATION", lines: [{ ...pick.lines[0], fromLocationId: undefined, reason: "WMS-0101" }, { ...pick.lines[0], lineNo: 2 }] };
    server.handler = (_url, init) =>
      init.method === "POST"
        ? json(201, {
            work: created,
            traces: [
              { blocked: true, code: "WMS-0101", trace: [{ directive: "D1", band: 1, action: 1, outcome: "miss", reason: "action 1 fifo: 1 short of stock" }] },
              { blocked: false, directive: "0190f0a0-0000-7000-8000-000000000201", trace: [{ directive: "D1", outcome: "hit", reason: "chose P-01" }] },
            ],
          })
        : undefined;
    const { container } = renderAt(`/warehouse/work/new?warehouse=${WH}`);
    fireEvent.change(screen.getByLabelText("Work type"), { target: { value: "SALES_PICK" } });
    fireEvent.click(screen.getByRole("button", { name: "Create work" }));
    expect(await screen.findByText("Enter the item.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Line 1 item", "BOLT");
    fill("Line 1 quantity", "10");
    fireEvent.change(screen.getByLabelText("Line 1 direction"), { target: { value: "PICK" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByLabelText("Line 2 direction")).toHaveValue("PICK");
    fill("Line 2 item", "NUT");
    fill("Line 2 quantity", "2.5");
    fill("Line 2 weight (kg)", "1.25");
    fill("Priority", "5");
    fireEvent.click(screen.getByRole("button", { name: "Create work" }));

    expect(await screen.findByText(/No directive could place every line/)).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Planned lines" });
    expect(table).toHaveTextContent("WMS-0101");
    expect(table).toHaveTextContent("chose P-01");
    expect(server.calls[0].body).toEqual({
      warehouseId: WH,
      workType: "SALES_PICK",
      priority: 5,
      lines: [
        { itemId: "BOLT", direction: "PICK", qty: "10" },
        { itemId: "NUT", direction: "PICK", qty: "2.5", weight: "1.25" },
      ],
    });
    expect(idempotencyKey(server.calls[0])).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open work" })).toHaveAttribute("href", `/warehouse/work/${WORK}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the service's refusal", async () => {
    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { workType: "is not a warehouse work type" } });
    renderAt(`/warehouse/work/new?warehouse=${WH}`);
    fill("Line 1 item", "BOLT");
    fill("Line 1 quantity", "1");
    fireEvent.click(screen.getByRole("button", { name: "Create work" }));
    expect(await screen.findByText(/request failed validation \(COM-0001\): workType is not a warehouse work type/)).toBeInTheDocument();
  });
});

describe("WorkDetailPage", () => {
  it("shows a loading state, then the work and its lines", async () => {
    server.handler = () => json(200, pick);
    const { container } = renderAt(`/warehouse/work/${WORK}`);
    expect(screen.getByText("Loading the work…")).toBeInTheDocument();
    const lines = await findTable({ name: "Work lines" });
    expect(lines).toHaveTextContent("BOLT");
    expect(screen.getByText("picker")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("confirms a line with the work version and an idempotency key", async () => {
    server.handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/lines/1:confirm")
        ? json(200, { ...pick, status: "COMPLETED", version: 3, lines: [{ ...pick.lines[0], status: "COMPLETED", confirmedQty: "10" }] })
        : json(200, pick);
    renderAt(`/warehouse/work/${WORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm line 1" }));
    expect(await screen.findByText("Line 1 confirmed: 10 BOLT.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ qty: "10" });
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"2"');
    expect(post && idempotencyKey(post)).toBeTruthy();
    expect(screen.getAllByText("Completed").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Confirm line 1" })).not.toBeInTheDocument();
  });

  it("records a short pick and links the remainder work (AC-31.5)", async () => {
    const parent = { ...pick, status: "PARTIALLY_COMPLETED" as const, version: 3, lines: [{ ...pick.lines[0], status: "SHORT" as const, confirmedQty: "6", reason: "EMPTY" }] };
    server.handler = (_url, init) =>
      init.method === "POST" ? json(200, { work: parent, remainder: { ...pick, id: REMAINDER, status: "OPEN", parentWorkId: WORK } }) : json(200, pick);
    renderAt(`/warehouse/work/${WORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Short pick line 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Record short pick" }));
    expect(await screen.findByText("Enter the quantity picked (0 or more) and a reason. (COM-0001)")).toBeInTheDocument();
    fill("Quantity picked", "6");
    fill("Reason", "EMPTY");
    fireEvent.click(screen.getByRole("button", { name: "Record short pick" }));
    expect(await screen.findByText(/Line 1 short picked at 6\./)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "0190f0a0" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/warehouse/work/${WORK}:exception`);
    expect(post?.body).toEqual({ action: "SHORT_PICK", lineNo: 1, confirmedQty: "6", reason: "EMPTY" });
    expect(screen.getByRole("table", { name: "Work lines" })).toHaveTextContent("Short");
  });

  it("cancels with a reason and shows a refused cancellation", async () => {
    let refuse = true;
    server.handler = (_url, init) => {
      if (init.method !== "POST") return json(200, { ...pick, status: "OPEN", assignedUser: undefined });
      return refuse
        ? json(403, { code: "WMS-NOT-ASSIGNEE", detail: "work is assigned to another user" })
        : json(200, { work: { ...pick, status: "CANCELLED", lines: [{ ...pick.lines[0], status: "CANCELLED", reason: "DAMAGED" }] }, remainder: null });
    };
    renderAt(`/warehouse/work/${WORK}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel work" }));
    expect(await screen.findByText("Enter why the work is cancelled. (COM-0001)")).toBeInTheDocument();
    fill("Cancellation reason", "DAMAGED");
    fireEvent.click(screen.getByRole("button", { name: "Cancel work" }));
    expect(await screen.findByText("work is assigned to another user (WMS-NOT-ASSIGNEE)")).toBeInTheDocument();
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Cancel work" }));
    expect(await screen.findByText("Work cancelled.")).toBeInTheDocument();
    const posts = server.calls.filter((c) => c.method === "POST");
    expect(idempotencyKey(posts[1])).toBe(idempotencyKey(posts[0]));
    expect(posts[1].body).toEqual({ action: "CANCEL", reason: "DAMAGED" });
    expect(screen.queryByRole("button", { name: "Cancel work" })).not.toBeInTheDocument();
  });

  it("reports a work that cannot be loaded", async () => {
    server.handler = () => json(404, { code: "WMS-WORK-NOT-FOUND", detail: "not found" });
    renderAt(`/warehouse/work/${WORK}`);
    expect(await screen.findByText("Could not load the work")).toBeInTheDocument();
    expect(screen.getByText("not found (WMS-WORK-NOT-FOUND)")).toBeInTheDocument();
  });
});
