import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import processmfg from "./module";
import { MERGE_DUTY } from "./StoredMergePage";

const ITEM = "1a000000-0000-4000-8000-000000000001";
const A = "5b000000-0000-4000-8000-0000000000a1";
const B = "5b000000-0000-4000-8000-0000000000a2";
const T = "5b000000-0000-4000-8000-0000000000a3";

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

function renderAt(path: string, duties: string[] = [MERGE_DUTY]) {
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

const batch = (id: string, batchNumber: string, version = 1, expiryDate?: string) => ({
  id,
  legalEntityId: "11111111-0000-4000-8000-000000000001",
  itemId: ITEM,
  batchNumber,
  disposition: "available",
  expiryDate,
  version,
  createdBy: "u",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
});

const link = (batchId: string, batchNumber: string, qty: string, unitCost: string) => ({
  batchId,
  batchNumber,
  itemId: ITEM,
  relation: "merge",
  qty,
  unitCost,
  createdBy: "u",
  createdAt: "2026-10-21T08:00:00Z",
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("merge stored batches (slice 13)", () => {
  it("sends the chosen batches with their versions, quantities and costs, then shows the new batch and parents (AC-24.12)", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/production/batches" && init.method !== "POST") {
        return { status: 200, body: { items: [batch(A, "L-A", 3, "2027-06-01"), batch(B, "L-B", 1, "2027-03-01")] } };
      }
      if (url.pathname === "/api/v1/production/batches:merge") {
        return {
          status: 201,
          body: {
            batch: { ...batch(T, "L-AB", 1, "2027-03-01"), attributes: [] },
            qty: "500",
            unitCost: "1.8",
            parents: [link(A, "L-A", "200", "1.5"), link(B, "L-B", "300", "2")],
          },
        };
      }
      return undefined;
    };
    const { container } = renderAt("/processmfg/merge-stored");
    fill("Item id", ITEM);
    const listed = await findTable({ name: "Batches of the item" });
    expect(within(listed).getByText("L-B")).toBeInTheDocument();
    fill("New batch number", " L-AB ");
    fill("Warehouse", "W1");
    fill("Text attributes allowed to differ", "grade, colour");
    fireEvent.click(screen.getByLabelText("Merge batch L-A"));
    fireEvent.click(screen.getByLabelText("Merge batch L-B"));
    fill("Quantity of batch L-A", "200");
    fill("Cost per unit of batch L-A", "1.5");
    fill("Quantity of batch L-B", "300");
    fill("Cost per unit of batch L-B", "2");
    fireEvent.click(screen.getByRole("button", { name: "Merge batches" }));
    expect(await screen.findByText("Created batch L-AB")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({
      targetBatchNumber: "L-AB",
      sources: [
        { batchId: A, version: 3, warehouseId: "W1", qty: "200", unitCost: "1.5" },
        { batchId: B, version: 1, warehouseId: "W1", qty: "300", unitCost: "2" },
      ],
      mergeOverride: ["grade", "colour"],
    });
    expect(screen.getByRole("list", { name: "Merged batch" })).toHaveTextContent("Quantity 500");
    const parents = screen.getByRole("table", { name: "Parent batches" });
    expect(within(parents).getByRole("link", { name: "L-A" })).toHaveAttribute("href", `/processmfg/batches/${A}`);
    expect(screen.getByRole("link", { name: "Open the new batch" })).toHaveAttribute("href", `/processmfg/batches/${T}`);
    await waitFor(() => expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]));
  });

  it("validates before calling and shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/processmfg/merge-stored");
    fireEvent.click(screen.getByRole("button", { name: "Merge batches" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter a batch number of 1 to 40 characters.")).toBeInTheDocument();
    expect(screen.getByText("Enter the warehouse of the batches.")).toBeInTheDocument();
    fill("Item id", ITEM);
    expect(await screen.findByText("No batches registered for this item")).toBeInTheDocument();
    fill("New batch number", "X");
    fill("Warehouse", "W1");
    fireEvent.click(screen.getByRole("button", { name: "Merge batches" }));
    expect(await screen.findByText("Select at least two batches to merge.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("checks the quantity and cost of each chosen batch", async () => {
    handler = () => ({ status: 200, body: { items: [batch(A, "L-A"), batch(B, "L-B")] } });
    renderAt("/processmfg/merge-stored");
    fill("Item id", ITEM);
    await findTable({ name: "Batches of the item" });
    fill("New batch number", "X");
    fill("Warehouse", "W1");
    fireEvent.click(screen.getByLabelText("Merge batch L-A"));
    fireEvent.click(screen.getByLabelText("Merge batch L-B"));
    fill("Quantity of batch L-A", "0");
    fireEvent.click(screen.getByRole("button", { name: "Merge batches" }));
    expect(await screen.findByText(/Enter a quantity above zero\./)).toBeInTheDocument();
    expect(screen.getByText(/Enter the cost per unit\./)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows a refusal from the service, with a reload for a stale version", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":merge")) return { status: 409, body: { code: "PMF-0104", detail: "batch L-A is at version 4, not 1; reload and try again" } };
      if (init.method === undefined || init.method === "GET") return { status: 200, body: { items: [batch(A, "L-A"), batch(B, "L-B")] } };
      return undefined;
    };
    renderAt("/processmfg/merge-stored");
    fill("Item id", ITEM);
    await findTable({ name: "Batches of the item" });
    fill("New batch number", "X");
    fill("Warehouse", "W1");
    for (const n of ["L-A", "L-B"]) {
      fireEvent.click(screen.getByLabelText(`Merge batch ${n}`));
      fill(`Quantity of batch ${n}`, "1");
      fill(`Cost per unit of batch ${n}`, "1");
    }
    fireEvent.click(screen.getByRole("button", { name: "Merge batches" }));
    expect(await findAlert()).toHaveTextContent("PMF-0104");
    const before = calls.filter((c) => c.method === "GET").length;
    fireEvent.click(screen.getByRole("button", { name: "Reload batches" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(before));
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "database unavailable" } });
    renderAt("/processmfg/merge-stored");
    fill("Item id", ITEM);
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("explains the missing duty instead of offering the form", () => {
    renderAt("/processmfg/merge-stored", ["production.batch-order.read"]);
    expect(screen.getByText(/needs the batch merge duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Merge batches" })).toBeNull();
  });
});

describe("batch genealogy on the batch detail (slice 13)", () => {
  const detail = { ...batch(T, "L-AB", 1, "2027-03-01"), attributes: [], attributeHistory: [], dispositions: [] };

  it("lists the parents and the batches it went into", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/production/batches/${T}`) return { status: 200, body: detail };
      if (url.pathname === `/api/v1/production/batches/${T}/genealogy`) {
        return { status: 200, body: { batchId: T, parents: [link(A, "L-A", "200", "1.5"), link(B, "L-B", "300", "2")], children: [link("5b000000-0000-4000-8000-0000000000a9", "L-NEXT", "100", "1.8")] } };
      }
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: [] } };
      return undefined;
    };
    renderAt(`/processmfg/batches/${T}`);
    const from = await findTable({ name: "Made from" });
    expect(within(from).getByRole("link", { name: "L-B" })).toHaveAttribute("href", `/processmfg/batches/${B}`);
    expect(within(await findTable({ name: "Went into" })).getByText("L-NEXT")).toBeInTheDocument();
  });

  it("shows the empty states and a genealogy load error with retry", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname === `/api/v1/production/batches/${T}`) return { status: 200, body: detail };
      if (url.pathname === `/api/v1/production/batches/${T}/genealogy`) {
        return fail ? { status: 500, body: { code: "COM-0500", detail: "down" } } : { status: 200, body: { batchId: T, parents: [], children: [] } };
      }
      if (url.pathname.endsWith("/batch-attribute-definitions")) return { status: 200, body: { items: [] } };
      return undefined;
    };
    renderAt(`/processmfg/batches/${T}`);
    expect(await screen.findByText("Could not load the genealogy")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getAllByRole("button", { name: "Try again" })[0]);
    expect(await screen.findByText("Not made from another batch")).toBeInTheDocument();
    expect(screen.getByText("Did not go into another batch")).toBeInTheDocument();
  });
});
