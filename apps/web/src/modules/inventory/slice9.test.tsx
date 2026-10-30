import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateLine } from "./JournalDetailPage";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const JOURNAL_ID = "1a2b3c4d-0000-4000-8000-000000000001";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const transfer = (overrides: Record<string, unknown> = {}) => ({
  id: JOURNAL_ID,
  legalEntityId: ENTITY_ID,
  journalType: "TRANSFER",
  status: "DRAFT",
  postingDate: "2026-10-01",
  currency: "USD",
  version: 1,
  lineCount: 0,
  createdAt: "2026-10-01T08:00:00Z",
  lines: [],
  ...overrides,
});

const emptyDims = { site: "", warehouse: "", location: "", batch: "", serial: "", inventory_status: "", owner: "", segment: "" };
const base = { productId: PRODUCT_ID, qty: "3", enteredUnit: "EA", costPrice: "", dimensions: { ...emptyDims, site: "S1", location: "A1" } };

describe("Transfer journals", () => {
  it("validates the destination of a transfer line", () => {
    const to = { site: "S1", warehouse: "", location: "B7", segment: "" };
    expect(validateLine({ ...base, toDimensions: to }, "TRANSFER")).toEqual({});
    expect(validateLine({ ...base, qty: "-3", toDimensions: to }, "TRANSFER").qty).toBe("Enter the quantity to move (above zero).");
    expect(validateLine({ ...base, toDimensions: { ...to, site: "" } }, "TRANSFER").toSite).toBe("Enter the destination site.");
    expect(validateLine({ ...base, toDimensions: { ...to, location: "A1" } }, "TRANSFER").toSame).toBe("The destination must differ from the source.");
    expect(validateLine({ ...base, toDimensions: { ...to, segment: "P1" } }, "TRANSFER").toSegment).toMatch(/SALES_ORDER/);
    expect(
      validateLine({ ...base, dimensions: { ...base.dimensions, inventory_status: "BLOCKED" }, toDimensions: to }, "TRANSFER").inventory_status,
    ).toMatch(/releasing its blocking/);
    expect(validateLine(base, "ADJUSTMENT")).toEqual({});
  });

  it("is offered on the new journal form without an offset account", async () => {
    renderAt("/inventory/journals/new?type=TRANSFER");
    expect(await screen.findByLabelText("Journal type")).toHaveValue("TRANSFER");
    expect(screen.queryByLabelText("Offset account")).toBeNull();
    expect(screen.getByText(/Moves stock between two locations/)).toBeInTheDocument();
  });

  it("adds a transfer line with its destination and shows it", async () => {
    const line = {
      id: "l1",
      lineNo: 1,
      productId: PRODUCT_ID,
      dimensions: { site: "S1", location: "A1" },
      toDimensions: { site: "S1", location: "B7" },
      qty: "3",
      enteredQty: "3",
      enteredUnit: "EA",
      costPrice: "0",
    };
    handler = (_url, init) => (init.method === "POST" ? json(201, transfer({ version: 2, lineCount: 1, lines: [line] })) : json(200, transfer()));
    const { container } = renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer journal" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Location"), { target: { value: "A1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(await screen.findByText("Enter the destination site.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("To site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("To location"), { target: { value: "B7" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0]).lines[0]).toMatchObject({
      productId: PRODUCT_ID,
      qty: "3",
      dimensions: { site: "S1", location: "A1" },
      toDimensions: { site: "S1", location: "B7" },
    });
    expect((await screen.findAllByText(/B7/)).length).toBeGreaterThan(0);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows the service's refusal of a transfer line", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? json(422, { code: "INV-JNL-006", detail: "line 1: a transfer cannot change the color (it moves stock, it does not relabel it)" })
        : json(200, transfer());
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer journal" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("To site"), { target: { value: "S2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(await screen.findByText(/cannot change the color/)).toBeInTheDocument();
    expect(screen.getByText("Could not add the line")).toBeInTheDocument();
  });

  it("does not offer a reversal of a posted transfer", async () => {
    handler = () => json(200, transfer({ status: "POSTED" }));
    renderAt(`/inventory/journals/${JOURNAL_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer journal" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reverse journal" })).toBeNull();
  });
});
