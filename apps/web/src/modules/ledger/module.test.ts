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
    expect(ledger.routes.map((r) => r.path)).toEqual(["/ledger/trial-balance", "/ledger/entries/:id"]);
    expect(ledger.nav.every((n) => n.to.startsWith("/ledger"))).toBe(true);
  });

  it("gates the trial balance nav on the duty the balances API enforces", () => {
    // services/ledger/internal/api: GET /api/v1/ledger/balances requires ledger.balance.read.
    expect(ledger.nav.find((n) => n.to === "/ledger/trial-balance")?.duty).toBe("ledger.balance.read");
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
    expect(screen.getByRole("rowheader", { name: "1000" })).toBeInTheDocument();
    expect(screen.getByText("Cash")).toBeInTheDocument();
  });
});
