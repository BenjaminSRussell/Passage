import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { Batch, StoredAttributeDefinition } from "./api/client";
import { DISPOSITION_DUTY } from "./BatchRecordPages";
import processmfg from "./module";

const BATCH = "b1000000-0000-4000-8000-000000000001";
const ITEM = "1a000000-0000-4000-8000-000000000001";
const LE = "1e000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = []) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {processmfg.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const batch: Batch = {
  id: BATCH,
  legalEntityId: LE,
  itemId: ITEM,
  batchNumber: "B-100",
  disposition: "available",
  version: 1,
  createdBy: "qa-1",
  createdAt: "2026-10-21T08:00:00Z",
  updatedAt: "2026-10-21T08:00:00Z",
  attributes: [],
  attributeHistory: [],
  dispositions: [],
};

const defs: StoredAttributeDefinition[] = [
  { id: "d1", itemId: ITEM, name: "potency", kind: "numeric", role: "potency", min: "40", max: "60", options: [], createdBy: "qa-1", createdAt: "2026-10-21T08:00:00Z" },
  { id: "d2", itemId: ITEM, name: "grade", kind: "enum", role: "generic", options: ["A", "B"], createdBy: "qa-1", createdAt: "2026-10-21T08:00:00Z" },
  { id: "d3", itemId: ITEM, name: "expiry", kind: "date", role: "generic", options: [], createdBy: "qa-1", createdAt: "2026-10-21T08:00:00Z" },
];

function serve(current: Batch) {
  handler = (url, init) => {
    if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: defs } };
    if (url.pathname.endsWith(`/batches/${BATCH}/genealogy`)) return { status: 200, body: { batchId: BATCH, parents: [], children: [] } };
    if (url.pathname.endsWith(`/batches/${BATCH}`)) return { status: 200, body: current };
    if (url.pathname.endsWith("/attributes") && init.method === "PUT") {
      return {
        status: 200,
        body: { batch: { ...current, version: 2 }, checks: [{ name: "potency", pass: false, detail: "potency 38.5 is below the minimum 40" }] },
      };
    }
    return undefined;
  };
}

describe("batches list", () => {
  it("shows batches with their disposition and filters by it", async () => {
    handler = (url) => {
      if (!url.pathname.endsWith("/batches")) return undefined;
      const wanted = url.searchParams.get("disposition");
      const rows = [batch, { ...batch, id: "b2", batchNumber: "B-101", disposition: "rejected" as const }].filter((b) => !wanted || b.disposition === wanted);
      return { status: 200, body: { items: rows } };
    };
    const { container } = renderAt("/processmfg/batches");
    const table = await findTable({ name: "Batches" });
    expect(within(table).getByText("B-100")).toBeTruthy();
    expect(within(table).getByText("Rejected")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Disposition"), { target: { value: "rejected" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("disposition") === "rejected")).toBe(true));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Batches" })).queryByText("B-100")).toBeNull());
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/batches");
    expect(await screen.findByText("No batches yet")).toBeTruthy();
  });

  it("shows the error with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } });
    renderAt("/processmfg/batches");
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not load batches");
    expect(within(alert).getByText(/COM-0500/)).toBeTruthy();
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("register batch", () => {
  it("validates, then registers with an Idempotency-Key and opens the batch", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/batches") && init.method === "POST") return { status: 201, body: batch };
      if (url.pathname.endsWith(`/batches/${BATCH}`)) return { status: 200, body: batch };
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: [] } };
      return undefined;
    };
    renderAt("/processmfg/batches/new");
    fireEvent.click(screen.getByRole("button", { name: "Register batch" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeTruthy();
    expect(screen.getByText("Enter a batch number of 1 to 40 characters.")).toBeTruthy();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Batch number"), { target: { value: "B-100" } });
    fireEvent.click(screen.getByRole("button", { name: "Register batch" }));
    await screen.findByRole("heading", { name: "B-100" });
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ itemId: ITEM, batchNumber: "B-100", disposition: "available" });
    expect((post.init.headers as Headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows a duplicate batch number refusal", async () => {
    handler = () => ({ status: 409, body: { code: "PMF-0109", detail: "batch B-100 of item x already exists" } });
    renderAt("/processmfg/batches/new");
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Batch number"), { target: { value: "B-100" } });
    fireEvent.click(screen.getByRole("button", { name: "Register batch" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not register the batch");
    expect(within(alert).getByText(/PMF-0109/)).toBeTruthy();
  });
});

describe("batch detail", () => {
  it("records attributes with the version and flags values outside limits", async () => {
    serve(batch);
    renderAt(`/processmfg/batches/${BATCH}`);
    await screen.findByRole("heading", { name: "B-100" });
    fireEvent.change(await screen.findByLabelText(/^potency/), { target: { value: "38.5" } });
    fireEvent.change(screen.getByLabelText(/^grade/), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Record attributes" }));
    const status = await findStatus();
    expect(status.textContent).toMatch(/outside limits/);
    expect(status.textContent).toContain("potency 38.5 is below the minimum 40");
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ version: 1, values: { potency: { num: "38.5" }, grade: { text: "A" } } });
    expect((put.init.headers as Headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("refuses an empty or malformed entry without calling the service", async () => {
    serve(batch);
    renderAt(`/processmfg/batches/${BATCH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Record attributes" }));
    expect(await screen.findByText("Enter at least one value.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^potency/), { target: { value: "high" } });
    fireEvent.change(screen.getByLabelText(/^expiry/), { target: { value: "03/01/2027" } });
    fireEvent.click(screen.getByRole("button", { name: "Record attributes" }));
    expect(await screen.findByText("Enter a number with at most 6 decimals.")).toBeTruthy();
    expect(screen.getByText("Enter a date as yyyy-mm-dd.")).toBeTruthy();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("shows a stale-version refusal on recording", async () => {
    serve(batch);
    const inner = handler;
    handler = (url, init) =>
      init.method === "PUT" ? { status: 409, body: { code: "PMF-0104", detail: "batch B-100 is at version 3, not 1; reload and try again" } } : inner(url, init);
    renderAt(`/processmfg/batches/${BATCH}`);
    fireEvent.change(await screen.findByLabelText(/^potency/), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Record attributes" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not record the attributes");
    expect(within(alert).getByText(/PMF-0104/)).toBeTruthy();
    const loads = calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith(`/batches/${BATCH}`)).length;
    fireEvent.click(within(alert).getByRole("button", { name: "Reload batch" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith(`/batches/${BATCH}`)).length).toBe(loads + 1));
  });

  it("shows current attributes, history and disposition history", async () => {
    const recorded = { name: "potency", kind: "numeric" as const, num: "38.5", pass: false, detail: "potency 38.5 is below the minimum 40", recordedBy: "qa-1", recordedAt: "2026-10-21T09:00:00Z" };
    serve({
      ...batch,
      disposition: "on-hold",
      expiryDate: "2027-03-01",
      attributes: [recorded],
      attributeHistory: [recorded],
      dispositions: [
        { from: "on-hold", to: "available", reason: "my retest passed", segregationConflict: true, changedBy: "qa-1", changedAt: "2026-10-21T09:10:00Z" },
        { from: "available", to: "on-hold", reason: "failed potency", segregationConflict: false, changedBy: "qa-2", changedAt: "2026-10-21T09:05:00Z" },
      ],
    });
    renderAt(`/processmfg/batches/${BATCH}`);
    const current = await findTable({ name: "Current batch attributes" });
    expect(within(current).getByText("Outside limits")).toBeTruthy();
    const history = await findTable({ name: "Disposition history" });
    expect(within(history).getByText("failed potency")).toBeTruthy();
    const rows = within(history).getAllByRole("row");
    expect(within(rows[1]).getByText("Same user recorded attributes")).toBeTruthy();
    expect(within(rows[2]).queryByText("Same user recorded attributes")).toBeNull();
    expect(within(await findTable({ name: "Attribute history" })).getByText("38.5")).toBeTruthy();
    expect(screen.getByText("2027-03-01")).toBeTruthy();
  });

  it("changes the disposition only for a holder of the disposition duty, with a reason", async () => {
    serve(batch);
    const inner = handler;
    handler = (url, init) =>
      url.pathname.endsWith(":disposition") ? { status: 200, body: { ...batch, version: 2, disposition: "on-hold" } } : inner(url, init);
    renderAt(`/processmfg/batches/${BATCH}`, [DISPOSITION_DUTY]);
    fireEvent.click(await screen.findByRole("button", { name: "Change disposition" }));
    expect(await screen.findByText("Give a reason of 1 to 500 characters.")).toBeTruthy();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "failed potency" } });
    fireEvent.click(screen.getByRole("button", { name: "Change disposition" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ version: 1, disposition: "on-hold", reason: "failed potency" });
  });

  it("explains the missing duty instead of offering the disposition form", async () => {
    serve(batch);
    renderAt(`/processmfg/batches/${BATCH}`, ["production.batch-attribute.write"]);
    await screen.findByRole("heading", { name: "B-100" });
    expect(screen.getByText(/needs the batch disposition duty/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Change disposition" })).toBeNull();
  });

  it("shows the error with a retry when the batch cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "PMF-0108", detail: "batch x not found" } });
    renderAt(`/processmfg/batches/${BATCH}`);
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not load the batch");
    expect(within(alert).getByText(/PMF-0108/)).toBeTruthy();
  });
});

describe("attribute definitions", () => {
  it("lists definitions, validates and defines a numeric attribute", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/batch-attribute-definitions") && init.method === "POST") return { status: 201, body: defs[0] };
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: defs } };
      return undefined;
    };
    renderAt("/processmfg/attribute-definitions");
    const table = await findTable({ name: "Batch attribute definitions" });
    expect(within(table).getByText("potency")).toBeTruthy();
    expect(within(table).getByText("A, B")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Define attribute" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeTruthy();
    expect(screen.getByText("Enter a name of 1 to 40 characters.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Item id", { selector: "#def-item" }), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "potency" } });
    fireEvent.change(screen.getByLabelText("Minimum"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("Maximum"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Define attribute" }));
    expect((await findStatus()).textContent).toContain("Defined potency.");
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ itemId: ITEM, name: "potency", kind: "numeric", min: "40", max: "60" });
    expect((post.init.headers as Headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("defines the potency attribute of an item with its role", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/batch-attribute-definitions") && init.method === "POST") return { status: 201, body: defs[0] };
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: defs } };
      return undefined;
    };
    renderAt("/processmfg/attribute-definitions");
    const table = await findTable({ name: "Batch attribute definitions" });
    expect(within(table).getByText("Potency")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Item id", { selector: "#def-item" }), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "assay" } });
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "potency" } });
    fireEvent.click(screen.getByRole("button", { name: "Define attribute" }));
    await findStatus();
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ itemId: ITEM, name: "assay", kind: "numeric", role: "potency" });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "text" } });
    expect(screen.queryByLabelText("Role")).toBeNull();
  });

  it("shows the empty state and a duplicate refusal", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? { status: 409, body: { code: "PMF-0110", detail: "item already defines the attribute potency" } }
        : url.pathname.endsWith("/batch-attribute-definitions")
          ? { status: 200, body: { items: [] } }
          : undefined;
    renderAt("/processmfg/attribute-definitions");
    expect(await screen.findByText("No attribute definitions")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Item id", { selector: "#def-item" }), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "potency" } });
    fireEvent.click(screen.getByRole("button", { name: "Define attribute" }));
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not define the attribute");
    expect(within(alert).getByText(/PMF-0110/)).toBeTruthy();
  });

  it("shows the error with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } });
    renderAt("/processmfg/attribute-definitions");
    expect((await findAlert()).textContent).toContain("Could not load the definitions");
  });
});
