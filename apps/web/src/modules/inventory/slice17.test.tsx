import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { PrincipalProvider } from "../../shell/principal";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";
import { validateBlocking } from "./NewBlockingPage";
import { emptyDimensions } from "./parts";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const BLK_ID = "6b6b6b6b-0000-4000-8000-000000000005";
const SCRAP_JOURNAL_ID = "0a1b2c3d-0000-4000-8000-000000000009";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

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

function renderAt(path: string, duties: string[] = ["inventory.read", "inventory.blocking.write", "inventory.blocking.scrap"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {inventory.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const blocking = (overrides: Record<string, unknown> = {}) => ({
  id: BLK_ID,
  legalEntityId: ENTITY_ID,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", batch: "B1" },
  qty: "4",
  unit: "EA",
  targetStatus: "QUARANTINE",
  reason: "RECALL",
  state: "ACTIVE",
  physicalDate: "2026-09-02",
  expectedReleaseDate: "2026-09-09",
  version: 1,
  ...overrides,
});

describe("Blocking dispositions", () => {
  it("shows the expected release date and offers all three actions on a quarantine", async () => {
    handler = () => json(200, { items: [blocking(), blocking({ id: "7c7c7c7c-0000-4000-8000-000000000001", targetStatus: "BLOCKED", expectedReleaseDate: undefined })] });
    renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    await waitFor(() => expect(table).toHaveTextContent("2026-09-09"));
    expect(within(table).getAllByRole("button", { name: /^Scrap blocking/ })).toHaveLength(2);
    expect(within(table).getAllByRole("button", { name: /^Release blocking/ })).toHaveLength(2);
    // Only the quarantine can move to blocked.
    expect(within(table).getAllByRole("button", { name: /^Move to blocked blocking/ })).toHaveLength(1);
  });

  it("offers no scrap to a user without the scrap duty", async () => {
    handler = () => json(200, { items: [blocking()] });
    renderAt("/inventory/blockings", ["inventory.read", "inventory.blocking.write"]);
    const table = await findTable({ name: "Blockings" });
    await within(table).findByRole("button", { name: "Release blocking 6b6b6b6b" });
    expect(within(table).getByRole("button", { name: "Move to blocked blocking 6b6b6b6b" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: /^Scrap blocking/ })).toBeNull();
  });

  it("moves a quarantine to blocked at its version", async () => {
    let moved = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        moved = true;
        return json(200, blocking({ targetStatus: "BLOCKED", version: 2 }));
      }
      return json(200, { items: [blocking({ targetStatus: moved ? "BLOCKED" : "QUARANTINE", version: moved ? 2 : 1 })] });
    };
    renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    fireEvent.click(await within(table).findByRole("button", { name: "Move to blocked blocking 6b6b6b6b" }));
    await waitFor(() => expect(screen.getByText(/from quarantine to blocked/)).toBeInTheDocument());
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/blockings/${BLK_ID}:block`);
    expect(header(post, "If-Match")).toBe('"1"');
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Blockings" })).queryByRole("button", { name: /^Move to blocked/ })).toBeNull());
  });

  it("scrapping asks first and sends nothing when cancelled", async () => {
    handler = () => json(200, { items: [blocking()] });
    renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    fireEvent.click(await within(table).findByRole("button", { name: "Scrap blocking 6b6b6b6b" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("4 EA will be written off");
    fireEvent.click(screen.getByRole("button", { name: "Keep stock" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(sent("POST")).toHaveLength(0);
  });

  it("scraps after confirmation and links the journal that wrote the stock off", async () => {
    let scrapped = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        scrapped = true;
        return json(200, blocking({ state: "RELEASED", disposition: "SCRAP", scrapJournalId: SCRAP_JOURNAL_ID, version: 2 }));
      }
      return json(200, { items: [scrapped ? blocking({ state: "RELEASED", disposition: "SCRAP", scrapJournalId: SCRAP_JOURNAL_ID, version: 2 }) : blocking()] });
    };
    renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    fireEvent.click(await within(table).findByRole("button", { name: "Scrap blocking 6b6b6b6b" }));
    fireEvent.click(await screen.findByRole("button", { name: "Scrap stock" }));
    const post = await waitFor(() => {
      const first = sent("POST")[0];
      expect(first).toBeDefined();
      return first;
    });
    expect(post.url.pathname).toBe(`/api/v1/inventory/blockings/${BLK_ID}:release`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(bodyOf(post)).toEqual({ disposition: "SCRAP" });
    expect(await screen.findByText(/the stock was written off/)).toBeInTheDocument();
    // The list was asked for active blockings only, so switch to all to see the scrap and its journal.
    fireEvent.change(screen.getByLabelText("State"), { target: { value: "" } });
    const link = await screen.findByRole("link", { name: "Journal 0a1b2c3d" });
    expect(link).toHaveAttribute("href", `/inventory/journals/${SCRAP_JOURNAL_ID}`);
  });

  it("explains a scrap the caller may not make and keeps the stock listed", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(403, { code: "INV-BLK-008", detail: "scrapping blocked stock writes it off and needs the inventory.blocking.scrap duty" }) : json(200, { items: [blocking()] });
    renderAt("/inventory/blockings");
    const table = await findTable({ name: "Blockings" });
    fireEvent.click(await within(table).findByRole("button", { name: "Scrap blocking 6b6b6b6b" }));
    fireEvent.click(await screen.findByRole("button", { name: "Scrap stock" }));
    expect(await findAlert()).toHaveTextContent("inventory.blocking.scrap duty (INV-BLK-008)");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(within(screen.getByRole("table", { name: "Blockings" })).getByRole("button", { name: "Scrap blocking 6b6b6b6b" })).toBeInTheDocument();
  });

  it("sends the expected release date when blocking stock and checks it first", async () => {
    const base = { legalEntityId: ENTITY_ID, productId: PRODUCT_ID, qty: "2", unit: "EA", targetStatus: "QUARANTINE" as const, reason: "QUALITY" as const, note: "", physicalDate: "2026-09-02", expectedReleaseDate: "", dimensions: { ...emptyDimensions(), site: "S1" } };
    expect(validateBlocking(base)).toEqual({});
    expect(validateBlocking({ ...base, expectedReleaseDate: "2026-09-01" })).toEqual({ expectedReleaseDate: "The expected release cannot be before the physical date." });
    expect(validateBlocking({ ...base, expectedReleaseDate: "2026-09-02" })).toEqual({});

    handler = (_url, init) => (init.method === "POST" ? json(201, blocking()) : json(200, { items: [blocking()] }));
    renderAt("/inventory/blockings/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Physical date"), { target: { value: "2026-09-02" } });
    fireEvent.change(screen.getByLabelText(/Expected release/), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Block stock" }));
    expect(await screen.findByText("The expected release cannot be before the physical date.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText(/Expected release/), { target: { value: "2026-09-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Block stock" }));
    expect(await screen.findByRole("heading", { name: "Blocking and quarantine" })).toBeInTheDocument();
    expect(bodyOf(sent("POST")[0])).toMatchObject({ physicalDate: "2026-09-02", expectedReleaseDate: "2026-09-09" });
  });
});
