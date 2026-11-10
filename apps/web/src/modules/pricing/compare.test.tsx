import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { NetPriceRow, CompareResponse, PriceListItem, Procedure, ResolveResponse } from "./api/client";
import { referenceSteps } from "./ProcedureStepsEditor";
import { validateComparison } from "./ComparePage";
import { netPriceLines, netPricesCsv, winningQuantities } from "./format";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const VIEW = ["pricing.view"];
const v2: Procedure = { id: "0190f0a0-0000-7000-8000-000000000d02", legalEntityId: LE, name: "AX_STANDARD", version: 2, status: "DRAFT", revision: 1, steps: referenceSteps() };
const v1: Procedure = { ...v2, id: "0190f0a0-0000-7000-8000-000000000d01", version: 1, status: "ACTIVE" };

const side = (net: string): ResolveResponse => ({
  currency: "USD", lines: [], subtotal: net, totalDiscount: "0", surcharge: "0", orderNet: net, applied: [], explainRef: "sha256:x", freeGoods: [],
});
const compared: CompareResponse = {
  a: side("80.96"),
  b: side("88"),
  comparison: {
    identical: false, subtotalDelta: "7.04", totalDiscountDelta: "0", surchargeDelta: "0", orderNetDelta: "7.04", changedLines: 1,
    lines: [{ lineNo: 1, netUnitPriceA: "80.96", netUnitPriceB: "88", netUnitPriceDelta: "7.04", lineNetA: "80.96", lineNetB: "88", lineNetDelta: "7.04", appliedOnlyA: ["MULTILINE_DISC:multi12"], appliedOnlyB: [], changed: true }],
  },
};

function load() {
  fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
  fireEvent.click(screen.getByRole("button", { name: "Load versions" }));
}

function fill() {
  fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "C1" } });
  fireEvent.change(screen.getByLabelText("Product"), { target: { value: "P1" } });
}

describe("Compare procedure versions", () => {
  it("starts empty and validates the legal entity id", async () => {
    const { container } = renderAt("/pricing/compare", VIEW);
    expect(screen.getByText("No legal entity loaded")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load versions" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains that a comparison needs two versions", async () => {
    server.handler = (url) => (url.pathname.endsWith("/procedures") ? json(200, { items: [v1] }) : undefined);
    renderAt("/pricing/compare", VIEW);
    load();
    expect(await screen.findByText("Fewer than two versions")).toBeInTheDocument();
  });

  it("compares the newest version with the one before it and shows the line changes", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/procedures")) return json(200, { items: [v2, v1] });
      if (url.pathname.endsWith("/compare")) return json(200, compared);
      return undefined;
    };
    const { container } = renderAt("/pricing/compare", VIEW);
    load();
    expect(await screen.findByLabelText("Baseline version")).toHaveValue(v1.id);
    expect(screen.getByLabelText("Version to compare")).toHaveValue(v2.id);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Compare versions" }));
    const table = await findTable({ name: "Compared lines" });
    expect(within(table).getByText("MULTILINE_DISC:multi12")).toBeInTheDocument();
    expect(within(table).getByText("+7.04")).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith("/compare"));
    expect(call?.body).toMatchObject({ legalEntityId: LE, procedureIdA: v1.id, procedureIdB: v2.id, customerId: "C1", lines: [{ productId: "P1", unit: "EA", quantity: "1" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("says so when both versions price identically", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/procedures")) return json(200, { items: [v2, v1] });
      return json(200, { ...compared, comparison: { ...compared.comparison, identical: true, changedLines: 0, orderNetDelta: "0" } });
    };
    renderAt("/pricing/compare", VIEW);
    load();
    await screen.findByLabelText("Baseline version");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Compare versions" }));
    expect(await screen.findByText("Both versions price this request identically.")).toBeInTheDocument();
  });

  it("surfaces a service refusal and its field errors", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/procedures")) return json(200, { items: [v2, v1] });
      return json(422, { code: "COM-0001", detail: "request is invalid", errors: { procedureIdB: "belongs to another legal entity" } });
    };
    renderAt("/pricing/compare", VIEW);
    load();
    await screen.findByLabelText("Baseline version");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Compare versions" }));
    expect(await screen.findByText(/COM-0001/)).toBeInTheDocument();
    expect(await screen.findByText("belongs to another legal entity")).toBeInTheDocument();
  });

  it("reports a failed version load", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/pricing/compare", VIEW);
    load();
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
  });

  it("mirrors the compare validation", () => {
    const ok = { customerId: "C1", customerGroupId: "", currency: "USD", pricingDate: "2027-03-10", productId: "P1", unit: "EA", quantity: "1", procedureIdA: "a", procedureIdB: "b" };
    expect(validateComparison(ok)).toEqual({});
    expect(Object.keys(validateComparison({ ...ok, customerId: "", currency: "us", quantity: "0", procedureIdA: "", procedureIdB: "" })).sort()).toEqual(["currency", "customerId", "procedureIdA", "procedureIdB", "quantity"]);
    expect(validateComparison({ ...ok, procedureIdB: "a" }).procedureIdB).toMatch(/different/);
  });
});

const item = (id: string, scope: PriceListItem["scope"], amount: string, extra: Partial<PriceListItem> = {}): PriceListItem => ({
  id, legalEntityId: LE, type: "PRICE", customerKind: scope === "ALL" ? "all" : scope === "GROUP" ? "customer_group" : "customer", customerKey: scope === "ALL" ? "" : "X",
  productKind: "product", productKey: "P1", variant: "", currency: "USD", unit: "EA", fromQuantity: "0", amount, discPct1: "0", discPct2: "0",
  validFrom: "2027-01-01", validTo: "2027-12-31", searchNext: false, exclusive: false, status: "ACTIVE", version: 1, scope, ...extra,
});

function ask() {
  fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
  fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "C1" } });
  fireEvent.click(screen.getByRole("button", { name: "Show price list" }));
}

describe("Price list by customer", () => {
  it("starts empty and validates the form", async () => {
    const { container } = renderAt("/pricing/price-list", VIEW);
    expect(screen.getByText("No price list yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show price list" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the customer.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("lists the applicable agreements with their scope and sends the query", async () => {
    server.handler = (url) => (url.pathname.endsWith("/price-list") ? json(200, { items: [item("r1", "CUSTOMER", "90"), item("r2", "GROUP", "95"), item("r3", "ALL", "100")] }) : undefined);
    const { container } = renderAt("/pricing/price-list", VIEW);
    ask();
    const table = await findTable({ name: "Price list" });
    expect(within(table).getByText("This customer")).toBeInTheDocument();
    expect(within(table).getByText("Customer group")).toBeInTheDocument();
    expect(within(table).getByText("All customers")).toBeInTheDocument();
    expect(within(table).getByText("90 USD")).toBeInTheDocument();
    const q = server.calls[0]!.url.searchParams;
    expect(q.get("customerId")).toBe("C1");
    expect(q.get("legalEntityId")).toBe(LE);
    expect(q.get("type")).toBe("PRICE");
    expect(q.has("customerGroupId")).toBe(false);
    expect(screen.queryByText("Only the first agreements are shown")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("warns when the service cut the list at its cap and loads the next page", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/price-list")
        ? url.searchParams.get("offset") === "1"
          ? json(200, { items: [item("r2", "ALL", "90")], truncated: false, total: 2 })
          : json(200, { items: [item("r1", "ALL", "100")], truncated: true, total: 2, nextOffset: 1 })
        : undefined;
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText("Only the first agreements are shown")).toBeInTheDocument();
    expect(screen.getByText(/1 of 2 agreements are shown/)).toBeInTheDocument();
    // The next page continues the shown list, not what the form says now.
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: "C2" } });
    fireEvent.change(screen.getByLabelText(/^Product$/), { target: { value: "P9" } });
    fireEvent.click(screen.getByRole("button", { name: "Load the next page" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Load the next page" })).not.toBeInTheDocument());
    const next = server.calls.find((c) => c.url.searchParams.get("offset") === "1")!;
    expect(next.url.searchParams.get("customerId")).toBe("C1");
    expect(next.url.searchParams.get("productId")).toBeNull();
    expect(next.url.searchParams.get("type")).toBe("PRICE");
    expect(screen.queryByText("Only the first agreements are shown")).not.toBeInTheDocument();
    expect(screen.getAllByText(/90/).length).toBeGreaterThan(0);
  });

  it("keeps the shown page when the next one fails", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("/price-list")
        ? url.searchParams.get("offset") === "1"
          ? json(500, { code: "COM-0500", detail: "database unavailable" })
          : json(200, { items: [item("r1", "ALL", "100")], truncated: true, total: 2, nextOffset: 1 })
        : undefined;
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText(/1 of 2 agreements are shown/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load the next page" }));
    expect(await screen.findByText("The next page could not be loaded")).toBeInTheDocument();
    expect(screen.getByText(/COM-0500/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load the next page" })).toBeInTheDocument();
    expect(screen.getByText(/1 of 2 agreements are shown/)).toBeInTheDocument();
  });

  it("shows the empty state when nothing applies", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText("No agreements apply")).toBeInTheDocument();
  });

  it("shows a failure from the service", async () => {
    server.handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { currency: "must be three upper-case letters" } });
    renderAt("/pricing/price-list", VIEW);
    ask();
    await waitFor(() => expect(screen.getByText("must be three upper-case letters")).toBeInTheDocument());
    expect(await screen.findByText(/COM-0001/)).toBeInTheDocument();
  });

  const netRows = {
    rows: [
      {
        productKind: "product", productKey: "P1", variant: "", unit: "EA",
        breaks: [
          { quantity: "1", unitPrice: "100", netUnitPrice: "100", lineNet: "100", priceRecordId: "r3", applied: [{ type: "PRICE", recordId: "r3" }] },
          { quantity: "10", unitPrice: "85", netUnitPrice: "85", lineNet: "850", priceRecordId: "r1", applied: [{ type: "PRICE", recordId: "r1" }] },
        ],
      },
      { productKind: "product", productKey: "P2", variant: "", unit: "EA", breaks: [{ quantity: "5", unitPrice: "0", netUnitPrice: "0", lineNet: "0", applied: [], errorCode: "PRC-0001" }] },
    ],
    truncated: false,
  };

  it("resolves the net price per quantity break and marks the winning agreements", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/net-prices")) return json(200, netRows);
      return url.pathname.endsWith("/price-list") ? json(200, { items: [item("r1", "CUSTOMER", "85"), item("r3", "ALL", "100")] }) : undefined;
    };
    const { container } = renderAt("/pricing/price-list", VIEW);
    fireEvent.change(screen.getByLabelText(/^Product$/), { target: { value: "P1" } });
    fireEvent.change(screen.getByLabelText(/^Sales channel/), { target: { value: "WEB" } });
    ask();
    const net = await findTable({ name: "Net prices" });
    expect(within(net).getByText("850")).toBeInTheDocument();
    expect(within(net).getByText("No price")).toBeInTheDocument();
    const list = await findTable({ name: "Price list" });
    expect(within(list).getByText("10")).toBeInTheDocument();
    expect(within(list).getByText("1")).toBeInTheDocument();
    const q = server.calls[1]!.url.searchParams;
    expect(server.calls[1]!.url.pathname).toMatch(/\/price-list\/net-prices$/);
    expect(q.get("productId")).toBe("P1");
    expect(q.has("productGroupId")).toBe(false);
    expect(q.get("salesChannel")).toBe("WEB");
    expect(q.has("type")).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps the list when the net prices fail and names the bad field", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/net-prices")) return json(422, { code: "COM-0001", detail: "request is invalid", errors: { salesChannel: "must be upper case" } });
      return url.pathname.endsWith("/price-list") ? json(200, { items: [item("r1", "ALL", "100")] }) : undefined;
    };
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText("The net prices could not be loaded")).toBeInTheDocument();
    expect(screen.getByText("Applicable agreements")).toBeInTheDocument();
    expect(screen.getByText("must be upper case")).toBeInTheDocument();
  });

  it("warns when the net price rows were cut", async () => {
    server.handler = (url) => (url.pathname.endsWith("/net-prices") ? json(200, { ...netRows, truncated: true }) : json(200, { items: [item("r1", "ALL", "100")] }));
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText("Only the first products are shown")).toBeInTheDocument();
  });

  it("shows an empty state when no product has a price", async () => {
    server.handler = (url) => (url.pathname.endsWith("/net-prices") ? json(200, { rows: [], truncated: false }) : json(200, { items: [] }));
    renderAt("/pricing/price-list", VIEW);
    ask();
    expect(await screen.findByText("No prices to resolve")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Export CSV" })).not.toBeInTheDocument();
  });

  it("exports the net prices as CSV", async () => {
    const created: Blob[] = [];
    const create = vi.fn((b: Blob) => (created.push(b), "blob:x"));
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    server.handler = (url) => (url.pathname.endsWith("/net-prices") ? json(200, netRows) : json(200, { items: [item("r1", "ALL", "100")] }));
    renderAt("/pricing/price-list", VIEW);
    ask();
    const exportButton = await screen.findByRole("button", { name: "Export CSV" });
    // Editing the form after the query does not change what the file says it is.
    fireEvent.change(screen.getByLabelText(/^Customer$/), { target: { value: "OTHER" } });
    fireEvent.change(screen.getByLabelText(/^Currency/), { target: { value: "EUR" } });
    fireEvent.click(exportButton);
    expect(click).toHaveBeenCalled();
    expect((click.mock.contexts[0] as HTMLAnchorElement).download).toMatch(/^price-list-C1-\d{4}-\d{2}-\d{2}\.csv$/);
    const text = await created[0]!.text();
    expect(text.split("\r\n")[0]).toBe("Product,Unit,From quantity,List price USD,Net unit price USD,Line net USD,Winning price record,Error");
    expect(text).toContain("P1,EA,10,85,85,850,r1,");
    click.mockRestore();
  });

  it("flattens breaks, collects the winning quantities and escapes CSV cells", () => {
    const rows = netRows.rows as NetPriceRow[];
    expect(netPriceLines(rows).map((l) => `${l.product}@${l.quantity}`)).toEqual(["P1@1", "P1@10", "P2@5"]);
    expect(winningQuantities(rows).get("r1")).toEqual(["10"]);
    const statistical: NetPriceRow = { ...rows[0]!, breaks: [{ ...rows[0]!.breaks[0]!, applied: [{ type: "PRICE", recordId: "r3" }, { type: "LINE_DISC", recordId: "s1", statistical: true }] }] };
    expect(winningQuantities([statistical]).has("s1")).toBe(false);
    const line = netPriceLines(rows)[0]!;
    const cells = (product: string) => netPricesCsv([{ ...line, product }], "USD").split("\r\n")[1]!;
    expect(cells('=SUM(A1),"x"')).toMatch(/^"'=SUM\(A1\),""x""",/);
    expect(cells("+1+1")).toMatch(/^'\+1\+1,/);
    expect(cells("@cmd")).toMatch(/^'@cmd,/);
    expect(cells("-2+3")).toMatch(/^'-2\+3,/);
    expect(cells("\t=1")).toMatch(/^'\t=1,/);
    expect(cells("\r=1")).toMatch(/^"'\r=1",/);
    expect(cells("-12.5")).toMatch(/^-12\.5,/);
  });
});
