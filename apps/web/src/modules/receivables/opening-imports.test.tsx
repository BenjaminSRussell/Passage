import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { OpenItem, OpeningImportSummary } from "./api/client";
import { findAlert, findTable } from "../../test-utils";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";

const summary: OpeningImportSummary = {
  importSource: "SAP-PRD",
  legalEntityId: LE,
  currency: "USD",
  itemCount: 3,
  debitCount: 2,
  creditCount: 1,
  debitOpen: "900.00",
  creditOpen: "-120.00",
  netOpen: "780.00",
  lastImportedAt: "2026-11-01T08:30:00Z",
};

function item(id: string, over: Partial<OpenItem>): OpenItem {
  return {
    id,
    legalEntityId: LE,
    customerId: CUST,
    sourceType: "INVOICE",
    documentNumber: `D-${id}`,
    documentDate: "2026-09-01",
    dueDate: "2026-10-01",
    currency: "USD",
    amount: "1000.00",
    openAmount: "600.00",
    cashDiscountAmount: "0",
    dunningLevel: 0,
    dunningBlock: false,
    status: "PARTIALLY_SETTLED",
    version: 1,
    side: "DEBIT",
    importSource: "SAP-PRD",
    legacyKey: `K-${id}`,
    ...over,
  };
}

describe("imported opening items", () => {
  it("shows the totals per source and, for a chosen source, its items with credits negative", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/open-item-imports")) return json(200, { items: [summary] });
      if (url.pathname.endsWith("/open-items")) {
        return json(200, { items: [item("1", {}), item("2", { sourceType: "CREDIT_NOTE", side: "CREDIT", amount: "-200.00", openAmount: "-120.00", status: "PARTIALLY_SETTLED" })] });
      }
      return undefined;
    };
    const { container } = renderAt("/receivables/opening-items?source=SAP-PRD");
    const totals = await findTable({ name: "Import totals" });
    await waitFor(() => expect(totals).toHaveTextContent("780.00 USD"));
    expect(totals).toHaveTextContent("-120.00 USD");
    const list = await findTable({ name: "Imported items" });
    await waitFor(() => expect(list).toHaveTextContent("K-2"));
    expect(list).toHaveTextContent("CREDIT");
    expect(list).toHaveTextContent("-200.00 USD");
    const call = server.calls.find((c) => c.url.pathname.endsWith("/open-items"));
    expect(call?.url.searchParams.get("importSource")).toBe("SAP-PRD");
    expect(call?.url.searchParams.get("status")).toBe("all");
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks for a source before listing items, and refuses a malformed one", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-imports") ? json(200, { items: [summary] }) : undefined);
    const first = renderAt("/receivables/opening-items");
    expect(await screen.findByText(/Choose an import source/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/open-items"))).toBe(false);
    first.unmount();
    renderAt("/receivables/opening-items?source=sap%20prd");
    expect(await screen.findByText(/Use the legacy system's label/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/open-items"))).toBe(false);
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname.endsWith("/open-item-imports") ? json(200, { items: [] }) : json(200, { items: [] }));
    renderAt("/receivables/opening-items?source=AX-EU");
    expect(await screen.findByText("Nothing imported yet")).toBeInTheDocument();
    expect(await screen.findByText("No items from this source")).toBeInTheDocument();
  });

  it("shows an error with a retry when the totals fail", async () => {
    server.handler = () => json(500, { code: "COM-0500", title: "Internal error", detail: "boom" });
    renderAt("/receivables/opening-items");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Try again");
  });
});
