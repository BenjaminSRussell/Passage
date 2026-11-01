import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import ledger from "./module";
import { TrialBalanceScreen } from "./pages";

const LEDGER_ID = "0190a000-0000-7000-8000-000000000001";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderScreen() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    createElement(
      QueryClientProvider,
      { client },
      createElement(MemoryRouter, null, createElement(TrialBalanceScreen)),
    ),
  );
}

describe("ledger module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(ledger.id).toBe("ledger");
    expect(ledger.routes.map((r) => r.path)).toEqual([
      "/ledger/journal-batches",
      "/ledger/journal-batches/new",
      "/ledger/journal-batches/:id",
      "/ledger/journal-names",
      "/ledger/journal-names/new",
      "/ledger/journal-names/:code",
      "/ledger/recurring-journals",
      "/ledger/recurring-journals/new",
      "/ledger/recurring-journals/:id",
      "/ledger/open-items",
      "/ledger/open-item-accounts",
      "/ledger/clearings",
      "/ledger/clearings/:id",
      "/ledger/tolerance-groups",
      "/ledger/tolerance-groups/new",
      "/ledger/tolerance-groups/:code",
      "/ledger/auto-clearing",
      "/ledger/auto-clearing-runs/:id",
      "/ledger/auto-clearing-rules",
      "/ledger/auto-clearing-rules/new",
      "/ledger/auto-clearing-rules/:id",
      "/ledger/lookup-tables",
      "/ledger/lookup-tables/new",
      "/ledger/lookup-tables/:id",
      "/ledger/splitting-rules",
      "/ledger/splitting-rules/new",
      "/ledger/splitting-rules/:id",
      "/ledger/realignments",
      "/ledger/realignments/new",
      "/ledger/realignments/:id",
      "/ledger/derivation-strategies",
      "/ledger/derivation-strategies/new",
      "/ledger/derivation-strategies/:version",
      "/ledger/validation-rules",
      "/ledger/validation-rules/new",
      "/ledger/validation-rules/:id",
      "/ledger/trial-balance",
      "/ledger/transactions",
      "/ledger/entries/:id",
      "/ledger/reason-codes",
      "/ledger/reason-codes/new",
      "/ledger/reason-codes/:code",
      "/ledger/structures",
      "/ledger/structures/new",
      "/ledger/structures/:id",
      "/ledger/dimension-values",
      "/ledger/description-templates",
      "/ledger/description-templates/new",
      "/ledger/description-templates/:id",
    ]);
    expect(ledger.nav.every((n) => n.to.startsWith("/ledger"))).toBe(true);
  });

  it("gates the trial balance nav on the duty the balances API enforces", () => {
    // services/ledger/internal/api: GET /api/v1/ledger/balances requires ledger.balance.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/trial-balance")?.duty).toBe("ledger.balance.read");
    // GET /api/v1/ledger/reason-codes requires ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/reason-codes")?.duty).toBe("ledger.journal.read");
    // GET .../account-structures and .../dimension-values require ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/structures")?.duty).toBe("ledger.journal.read");
    expect(ledger.nav.find((n) => n.to === "/ledger/dimension-values")?.duty).toBe("ledger.journal.read");
    // GET /api/v1/ledger/journal-batches and /journal-names require ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/journal-batches")?.duty).toBe("ledger.journal.read");
    expect(ledger.nav.find((n) => n.to === "/ledger/journal-names")?.duty).toBe("ledger.journal.read");
    // GET /api/v1/ledger/recurring-journals requires ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/recurring-journals")?.duty).toBe("ledger.journal.read");
    // GET /api/v1/ledger/open-items, /clearings, .../open-item-accounts and .../tolerance-groups require ledger.journal.read.
    for (const to of ["/ledger/open-items", "/ledger/clearings", "/ledger/open-item-accounts", "/ledger/tolerance-groups", "/ledger/auto-clearing", "/ledger/auto-clearing-rules"]) {
      expect(ledger.nav.find((n) => n.to === to)?.duty).toBe("ledger.journal.read");
    }
    // GET /api/v1/ledger/journal-lines requires ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/transactions")?.duty).toBe("ledger.journal.read");
    // GET .../lookup-tables and .../derivation-strategies require ledger.journal.read; the writes need ledger.rules.write (service only).
    for (const to of ["/ledger/lookup-tables", "/ledger/derivation-strategies", "/ledger/validation-rules", "/ledger/splitting-rules", "/ledger/realignments"]) {
      expect(ledger.nav.find((n) => n.to === to)?.duty).toBe("ledger.journal.read");
    }
    // GET /api/v1/ledger/description-templates requires ledger.journal.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/description-templates")?.duty).toBe("ledger.journal.read");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("ledger");
  });

  it("validates the ledger id before calling the API", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    renderScreen();
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    expect(screen.getByRole("alert").textContent).toContain("UUID");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("renders the trial balance rows and the balanced check", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        status: 200,
        json: async () => ({
          ledgerId: LEDGER_ID,
          currency: "USD",
          rows: [{ account: "1000", name: "Cash", debit: "100.00", credit: "0.00", net: "100.00", lineCount: 1 }],
          debit: "100.00",
          credit: "100.00",
          net: "0.00",
          balanced: true,
        }),
      })),
    );
    renderScreen();
    fireEvent.change(screen.getByLabelText("Ledger id"), { target: { value: LEDGER_ID } });
    fireEvent.click(screen.getByRole("button", { name: "Show trial balance" }));
    await waitFor(() => expect(screen.getByTestId("tb-balanced").textContent).toBe("Balanced"));
    expect(screen.getByRole("table", { name: "Trial balance" }).textContent).toContain("1000");
    expect(screen.getByText("Cash")).toBeInTheDocument();
  });
});
