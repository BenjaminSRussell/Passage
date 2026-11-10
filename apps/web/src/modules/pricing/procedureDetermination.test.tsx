import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Procedure, ProcedureAssignment, ResolveResponse } from "./api/client";
import { referenceSteps } from "./ProcedureStepsEditor";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ADMIN = ["pricing.view", "pricing.admin"];

const std: Procedure = { id: "0190f0a0-0000-7000-8000-000000000d01", legalEntityId: LE, name: "AX_STANDARD", version: 2, status: "ACTIVE", revision: 1, steps: referenceSteps() };
const old: Procedure = { ...std, id: "0190f0a0-0000-7000-8000-000000000d00", version: 1, status: "SUPERSEDED" };
const webOnly: Procedure = { ...std, id: "0190f0a0-0000-7000-8000-000000000d02", name: "WEB_PRICING", version: 1 };
const draft: Procedure = { ...std, id: "0190f0a0-0000-7000-8000-000000000d03", name: "DRAFT_ONLY", version: 1, status: "DRAFT" };
const dflt: ProcedureAssignment = { legalEntityId: LE, salesChannel: "", procedureName: "AX_STANDARD", version: 1, updatedBy: "ana" };
const web: ProcedureAssignment = { legalEntityId: LE, salesChannel: "WEB", procedureName: "WEB_PRICING", version: 3 };

function load() {
  fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
  fireEvent.click(screen.getByRole("button", { name: "Load assignments" }));
}

/** Answers the list reads from the given state, and records writes through the callbacks. */
function backend(rows: () => ProcedureAssignment[], onWrite?: (method: string, url: URL, body: unknown) => ReturnType<typeof json>) {
  server.handler = (url, init) => {
    if (url.pathname.endsWith("/procedure-assignments")) {
      if (init.method === "PUT" || init.method === "DELETE") return onWrite?.(init.method, url, init.body ? JSON.parse(String(init.body)) : undefined);
      return json(200, { items: rows() });
    }
    if (url.pathname.endsWith("/procedures")) return json(200, { items: [std, old, webOnly, draft] });
    return undefined;
  };
}

describe("Procedure determination", () => {
  it("starts empty and validates the legal entity id", async () => {
    const { container } = renderAt("/pricing/procedure-determination", ADMIN);
    expect(screen.getByText("No legal entity loaded")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load assignments" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("lists the assignments and offers only active procedures", async () => {
    backend(() => [dflt, web]);
    const { container } = renderAt("/pricing/procedure-determination", ADMIN);
    load();
    const table = await findTable({ name: "Procedure assignments" });
    expect(within(table).getByText("All channels (default)")).toBeInTheDocument();
    expect(within(table).getByText("WEB_PRICING")).toBeInTheDocument();
    const options = within(screen.getByLabelText("Procedure")).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["AX_STANDARD", "WEB_PRICING"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state when nothing is assigned", async () => {
    backend(() => []);
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    expect(await screen.findByText("Nothing assigned")).toBeInTheDocument();
  });

  it("saves a new channel at version 0 and an existing one at the version read", async () => {
    let rows = [dflt];
    backend(
      () => rows,
      (_m, _u, body) => {
        const b = body as { salesChannel: string; procedureName: string; version: number };
        rows = [...rows.filter((r) => r.salesChannel !== b.salesChannel), { legalEntityId: LE, salesChannel: b.salesChannel, procedureName: b.procedureName, version: b.version + 1 }];
        return json(200, rows[rows.length - 1]);
      },
    );
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await findTable({ name: "Procedure assignments" });
    fireEvent.change(screen.getByLabelText(/^Sales channel/), { target: { value: "WEB" } });
    fireEvent.change(screen.getByLabelText("Procedure"), { target: { value: "WEB_PRICING" } });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await findStatus()).toHaveTextContent("WEB now prices with WEB_PRICING.");
    const first = server.calls.find((c) => c.method === "PUT")!;
    expect(first.body).toEqual({ legalEntityId: LE, salesChannel: "WEB", procedureName: "WEB_PRICING", version: 0 });
    expect(idempotencyKey(first)).toBeTruthy();
    // The default exists at version 1, so changing it sends that version.
    fireEvent.change(screen.getByLabelText(/^Sales channel/), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Procedure"), { target: { value: "WEB_PRICING" } });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    const second = server.calls.filter((c) => c.method === "PUT")[1];
    expect(second.body).toEqual({ legalEntityId: LE, salesChannel: "", procedureName: "WEB_PRICING", version: 1 });
    expect(idempotencyKey(second)).not.toBe(idempotencyKey(first));
  });

  it("checks the channel before calling the service", async () => {
    backend(() => []);
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await screen.findByText("Nothing assigned");
    fireEvent.change(screen.getByLabelText(/^Sales channel/), { target: { value: "web shop" } });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await screen.findByText(/Use up to 32 upper-case letters/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("shows the service refusal with its code", async () => {
    backend(
      () => [],
      () => json(422, { code: "PRC-0063", detail: "pricing procedure AX_STANDARD has no active version" }),
    );
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await screen.findByText("Nothing assigned");
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await findAlert()).toHaveTextContent("PRC-0063");
  });

  it("retries a failed save with its key and gives another command a fresh one", async () => {
    let failures = 1;
    backend(
      () => [dflt],
      (method) => {
        if (method === "PUT" && failures-- > 0) return json(503, { code: "COM-0500", detail: "internal error" });
        return method === "PUT" ? json(200, dflt) : { status: 204, body: null };
      },
    );
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await findTable({ name: "Procedure assignments" });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
    // The removal is another command: it must not reuse the key of the save that failed.
    fireEvent.click(screen.getByRole("button", { name: "Remove All channels (default)" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove assignment" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "DELETE")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    const [failed, retried] = server.calls.filter((c) => c.method === "PUT");
    const removal = server.calls.find((c) => c.method === "DELETE")!;
    expect(idempotencyKey(removal)).not.toBe(idempotencyKey(failed));
    expect(idempotencyKey(retried)).not.toBe(idempotencyKey(removal));
  });

  it("reuses the key when the same save is retried after a server failure", async () => {
    let failures = 1;
    backend(
      () => [dflt],
      () => (failures-- > 0 ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, dflt)),
    );
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await findTable({ name: "Procedure assignments" });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await findStatus()).toHaveTextContent("The default now prices with AX_STANDARD.");
    const [first, retry] = server.calls.filter((c) => c.method === "PUT");
    expect(idempotencyKey(retry)).toBe(idempotencyKey(first));
  });

  it("removes an assignment after confirmation, at the version read", async () => {
    let rows = [dflt, web];
    backend(
      () => rows,
      (method, url) => {
        expect(method).toBe("DELETE");
        expect(url.searchParams.get("salesChannel")).toBe("WEB");
        expect(url.searchParams.get("version")).toBe("3");
        rows = [dflt];
        return { status: 204, body: null };
      },
    );
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    await findTable({ name: "Procedure assignments" });
    fireEvent.click(screen.getByRole("button", { name: "Remove WEB" }));
    fireEvent.click(await screen.findByRole("button", { name: "Remove assignment" }));
    expect(await findStatus()).toHaveTextContent("WEB was removed.");
    expect(screen.queryByText("WEB_PRICING", { selector: "td" })).toBeNull();
  });

  it("reads without pricing.admin but offers no change", async () => {
    backend(() => [dflt]);
    renderAt("/pricing/procedure-determination", ["pricing.view"]);
    load();
    await findTable({ name: "Procedure assignments" });
    expect(screen.queryByRole("button", { name: "Save assignment" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Remove/ })).toBeNull();
    expect(screen.getByText("Changing assignments needs the pricing.admin duty.")).toBeInTheDocument();
  });

  it("explains when no procedure is active", async () => {
    server.handler = (url) => (url.pathname.endsWith("/procedures") ? json(200, { items: [draft] }) : json(200, { items: [] }));
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    expect(await screen.findByText("No active procedure")).toBeInTheDocument();
  });

  it("shows a load failure", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/pricing/procedure-determination", ADMIN);
    load();
    expect(await findAlert()).toHaveTextContent("COM-0500");
  });
});

const priced: ResolveResponse = {
  currency: "USD",
  lines: [
    {
      lineNo: 1,
      unit: "EA",
      quantity: "1",
      unitPrice: "100",
      netUnitPrice: "88",
      lineNet: "88",
      applied: [
        { type: "PRICE", recordId: "p", access: 9, stepNo: 10 },
        { type: "MULTILINE_DISC", recordId: "m", access: 9, stepNo: 30 },
        { type: "LINE_DISC", recordId: "s", access: 9, stepNo: 40, statistical: true, amount: "-5.00" },
      ],
      trace: [
        { condition: "LINE_DISC", stepNo: 20, reason: "excluded: BEST of exclusion group DISC kept step 30", accesses: [{ seq: 9, access: "all x all", evaluated: true, candidates: ["l"], winner: "l", reason: "winner at this access: search stops" }] },
      ],
    },
  ],
  subtotal: "88",
  totalDiscount: "0",
  surcharge: "0",
  orderNet: "88",
  applied: [],
  procedure: { id: "0190f0a0-0000-7000-8000-000000000d01", name: "AX_STANDARD", version: 2 },
  explainRef: `sha256:${"b".repeat(64)}`,
  freeGoods: [],
};

describe("Price simulation under a procedure", () => {
  it("sends the channel and procedure, names the version and shows why a member lost", async () => {
    server.handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/resolve") ? json(200, priced) : undefined);
    renderAt("/pricing/simulate");
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "C1" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "BOLT" } });
    fireEvent.change(screen.getByLabelText(/^Sales channel/), { target: { value: "WEB" } });
    fireEvent.change(screen.getByLabelText("Procedure"), { target: { value: "AX_STANDARD" } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByRole("list", { name: "Simulation totals" })).toHaveTextContent("Procedure AX_STANDARD v2");
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toMatchObject({ salesChannel: "WEB", procedure: "AX_STANDARD" });
    expect(screen.getByRole("table", { name: "Priced lines" })).toHaveTextContent("Multiline discount (step 30, access 9)");
    expect(screen.getByRole("table", { name: "Priced lines" })).toHaveTextContent("statistical -5.00");
    expect(screen.getByRole("table", { name: "Explain line 1" })).toHaveTextContent("excluded: BEST of exclusion group DISC kept step 30");
  });

  it("reads Reference order when no procedure applied", async () => {
    const { procedure: _drop, ...plain } = priced;
    server.handler = () => json(200, plain);
    renderAt("/pricing/simulate");
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "C1" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "BOLT" } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByRole("list", { name: "Simulation totals" })).toHaveTextContent("Procedure Reference order");
  });
});
