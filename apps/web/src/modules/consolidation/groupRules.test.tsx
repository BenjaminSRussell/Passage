import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { samplePairs, validateIcDifferences } from "./IcDifferencesPage";
import { ac4014, sampleDivestiture, validateDivestiture, validateEquity } from "./InvestmentsPage";
import consolidation from "./module";
import { ac4012, validateProfit } from "./ProfitInInventoryPage";
import { ac4015, validateUnits } from "./ValidationPage";

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

const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((c) => c.textContent);
const bodyRows = (table: HTMLElement) => within(table).getAllByRole("row").slice(1);

describe("profit in inventory page", () => {
  it("computes AC-40.12 per receipt layer", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/profit-in-inventory:preview"
        ? { status: 200, body: { currency: "EUR", layers: [{ id: "RCPT-0042", basis: "GroupCost", profit: "800.00" }], total: "800.00", fallbackCount: 0, warnings: [] } }
        : undefined;
    const { container } = renderAt("/consolidation/profit-in-inventory");
    expect(screen.getByRole("heading", { name: "No unrealized profit yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Compute unrealized profit" }));
    await screen.findByRole("list", { name: "Unrealized profit" });
    const table = screen.getByRole("table", { name: "Unrealized profit by layer" });
    expect(calls[0].body).toEqual({ currency: "EUR", layers: [{ id: "RCPT-0042", quantity: "40", legalUnitCost: "120.00", groupUnitCost: "100.00" }] });
    expect(screen.getByRole("list", { name: "Unrealized profit" })).toHaveTextContent("To eliminate 800.00 EUR");
    expect(bodyRows(table).map(cells)).toEqual([["RCPT-0042", "Group cost", "800.00"]]);
    expect(screen.queryByText("Group cost unknown")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the CNS-0908 fallback warning", async () => {
    handler = () => ({
      status: 200,
      body: {
        currency: "EUR",
        layers: [{ id: "layer-1", basis: "MarginFallback", profit: "480.00" }],
        total: "480.00",
        fallbackCount: 1,
        warnings: [{ code: "CNS-0908", message: "group cost unknown for 1 layers; margin fallback used" }],
      },
    });
    renderAt("/consolidation/profit-in-inventory");
    fireEvent.change(screen.getByLabelText("Group unit cost (supplier)"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Seller's average margin (%)"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Compute unrealized profit" }));
    expect(await screen.findByText("Group cost unknown")).toBeInTheDocument();
    expect(calls[0].body).toMatchObject({ sellerMarginPct: "10", layers: [{ quantity: "40", legalUnitCost: "120.00" }] });
    expect((calls[0].body as { layers: { groupUnitCost?: string }[] }).layers[0].groupUnitCost).toBeUndefined();
    expect(bodyRows(screen.getByRole("table", { name: "Unrealized profit by layer" })).map(cells)).toEqual([["layer-1", "Margin fallback", "480.00"]]);
  });

  it("requires the seller margin for a layer without group cost, and shows service refusals", async () => {
    expect(validateProfit({ ...ac4012, layers: [{ ...ac4012.layers[0], groupUnitCost: "" }] }).errors.sellerMarginPct).toBeDefined();
    expect(validateProfit({ ...ac4012, head: { currency: "EUR", sellerMarginPct: "150" } }).errors.sellerMarginPct).toBe("Enter a margin from 0 to 100.");
    expect(validateProfit({ ...ac4012, layers: [{ ...ac4012.layers[0], quantity: "-1" }] }).errors["layers[0].quantity"]).toBeDefined();
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "layers[0].legalUnitCost": "is required" } } });
    renderAt("/consolidation/profit-in-inventory");
    fireEvent.click(screen.getByRole("button", { name: "Compute unrealized profit" }));
    await waitFor(() => expect(screen.getByLabelText("Legal unit cost (holder)")).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByText("The unrealized profit was not computed")).toBeInTheDocument();
  });
});

describe("intercompany differences page", () => {
  const preview = {
    currency: "USD",
    threshold: "1000.00",
    taskStatus: "Error",
    pairs: [
      {
        pair: "P-S1",
        receivable: "50000.00",
        payable: "49400.00",
        difference: "600.00",
        status: "Eliminated",
        entries: [
          { rule: "E2", account: "IC_PAYABLE", debit: "49400.00", credit: "0.00" },
          { rule: "E2", account: "IC_RECEIVABLE", debit: "0.00", credit: "50000.00" },
          { rule: "E2", account: "IC_DIFFERENCE", debit: "600.00", credit: "0.00" },
        ],
      },
      {
        pair: "P-S2",
        receivable: "20000.00",
        payable: "22500.00",
        difference: "-2500.00",
        status: "Error",
        code: "CNS-0905",
        message: "IC difference 2500.00 exceeds threshold 1000.00 for P-S2",
        entries: [],
      },
    ],
  };

  it("eliminates pairs within the threshold and stops on the one above (BR-40.15)", async () => {
    handler = (url) => (url.pathname === "/api/v1/consol/ic-differences:preview" ? { status: 200, body: preview } : undefined);
    const { container } = renderAt("/consolidation/ic-differences");
    expect(screen.getByRole("heading", { name: "No elimination yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Eliminate pairs" }));
    expect(await screen.findByText("Elimination task ends in Error")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ currency: "USD", pairs: samplePairs.pairs });
    const pairs = bodyRows(screen.getByRole("table", { name: "Partner pairs" })).map(cells);
    expect(pairs[0]).toEqual(["P-S1", "50000.00", "49400.00", "600.00", "Eliminated", "—"]);
    expect(pairs[1]).toEqual(["P-S2", "20000.00", "22500.00", "-2500.00", "Above threshold", "IC difference 2500.00 exceeds threshold 1000.00 for P-S2 (CNS-0905)"]);
    const entries = bodyRows(screen.getByRole("table", { name: "Elimination entries" })).map(cells);
    expect(entries[2]).toEqual(["P-S1", "E2", "Intercompany difference", "IC_DIFFERENCE", "600.00", "—"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends a raised threshold and checks the pairs first", async () => {
    handler = () => ({ status: 200, body: { ...preview, taskStatus: "Completed", threshold: "3000.00", pairs: [preview.pairs[0]] } });
    renderAt("/consolidation/ic-differences");
    fireEvent.change(screen.getByLabelText("Difference threshold"), { target: { value: "3000" } });
    fireEvent.click(screen.getByRole("button", { name: "Eliminate pairs" }));
    expect(await screen.findByText("Every pair eliminated")).toBeInTheDocument();
    expect((calls[0].body as { threshold: string }).threshold).toBe("3000");
    expect(validateIcDifferences({ ...samplePairs, head: { currency: "USD", threshold: "-1" } }).errors.threshold).toBeDefined();
    expect(validateIcDifferences({ ...samplePairs, pairs: [{ pair: "", receivable: "1", payable: "1" }] }).errors["pairs[0].pair"]).toBeDefined();
    expect(validateIcDifferences({ ...samplePairs, pairs: [] }).errors.pairs).toBe("Add at least one partner pair.");
  });
});

describe("investments page", () => {
  it("applies the AC-40.14 equity method and lists its entries", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/equity-method:preview"
        ? {
            status: 200,
            body: {
              currency: "USD",
              shareOfProfit: "60000.00",
              unrecognizedLoss: "0.00",
              closingCarrying: "340000.00",
              balanced: true,
              entries: [
                { rule: "EQ", account: "INVESTMENT_ASSOCIATE", debit: "60000.00", credit: "0.00" },
                { rule: "EQ", account: "SHARE_OF_PROFIT_ASSOCIATES", debit: "0.00", credit: "60000.00" },
                { rule: "EQ", account: "DIVIDEND_INCOME", debit: "20000.00", credit: "0.00" },
                { rule: "EQ", account: "INVESTMENT_ASSOCIATE", debit: "0.00", credit: "20000.00" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/investments");
    expect(screen.getByRole("heading", { name: "No equity-method result yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Apply equity method" }));
    await screen.findByRole("list", { name: "Equity method" });
    const table = screen.getByRole("table", { name: "Equity-method entries" });
    expect(calls[0].body).toEqual({ currency: "USD", ownershipPct: "30", openingCarrying: "300000.00", netIncome: "200000.00", dividendsReceived: "20000.00" });
    const tiles = screen.getByRole("list", { name: "Equity method" });
    expect(tiles).toHaveTextContent("Share of profit 60000.00 USD");
    expect(tiles).toHaveTextContent("Closing carrying amount 340000.00");
    expect(tiles).not.toHaveTextContent("Unrecognized loss");
    expect(bodyRows(table).map(cells)[0]).toEqual(["EQ", "Investment in associate", "INVESTMENT_ASSOCIATE", "60000.00", "—"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("computes a divestiture loss and explains a refused equity method", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/divestitures:preview"
        ? { status: 200, body: { currency: "USD", carryingAmount: "330000.00", gain: "-20000.00" } }
        : { status: 400, body: { code: "COM-0001", detail: "equity method applies above 0 and at most 50 percent ownership" } };
    renderAt("/consolidation/investments");
    fireEvent.change(screen.getByLabelText("Proceeds"), { target: { value: "300000" } });
    fireEvent.click(screen.getByRole("button", { name: "Compute gain" }));
    const tiles = await screen.findByRole("list", { name: "Divestiture" });
    expect(tiles).toHaveTextContent("Carrying amount disposed 330000.00 USD");
    expect(tiles).toHaveTextContent("Loss on disposal -20000.00");
    expect(calls[0].body).toMatchObject({ proceeds: "300000", netAssetsCarrying: "300000.00", ctaRecycled: "10000.00" });

    fireEvent.click(screen.getByRole("button", { name: "Apply equity method" }));
    await waitFor(() => expect(screen.getByText("The equity method was not applied")).toBeInTheDocument());
    expect(screen.getByText("The equity method was not applied").closest("[role=alert]")).toHaveTextContent("(COM-0001)");
  });

  it("checks the inputs before sending", () => {
    expect(validateEquity({ ...ac4014, ownershipPct: "60" }).errors.ownershipPct).toBe("Enter an ownership above 0 and at most 50.");
    expect(validateEquity({ ...ac4014, ownershipPct: "50" }).request).toBeDefined();
    expect(validateEquity({ ...ac4014, netIncome: "-500000" }).request?.netIncome).toBe("-500000");
    expect(validateEquity({ ...ac4014, dividendsReceived: "" }).request?.dividendsReceived).toBeUndefined();
    expect(validateDivestiture({ ...sampleDivestiture, goodwill: "-1" }).errors.goodwill).toBeDefined();
    expect(validateDivestiture({ ...sampleDivestiture, ctaRecycled: "-5000" }).request?.ctaRecycled).toBe("-5000");
    renderAt("/consolidation/investments");
    fireEvent.change(screen.getByLabelText("Opening carrying amount"), { target: { value: "-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply equity method" }));
    expect(screen.getByText("Enter the carrying amount, 0 or more.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("unit validation page", () => {
  it("fails AC-40.15 on S1 and blocks the group translation", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/validations:preview"
        ? {
            status: 200,
            body: {
              groupTranslation: "Blocked",
              units: [
                {
                  unitId: "S1",
                  status: "Error",
                  rules: [
                    { rule: "BS_BALANCES", severity: "Error", tolerance: "0.01", difference: "12", failed: true, code: "CNS-0906", message: "validation BS_BALANCES failed for S1: 12" },
                    { rule: "RE_ROLL", severity: "Error", tolerance: "1", difference: "0", failed: false },
                    { rule: "IC_PARTNER_PRESENT", severity: "Warning", tolerance: "0", difference: "0", failed: false },
                  ],
                },
                {
                  unitId: "S2",
                  status: "Completed",
                  rules: [
                    { rule: "BS_BALANCES", severity: "Error", tolerance: "0.01", difference: "0", failed: false },
                    { rule: "RE_ROLL", severity: "Error", tolerance: "1", difference: "0", failed: false },
                    { rule: "IC_PARTNER_PRESENT", severity: "Warning", tolerance: "0", difference: "0", failed: false },
                  ],
                },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/validation");
    expect(screen.getByRole("heading", { name: "No validation yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Validate units" }));
    expect(await screen.findByText("Group translation is blocked")).toBeInTheDocument();
    const sent = (calls[0].body as { units: { unitId: string; totalAssets: string; icItemsWithoutPartner?: number }[] }).units;
    expect(sent.map((u) => [u.unitId, u.totalAssets])).toEqual([
      ["S1", "1012.00"],
      ["S2", "1000.00"],
    ]);
    expect(sent[0].icItemsWithoutPartner).toBeUndefined();
    expect(bodyRows(screen.getByRole("table", { name: "Validate tasks" })).map(cells)).toEqual([
      ["S1", "Error", "1"],
      ["S2", "Completed", "0"],
    ]);
    const rules = bodyRows(screen.getByRole("table", { name: "Rule results" })).map(cells);
    expect(rules[0]).toEqual(["S1", "Total assets = liabilities + equity", "Error", "12", "0.01", "CNS-0906"]);
    expect(rules[2][4]).toBe("—");
    expect(pageViolations(container)).toEqual([]);
  });

  it("lets translation start when only warnings fail, and checks the units first", async () => {
    handler = () => ({
      status: 200,
      body: {
        groupTranslation: "NotStarted",
        units: [{ unitId: "S1", status: "CompletedWithWarnings", rules: [{ rule: "IC_PARTNER_PRESENT", severity: "Warning", tolerance: "0", difference: "1", failed: true, code: "CNS-0906" }] }],
      },
    });
    renderAt("/consolidation/validation");
    fireEvent.click(screen.getByRole("button", { name: "Validate units" }));
    expect(await screen.findByText("Group translation can start")).toBeInTheDocument();
    expect(bodyRows(screen.getByRole("table", { name: "Validate tasks" })).map(cells)).toEqual([["S1", "Completed with warnings", "1"]]);
    expect(validateUnits([...ac4015, { ...ac4015[0] }]).errors["units[2].unitId"]).toBe("This unit is already listed.");
    expect(validateUnits([{ ...ac4015[0], totalEquity: "" }]).errors["units[0].totalEquity"]).toBe("Enter an amount.");
    expect(validateUnits([{ ...ac4015[0], icItemsWithoutPartner: "2" }]).request?.units[0].icItemsWithoutPartner).toBe(2);
    expect(validateUnits([]).errors.units).toBe("Add at least one unit.");
  });

  it("shows a refusal in an alert", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty consolidation.run" } });
    renderAt("/consolidation/validation");
    fireEvent.click(screen.getByRole("button", { name: "Validate units" }));
    await waitFor(() => expect(screen.getByText("The units were not validated")).toBeInTheDocument());
    expect(screen.getByText("The units were not validated").closest("[role=alert]")).toHaveTextContent("missing duty consolidation.run (AUTH-0003)");
    expect(screen.queryByRole("table", { name: "Validate tasks" })).not.toBeInTheDocument();
  });
});
