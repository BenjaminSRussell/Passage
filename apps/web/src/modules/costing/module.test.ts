import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import costing from "./module";
import { previewSettlements } from "./api/client";
import { parseTransactions } from "./pages";

describe("costing module", () => {
  it("declares its id, routes and nav under /costing", () => {
    expect(costing.id).toBe("costing");
    expect(costing.routes.length).toBeGreaterThan(0);
    expect(costing.nav.every((n) => n.to.startsWith("/costing"))).toBe(true);
  });

  it("routes every nav entry, each behind costing.read", () => {
    const paths = costing.routes.map((r) => r.path);
    expect(costing.nav.map((n) => n.to)).toEqual([
      "/costing",
      "/costing/valuation-ledgers",
      "/costing/aging",
      "/costing/aging/snapshots",
      "/costing/valuation-setup",
      "/costing/versions",
      "/costing/value-report",
      "/costing/cost-transactions",
      "/costing/adjustments",
      "/costing/reconciliations",
    ]);
    for (const n of costing.nav) {
      expect(paths).toContain(n.to);
      expect(n.duty).toBe("costing.read");
    }
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("costing");
  });

  it("renders the settlement preview page", () => {
    render(createElement(MemoryRouter, null, costing.routes[0].element));
    expect(screen.getByRole("heading", { name: "Settlement preview" })).toBeInTheDocument();
  });
});

describe("previewSettlements client", () => {
  it("posts to the BFF path and returns the parsed result", async () => {
    const body = { issueCosts: {}, settlements: [], exceptions: [], remainingQty: "0", remainingValue: "0.00", method: "FIFO", currency: "EUR" };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    const out = await previewSettlements({ method: "FIFO", currency: "EUR", transactions: [] }, fetchMock as unknown as typeof fetch);
    expect(out.method).toBe("FIFO");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/costing/settlements:preview");
    expect(init.method).toBe("POST");
  });

  it("surfaces the stable problem code on refusal", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ title: "bad", code: "CST-TXN-001", detail: "dup" }), { status: 422 }));
    await expect(previewSettlements({ method: "FIFO", currency: "EUR", transactions: [] }, fetchMock as unknown as typeof fetch)).rejects.toMatchObject({
      problem: { code: "CST-TXN-001", status: 422 },
    });
  });
});

describe("parseTransactions", () => {
  it("accepts a non-empty array and rejects anything else", () => {
    expect(parseTransactions('[{"id":"a"}]').ok).toBe(true);
    expect(parseTransactions("[]").ok).toBe(false);
    expect(parseTransactions("{").ok).toBe(false);
  });
});

describe("SettlementPreview page", () => {
  it("shows a validation message for malformed JSON without calling the API", async () => {
    const { SettlementPreview } = await import("./pages");
    render(createElement(MemoryRouter, null, createElement(SettlementPreview)));
    fireEvent.change(screen.getByLabelText("Transactions (JSON)"), { target: { value: "{" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not valid JSON");
    await waitFor(() => expect(screen.queryByText("Settlements")).toBeNull());
  });
});

describe("SettlementPreview results", () => {
  it("sends the standard cost and fallback, then shows issue costs and exceptions", async () => {
    const { SettlementPreview } = await import("./pages");
    const body = {
      method: "STANDARD",
      currency: "EUR",
      issueCosts: { "iss-1": "1500.00" },
      settlements: [{ seq: 1, issueId: "iss-1", pool: "STANDARD", qty: "100", value: "1000.00" }],
      exceptions: [{ issueId: "iss-1", qty: "50", value: "500.00", fallback: "STANDARD" }],
      remainingQty: "0",
      remainingValue: "0.00",
    };
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    try {
      render(createElement(MemoryRouter, null, createElement(SettlementPreview)));
      fireEvent.change(screen.getByLabelText("Costing method"), { target: { value: "STANDARD" } });
      fireEvent.change(screen.getByLabelText("Unsettled issue fallback"), { target: { value: "STANDARD" } });
      fireEvent.change(screen.getByLabelText("Standard unit cost"), { target: { value: " 10.00 " } });
      fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
      expect(await screen.findByRole("heading", { name: "Exceptions" })).toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Issue costs" })).toBeInTheDocument();
      expect(screen.getByText("1500.00")).toBeInTheDocument();
      expect(screen.getByText("500.00")).toBeInTheDocument();
      const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
      expect(JSON.parse(init.body as string)).toMatchObject({ method: "STANDARD", fallback: "STANDARD", standardUnitCost: "10.00" });
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("omits an empty standard cost and shows the problem code on refusal", async () => {
    const { SettlementPreview } = await import("./pages");
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify({ title: "Unprocessable Entity", code: "CST-TXN-001", detail: "STANDARD needs a positive standard unit cost" }), { status: 422 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    try {
      render(createElement(MemoryRouter, null, createElement(SettlementPreview)));
      fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("CST-TXN-001");
      const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
      expect(JSON.parse(init.body as string)).not.toHaveProperty("standardUnitCost");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
