import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Partner } from "./api/partners";
import intercompany from "./module";
import { validateLine, validateChange, sampleChange, sampleLine } from "./PartnerDetailPage";
import { atMost, defaultPolicy, validateNewPartner } from "./PartnerFormPage";

type Reply = { status: number; body: unknown };
type Handler = (method: string, url: URL, body: unknown, headers: Record<string, string>) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; headers: Record<string, string> }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const method = init.method ?? "GET";
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      const headers = (init.headers as Record<string, string> | undefined) ?? {};
      calls.push({ method, url, body, headers });
      const reply = handler(method, url, body, headers) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${method} ${url.pathname}` } };
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
        {intercompany.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const id = "01890a5d-ac96-774b-bcce-b302099a8001";

function partner(over: Partial<Partner> = {}): Partner {
  return {
    id,
    version: 1,
    sellerEntity: "P",
    buyerEntity: "S",
    customerAccount: "IC-S",
    vendorAccount: "IC-P",
    active: true,
    actions: { autoCreate: true, autoConfirm: false, autoPostPackingSlip: false, autoPostInvoice: false },
    pricing: "CostPlus",
    markupPct: "10",
    tolerance: { pricePct: "0.5", dateDays: 2 },
    createdAt: "2026-10-27T09:00:00Z",
    updatedAt: "2026-10-27T09:00:00Z",
    ...over,
  };
}

const bodyRows = (table: HTMLElement) => within(table).getAllByRole("row").slice(1);
const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((c) => c.textContent);

describe("module registration", () => {
  it("routes the partner list, create form and detail page and links the list with IntercompanyView", () => {
    expect(intercompany.routes.map((r) => r.path)).toEqual(
      expect.arrayContaining(["/intercompany/partners", "/intercompany/partners/new", "/intercompany/partners/:id"]),
    );
    expect(intercompany.nav).toContainEqual({ label: "Trading partners", to: "/intercompany/partners", duty: "IntercompanyView" });
  });
});

describe("form checks", () => {
  it("compares bounds without floats", () => {
    expect(atMost("1000", 1000)).toBe(true);
    expect(atMost("1000.000001", 1000)).toBe(false);
    expect(atMost("999.999999", 1000)).toBe(true);
    expect(atMost("0030", 30)).toBe(true);
    expect(atMost("31", 30)).toBe(false);
    expect(atMost("100.5", 100)).toBe(false);
  });

  it("rejects the same entity on both sides and missing accounts, and builds the request otherwise", () => {
    const bad = validateNewPartner({ direction: { sellerEntity: "P", buyerEntity: "P" }, policy: defaultPolicy });
    expect(bad.request).toBeUndefined();
    expect(bad.errors).toMatchObject({
      buyerEntity: "The buyer must be a different entity from the seller.",
      customerAccount: "Enter the customer account.",
      vendorAccount: "Enter the vendor account.",
    });
    const ok = validateNewPartner({
      direction: { sellerEntity: " P ", buyerEntity: "S" },
      policy: { ...defaultPolicy, text: { ...defaultPolicy.text, customerAccount: "IC-S", vendorAccount: "IC-P", dateDays: "31" } },
    });
    expect(ok.errors["tolerance.dateDays"]).toBe("Enter a whole number of days from 0 to 30.");
  });

  it("asks for the inputs the pairing's pricing policy needs", () => {
    const noList = validateLine({ ...sampleLine, line: { ...sampleLine.line, listPrice: "" } }, "PriceList");
    expect(noList.errors.listPrice).toBe("Enter the unit list price, 0 or more.");
    const costPlus = validateLine(sampleLine, "CostPlus");
    expect(costPlus.request).toEqual({ item: "PUMP-100", date: "2026-11-02", currency: "EUR", quantity: "8", unitCost: "60.00" });
    const table = validateLine({ ...sampleLine, rows: [] }, "TransferPriceTable");
    expect(table.errors.transferPrices).toBe("Add the item's transfer prices.");
    expect(validateChange({ ...sampleChange, newQuantity: "0" }).errors.newQuantity).toBe("Enter a quantity above 0.");
  });
});

describe("trading partners list", () => {
  it("shows a loading table, then the pairings, and opens a pairing", async () => {
    handler = (method, url) =>
      method === "GET" && url.pathname === "/api/v1/interco/partners"
        ? { status: 200, body: { items: [partner(), partner({ id: "p2", sellerEntity: "S", buyerEntity: "P", active: false, pricing: "PriceList", version: 3 })] } }
        : method === "GET" && url.pathname === `/api/v1/interco/partners/${id}`
          ? { status: 200, body: partner() }
          : undefined;
    const { container } = renderAt("/intercompany/partners");
    expect(screen.getByRole("heading", { level: 2, name: "Trading partners" })).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Trading partners" })).toHaveAttribute("aria-busy", "true");
    const table = await screen.findByRole("table", { name: "Trading partners" });
    await waitFor(() => expect(bodyRows(table)).toHaveLength(2));
    expect(cells(bodyRows(table)[0])).toEqual(["P → S", "IC-S / IC-P", "Cost plus markup 10 %", "±0.5 % price, ±2 days", "Active", "1"]);
    expect(cells(bodyRows(table)[1])).toContain("Inactive");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "P → S" }));
    expect(await screen.findByRole("heading", { level: 2, name: "P to S" })).toBeInTheDocument();
  });

  it("shows the empty state and filters by entity and status", async () => {
    handler = (method, url) => (method === "GET" && url.pathname === "/api/v1/interco/partners" ? { status: 200, body: { items: [] } } : undefined);
    renderAt("/intercompany/partners");
    expect(await screen.findByText("No trading partners yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Entity"), { target: { value: " S " } });
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "inactive" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    expect(await screen.findByText("No pairings match")).toBeInTheDocument();
    const last = calls[calls.length - 1];
    expect(last.url.searchParams.get("entity")).toBe("S");
    expect(last.url.searchParams.get("active")).toBe("false");
  });

  it("shows the error with a retry, and loads the next page", async () => {
    let fail = true;
    handler = (method, url) => {
      if (method !== "GET" || url.pathname !== "/api/v1/interco/partners") return undefined;
      if (fail) return { status: 500, body: { code: "COM-0500", detail: "internal error" } };
      if (url.searchParams.get("after") === "cursor-1") return { status: 200, body: { items: [partner({ id: "p2", sellerEntity: "S", buyerEntity: "X" })] } };
      return { status: 200, body: { items: [partner()], nextCursor: "cursor-1" } };
    };
    renderAt("/intercompany/partners");
    expect(await screen.findByText("We couldn't load the trading partners")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("internal error (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    const table = await screen.findByRole("table", { name: "Trading partners" });
    await waitFor(() => expect(bodyRows(table)).toHaveLength(1));
    fireEvent.click(screen.getByRole("button", { name: "Show more pairings" }));
    await waitFor(() => expect(bodyRows(table)).toHaveLength(2));
    expect(screen.queryByRole("button", { name: "Show more pairings" })).not.toBeInTheDocument();
  });
});

describe("new trading partner", () => {
  it("checks the form before sending", async () => {
    const { container } = renderAt("/intercompany/partners/new");
    fireEvent.click(screen.getByRole("button", { name: "Save pairing" }));
    expect(await screen.findByText("Enter the seller entity.")).toBeInTheDocument();
    expect(screen.getByText("Enter the customer account.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates the pairing with an Idempotency-Key and opens it", async () => {
    handler = (method, url, body) => {
      if (method === "POST" && url.pathname === "/api/v1/interco/partners") return { status: 201, body: partner({ ...(body as Partial<Partner>) }) };
      if (method === "GET" && url.pathname === `/api/v1/interco/partners/${id}`) return { status: 200, body: partner() };
      return undefined;
    };
    renderAt("/intercompany/partners/new");
    fireEvent.change(screen.getByLabelText("Seller entity"), { target: { value: "P" } });
    fireEvent.change(screen.getByLabelText("Buyer entity"), { target: { value: "S" } });
    fireEvent.change(screen.getByLabelText("Customer account"), { target: { value: "IC-S" } });
    fireEvent.change(screen.getByLabelText("Vendor account"), { target: { value: "IC-P" } });
    fireEvent.change(screen.getByLabelText("Pricing policy"), { target: { value: "CostPlus" } });
    fireEvent.change(screen.getByLabelText("Markup %"), { target: { value: "10" } });
    fireEvent.click(screen.getByLabelText("Post the invoice"));
    fireEvent.click(screen.getByRole("button", { name: "Save pairing" }));
    expect(await screen.findByRole("heading", { level: 2, name: "P to S" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(post.body).toEqual({
      sellerEntity: "P",
      buyerEntity: "S",
      customerAccount: "IC-S",
      vendorAccount: "IC-P",
      active: true,
      actions: { autoCreate: true, autoConfirm: false, autoPostPackingSlip: false, autoPostInvoice: true },
      pricing: "CostPlus",
      markupPct: "10",
      tolerance: { pricePct: "0.5", dateDays: 2 },
    });
  });

  it("shows the service's conflict and keeps the input", async () => {
    handler = (method) =>
      method === "POST" ? { status: 409, body: { code: "IC-0840", detail: "an active pairing from P to S already exists; deactivate it first" } } : undefined;
    renderAt("/intercompany/partners/new");
    for (const [label, value] of [
      ["Seller entity", "P"],
      ["Buyer entity", "S"],
      ["Customer account", "IC-S"],
      ["Vendor account", "IC-P"],
    ]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Save pairing" }));
    expect(await screen.findByText("The pairing was not saved")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("already exists; deactivate it first (IC-0840)");
    expect(screen.getByLabelText("Seller entity")).toHaveValue("P");
  });
});

describe("trading partner detail", () => {
  it("shows the policy and deactivates with If-Match", async () => {
    handler = (method, url, body) => {
      if (url.pathname !== `/api/v1/interco/partners/${id}`) return undefined;
      if (method === "GET") return { status: 200, body: partner() };
      if (method === "PATCH") return { status: 200, body: partner({ ...(body as Partial<Partner>), version: 2 }) };
      return undefined;
    };
    const { container } = renderAt(`/intercompany/partners/${id}`);
    expect(screen.getByText("Loading the pairing…")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { level: 2, name: "P to S" })).toBeInTheDocument();
    expect(screen.getByText("Customer IC-S in P trades with vendor IC-P in S. Version 1.")).toBeInTheDocument();
    const policy = screen.getByRole("heading", { level: 3, name: "Policy" }).closest("div")!.parentElement!;
    expect(within(policy).getByText("Create the mirror document automatically")).toBeInTheDocument();
    expect(within(policy).getByText("Cost plus markup")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Deactivate pairing" }));
    expect(await screen.findByRole("button", { name: "Activate pairing" })).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.headers["If-Match"]).toBe('"1"');
    expect(patch.body).toEqual({ active: false });
    expect(screen.getByText("Inactive")).toBeInTheDocument();
  });

  it("saves the policy and explains a version conflict", async () => {
    let conflict = false;
    handler = (method, url, body) => {
      if (url.pathname !== `/api/v1/interco/partners/${id}`) return undefined;
      if (method === "GET") return { status: 200, body: partner() };
      if (conflict) return { status: 412, body: { code: "IC-0843", detail: "the pairing is at version 2, not 1" } };
      return { status: 200, body: partner({ ...(body as Partial<Partner>), version: 2 }) };
    };
    renderAt(`/intercompany/partners/${id}`);
    await screen.findByRole("heading", { level: 2, name: "P to S" });
    const edit = screen.getByRole("heading", { level: 3, name: "Edit policy" }).closest("div")!.parentElement!;
    fireEvent.change(within(edit).getByLabelText("Date tolerance (days)"), { target: { value: "5" } });
    fireEvent.click(within(edit).getByRole("button", { name: "Save policy" }));
    expect(await screen.findByText("Policy saved as version 2.")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.headers["If-Match"]).toBe('"1"');
    expect(patch.body).toMatchObject({ tolerance: { pricePct: "0.5", dateDays: 5 }, pricing: "CostPlus" });
    expect(patch.body).not.toHaveProperty("active");

    conflict = true;
    fireEvent.click(within(edit).getByRole("button", { name: "Save policy" }));
    expect(await screen.findByText(/Someone changed this pairing since you opened it/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload pairing" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
  });

  it("prices a line by the stored policy and checks an order change", async () => {
    handler = (method, url, body) => {
      if (method === "GET" && url.pathname === `/api/v1/interco/partners/${id}`) return { status: 200, body: partner() };
      if (url.pathname === `/api/v1/interco/partners/${id}/lines:price`)
        return {
          status: 200,
          body: { partnerId: id, partnerVersion: 1, item: "PUMP-100", quantity: "8", currency: "EUR", unitPrice: "66", extended: "528.00", basis: { policy: "CostPlus", method: "CostPlus", markupPct: "10", source: "partner-policy" } },
        };
      if (url.pathname === `/api/v1/interco/partners/${id}/changes:evaluate`) {
        const reapprove = (body as { newQuantity: string }).newQuantity !== "100";
        return {
          status: 200,
          body: reapprove
            ? { partnerId: id, partnerVersion: 1, action: "ReApprove", code: "IC-0804", reasons: ["quantity changed from 100 to 90; quantity changes always need re-approval"], dateShiftDays: 2, tolerance: { pricePct: "0.5", dateDays: 2 } }
            : { partnerId: id, partnerVersion: 1, action: "UpdateSilently", reasons: [], dateShiftDays: 2, tolerance: { pricePct: "0.5", dateDays: 2 } },
        };
      }
      return undefined;
    };
    renderAt(`/intercompany/partners/${id}`);
    await screen.findByRole("heading", { level: 2, name: "P to S" });
    fireEvent.click(screen.getByRole("button", { name: "Price line" }));
    expect(await screen.findByText("528.00 EUR")).toBeInTheDocument();
    expect(screen.getByText("CostPlus 10 %")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/lines:price"))!.body).toEqual({
      item: "PUMP-100",
      date: "2026-11-02",
      currency: "EUR",
      quantity: "8",
      unitCost: "60.00",
    });

    fireEvent.click(screen.getByRole("button", { name: "Check change" }));
    expect(await screen.findByText("The mirror purchase order updates silently")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity after"), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Check change" }));
    expect(await screen.findByText("The mirror purchase order goes back for approval (IC-0804)")).toBeInTheDocument();
    expect(screen.getByText(/quantity changed from 100 to 90/)).toBeInTheDocument();
  });

  it("asks for transfer prices on a table pairing and shows IC-0821", async () => {
    handler = (method, url) => {
      if (method === "GET") return { status: 200, body: partner({ pricing: "TransferPriceTable" }) };
      if (url.pathname.endsWith("/lines:price")) return { status: 422, body: { code: "IC-0821", detail: "no transfer price for Item / PUMP-100 on 2026-11-02" } };
      return undefined;
    };
    renderAt(`/intercompany/partners/${id}`);
    await screen.findByRole("heading", { level: 2, name: "P to S" });
    expect(screen.getByRole("heading", { level: 4, name: "Transfer prices" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Price line" }));
    expect(await screen.findByText("The line was not priced")).toBeInTheDocument();
    expect(screen.getByText(/no transfer price for Item \/ PUMP-100 on 2026-11-02 \(IC-0821\)/)).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/lines:price"))!.body).toMatchObject({
      transferPrices: [{ item: "PUMP-100", method: "Table", unitPrice: "66.00", effectiveFrom: "2026-01-01" }],
    });
  });

  it("shows not found for an unknown pairing and an error with retry otherwise", async () => {
    handler = (_method, url) => (url.pathname.endsWith("/missing") ? { status: 404, body: { code: "IC-0842", detail: "no intercompany partner" } } : { status: 503, body: { code: "COM-0503", detail: "unavailable" } });
    const { unmount } = renderAt("/intercompany/partners/missing");
    expect(await screen.findByRole("heading", { level: 2, name: "We couldn't find that pairing" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All trading partners" })).toHaveAttribute("href", "/intercompany/partners");
    unmount();
    renderAt(`/intercompany/partners/${id}`);
    expect(await screen.findByText("We couldn't load this pairing")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
