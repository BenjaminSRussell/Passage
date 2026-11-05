import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import { validateEliminations, workedExample } from "./EliminationsPage";
import consolidation from "./module";
import { ac406, validateTranslation } from "./TranslationPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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
        {consolidation.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("consolidation module", () => {
  it("is discovered by the shell and keeps every route and nav entry under /consolidation with PRD duties", () => {
    expect(loadModules().some((m) => m.id === "consolidation")).toBe(true);
    expect(consolidation.routes.every((r) => r.element != null && r.path?.startsWith("/consolidation"))).toBe(true);
    expect(consolidation.nav.map((n) => n.to)).toEqual([
      "/consolidation",
      "/consolidation/eliminations",
      "/consolidation/translation",
      "/consolidation/profit-in-inventory",
      "/consolidation/ic-differences",
      "/consolidation/investments",
      "/consolidation/validation",
      "/consolidation/dividends",
      "/consolidation/reclassification",
      "/consolidation/group-journals",
    ]);
    expect(consolidation.nav.map((n) => n.duty)).toEqual(["consolidation.view", ...Array(8).fill("consolidation.run"), "consolidation.journal.maintain"]);
  });

  it("links the workspace to each tool", () => {
    const { container } = renderAt("/consolidation");
    expect(screen.getByRole("heading", { level: 2, name: "Consolidation workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Preview eliminations" })).toHaveAttribute("href", "/consolidation/eliminations");
    expect(screen.getByRole("link", { name: "Translate a unit" })).toHaveAttribute("href", "/consolidation/translation");
    expect(screen.getByRole("link", { name: "Compute unrealized profit" })).toHaveAttribute("href", "/consolidation/profit-in-inventory");
    expect(screen.getByRole("link", { name: "Eliminate partner pairs" })).toHaveAttribute("href", "/consolidation/ic-differences");
    expect(screen.getByRole("link", { name: "Apply the equity method" })).toHaveAttribute("href", "/consolidation/investments");
    expect(screen.getByRole("link", { name: "Validate units" })).toHaveAttribute("href", "/consolidation/validation");
    expect(screen.getByRole("link", { name: "Eliminate a dividend" })).toHaveAttribute("href", "/consolidation/dividends");
    expect(screen.getByRole("link", { name: "Dry-run the rules" })).toHaveAttribute("href", "/consolidation/reclassification");
    expect(screen.getByRole("link", { name: "Check a journal" })).toHaveAttribute("href", "/consolidation/group-journals");
    expect(pageViolations(container)).toEqual([]);
  });
});

const psPreview = {
  currency: "USD",
  entries: [
    { rule: "E1", account: "IC_REVENUE", debit: "108000.00", credit: "0.00" },
    { rule: "E1", account: "IC_COGS", debit: "0.00", credit: "108000.00" },
    { rule: "E2", account: "IC_PAYABLE", debit: "108500.00", credit: "0.00" },
    { rule: "E2", account: "IC_RECEIVABLE", debit: "0.00", credit: "110000.00" },
    { rule: "E2", account: "FX_GAIN_LOSS", debit: "1500.00", credit: "0.00" },
    { rule: "E4", account: "GOODWILL", debit: "33523.81", credit: "0.00" },
    { rule: "E4", account: "CTA", debit: "0.00", credit: "1523.81" },
  ],
  rules: {
    E2: { debit: "110000.00", credit: "110000.00" },
    E1: { debit: "108000.00", credit: "108000.00" },
    E3: { debit: "15552.00", credit: "15552.00" },
    E4: { debit: "768523.81", credit: "768523.81" },
    E5: { debit: "32400.00", credit: "32400.00" },
  },
  totals: { debit: "1034475.81", credit: "1034475.81" },
  balanced: true,
};

describe("elimination entries page", () => {
  it("previews the FR-40.9 worked example with totals per rule and readable accounts", async () => {
    handler = (url) => (url.pathname === "/api/v1/consol/eliminations:preview" ? { status: 200, body: psPreview } : undefined);
    const { container } = renderAt("/consolidation/eliminations");
    expect(screen.getByRole("heading", { name: "No eliminations yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview eliminations" }));
    await screen.findByText("Every rule balances");
    const entries = screen.getByRole("table", { name: "Elimination entries" });
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toEqual({
      currency: "USD",
      ownershipPct: "80",
      rates: { closing: "1.10", average: "1.08", historical: "1.05" },
      icSales: "100000",
      icCost: "70000",
      unsoldShare: "0.4",
      icReceivable: "100000",
      payableGroup: "108500",
      shareCapital: "500000",
      preAcqRe: "200000",
      investmentCost: "620000",
      netIncome: "150000",
      sellerIsSubsidiary: true,
    });
    expect(screen.getByText("Every rule balances").parentElement).toHaveTextContent("Total debits 1034475.81 and credits 1034475.81 USD");
    const rules = screen.getByRole("table", { name: "Totals by rule" });
    const ruleCodes = within(rules)
      .getAllByRole("row")
      .slice(1)
      .map((r) => within(r).getAllByRole("cell")[0].textContent);
    expect(ruleCodes).toEqual(["E1", "E2", "E3", "E4", "E5"]);
    const fx = within(entries).getByRole("row", { name: /Exchange gain or loss/ });
    expect(within(fx).getAllByRole("cell").map((c) => c.textContent)).toEqual(["E2", "Exchange gain or loss", "FX_GAIN_LOSS", "1500.00", "—"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends a downstream sale without the NCI share of unrealized profit", async () => {
    handler = () => ({ status: 200, body: psPreview });
    renderAt("/consolidation/eliminations");
    fireEvent.change(screen.getByLabelText("Direction of the sale"), { target: { value: "downstream" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview eliminations" }));
    await screen.findByText("Every rule balances");
    expect((calls[0].body as { sellerIsSubsidiary: boolean }).sellerIsSubsidiary).toBe(false);
  });

  it("explains a bargain purchase refusal and keeps the values", async () => {
    handler = () => ({ status: 422, body: { code: "CNS-0920", detail: "investment and NCI are below acquired equity by 6000.00" } });
    renderAt("/consolidation/eliminations");
    fireEvent.change(screen.getByLabelText("Investment cost"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview eliminations" }));
    await waitFor(() => expect(screen.getByText("The eliminations were not built")).toBeInTheDocument());
    expect(screen.getByText("The eliminations were not built").closest("[role=alert]")).toHaveTextContent(
      "investment and NCI are below acquired equity by 6000.00 (CNS-0920)",
    );
    expect(screen.getByLabelText("Investment cost")).toHaveValue("1");
    expect(screen.queryByRole("table", { name: "Elimination entries" })).not.toBeInTheDocument();
  });

  it("shows the service's field error next to the rate", async () => {
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "rates.closing": "is required" } } });
    renderAt("/consolidation/eliminations");
    fireEvent.click(screen.getByRole("button", { name: "Preview eliminations" }));
    await waitFor(() => expect(screen.getByLabelText("Closing rate")).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByText("is required")).toBeInTheDocument();
  });

  it("checks the inputs before sending", () => {
    expect(validateEliminations({ ...workedExample, ownershipPct: "120" }).errors.ownershipPct).toBeDefined();
    expect(validateEliminations({ ...workedExample, ownershipPct: "100" }).request).toBeDefined();
    expect(validateEliminations({ ...workedExample, unsoldShare: "1.5" }).errors.unsoldShare).toBe("Enter a fraction from 0 to 1.");
    expect(validateEliminations({ ...workedExample, icSales: "-5" }).errors.icSales).toBeDefined();
    expect(validateEliminations({ ...workedExample, netIncome: "-5" }).request?.netIncome).toBe("-5");
    expect(validateEliminations({ ...workedExample, icCost: "" }).request?.icCost).toBeUndefined();
    renderAt("/consolidation/eliminations");
    fireEvent.change(screen.getByLabelText("Average rate"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview eliminations" }));
    expect(screen.getByText("Enter a rate above 0.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("currency translation page", () => {
  it("translates AC-40.6 and shows the CTA with its NCI split", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/translations:preview"
        ? {
            status: 200,
            body: {
              currency: "USD",
              openingNetAssets: "735000.00",
              netIncome: "162000.00",
              closingNetAssets: "935000.00",
              cta: "38000.00",
              nciShareOfCta: "7600.00",
              groupShareOfCta: "30400.00",
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/translation");
    expect(screen.getByRole("heading", { name: "No translation yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Translate unit" }));
    await screen.findByRole("list", { name: "Translation difference" });
    const table = screen.getByRole("table", { name: "Translated statement" });
    expect(calls[0].body).toEqual({
      currency: "USD",
      ownershipPct: "80",
      rates: { opening: "1.05", average: "1.08", closing: "1.10" },
      openingNetAssets: "700000",
      netIncome: "150000",
      closingNetAssets: "850000",
    });
    const tiles = screen.getByRole("list", { name: "Translation difference" });
    expect(tiles).toHaveTextContent("CTA 38000.00 USD");
    expect(tiles).toHaveTextContent("NCI share 7600.00");
    expect(within(table).getByRole("row", { name: /Closing net assets/ })).toHaveTextContent("935000.00");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refusal in an alert", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty consolidation.run" } });
    renderAt("/consolidation/translation");
    fireEvent.click(screen.getByRole("button", { name: "Translate unit" }));
    await waitFor(() => expect(screen.getByText("The unit was not translated")).toBeInTheDocument());
    expect(screen.getByText("The unit was not translated").closest("[role=alert]")).toHaveTextContent("missing duty consolidation.run (AUTH-0003)");
    expect(screen.queryByRole("table", { name: "Translated statement" })).not.toBeInTheDocument();
  });

  it("checks the statement before sending", () => {
    expect(validateTranslation({ ...ac406, currency: "usd" }).request?.currency).toBe("USD");
    expect(validateTranslation({ ...ac406, netIncome: "-20000" }).request?.netIncome).toBe("-20000");
    expect(validateTranslation({ ...ac406, ownershipPct: "0" }).errors.ownershipPct).toBeDefined();
    renderAt("/consolidation/translation");
    fireEvent.change(screen.getByLabelText("Closing net assets"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Translate unit" }));
    expect(screen.getByText("Enter an amount.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
