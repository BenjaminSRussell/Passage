import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CommodityBasis, CommodityCalculation, CommodityIndexValue, CommodityRun, CommodityRunPreview } from "./api/client";
import { calcState, describeFormula, describeTarget } from "./commodityFormat";
import { emptyBasis, toBasisInput, validateBasis } from "./NewCommodityBasisPage";
import { emptyCalculation, toCalculationInput, validateCalculation } from "./NewCommodityCalculationPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const BASIS = "0190f0a0-0000-7000-8000-0000000000b1";
const CALC = "0190f0a0-0000-7000-8000-0000000000c1";
const JOURNAL = "0190f0a0-0000-7000-8000-0000000000d1";

const basis: CommodityBasis = { id: BASIS, version: 1, legalEntityId: LE, code: "CU-LME", name: "LME copper cash", currency: "USD", unit: "MT" };
const quote: CommodityIndexValue = { basisId: BASIS, asOf: "2026-06-08", revision: 2, value: "9100", source: "LME", affectedAgreements: 3 };
const calc: CommodityCalculation = {
  id: CALC,
  version: 1,
  withdrawn: false,
  legalEntityId: LE,
  name: "Copper rod EA",
  basisId: BASIS,
  factor: "0.0012",
  marginKind: "PERCENT",
  margin: "10",
  rounding: 2,
  maxIndexAgeDays: 7,
  customerKind: "all",
  customerKey: "",
  productId: "ROD",
  unit: "EA",
  currency: "USD",
};
const plan: CommodityRunPreview = {
  calculationId: CALC,
  asOf: "2026-06-10",
  index: quote,
  price: "12.01",
  stale: false,
  indexAgeDays: 2,
  warnings: [],
  changes: [{ recordId: "rec-1", version: 3, currentAmount: "9", newAmount: "12.01" }],
  unchangedCount: 1,
};
const run: CommodityRun = {
  id: "run-1",
  legalEntityId: LE,
  calculationId: CALC,
  asOf: "2026-06-10",
  indexAsOf: "2026-06-08",
  indexRevision: 2,
  indexValue: "9100",
  price: "12.01",
  changedCount: 1,
  unchangedCount: 1,
  stale: false,
  journalId: JOURNAL,
  runBy: "u-runner",
  warnings: [],
  createdAt: "2026-06-10T08:00:00Z",
};

function fill(label: string, value: string) {
  const field = screen.queryByLabelText(label) ?? screen.getByLabelText(new RegExp(`^${label}`));
  fireEvent.change(field, { target: { value } });
}

describe("commodity formatting", () => {
  it("describes the formula and the target", () => {
    expect(describeFormula(calc)).toBe("index x 0.0012 + 10%, 2 decimals");
    expect(describeFormula({ ...calc, marginKind: "AMOUNT", margin: "1.5" })).toBe("index x 0.0012 + 1.5 USD, 2 decimals");
    expect(describeFormula({ ...calc, margin: "0" })).toBe("index x 0.0012, 2 decimals");
    expect(describeTarget(calc)).toBe("ROD per EA in USD for all customers");
    expect(describeTarget({ ...calc, customerKind: "customer_group", customerKey: "OEM" })).toBe("ROD per EA in USD for group OEM");
    expect(calcState(calc)).toBe("active");
    expect(calcState({ withdrawn: true })).toBe("withdrawn");
  });
});

describe("Commodity cost basis types", () => {
  it("lists them and opens one with its quotes", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/commodity/bases")) return json(200, { items: [basis] });
      if (url.pathname.endsWith(`/commodity/bases/${BASIS}/index-values`)) return json(200, { items: [quote, { ...quote, revision: 1, value: "9000" }] });
      if (url.pathname.endsWith(`/commodity/bases/${BASIS}`)) return json(200, basis);
      return undefined;
    };
    const { container } = renderAt("/pricing/commodity/bases");
    const table = await findTable({ name: "Commodity cost basis types" });
    await waitFor(() => expect(table).toHaveTextContent("LME copper cash"));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "CU-LME" }));
    expect(await screen.findByRole("heading", { name: "CU-LME: LME copper cash" })).toBeInTheDocument();
    const quotes = await findTable({ name: "Index quotes" });
    await waitFor(() => expect(within(quotes).getAllByRole("row")).toHaveLength(3));
    expect(within(quotes).getAllByRole("row")[1]).toHaveTextContent("9100");
    expect(within(quotes).getAllByRole("columnheader", { name: "Agreements affected" })).toHaveLength(1);
    expect(within(quotes).getAllByRole("row")[1]).toHaveTextContent("3");
  });

  it("shows the empty state and the error state with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/commodity/bases");
    expect(await screen.findByText("No cost basis types")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" });
    renderAt("/pricing/commodity/bases");
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("validates like the service and trims the input", () => {
    const ok = { ...emptyBasis, legalEntityId: LE, code: "CU-LME", name: "Copper", unit: "MT" };
    expect(validateBasis(ok)).toEqual({});
    expect(Object.keys(validateBasis({ ...emptyBasis, code: "cu lme", currency: "usd" })).sort()).toEqual(["code", "currency", "legalEntityId", "name", "unit"]);
    expect(toBasisInput({ ...ok, code: " CU-LME ", name: " Copper " })).toMatchObject({ code: "CU-LME", name: "Copper" });
  });

  it("creates a cost basis type with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/commodity/bases")) return json(201, basis);
      if (url.pathname.endsWith(`/commodity/bases/${BASIS}/index-values`)) return json(200, { items: [] });
      if (url.pathname.endsWith(`/commodity/bases/${BASIS}`)) return json(200, basis);
      return undefined;
    };
    const { container } = renderAt("/pricing/commodity/bases/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Code", "CU-LME");
    fill("Name", "LME copper cash");
    fill("Quoted per", "MT");
    fireEvent.click(screen.getByRole("button", { name: "Save cost basis type" }));
    expect(await screen.findByRole("heading", { name: "CU-LME: LME copper cash" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, code: "CU-LME", name: "LME copper cash", currency: "USD", unit: "MT" });
    expect(idempotencyKey(post)).toBeTruthy();
    expect(await screen.findByText("No index quotes")).toBeInTheDocument();
  });

  it("checks the form first and shows a duplicate code as a failure", async () => {
    renderAt("/pricing/commodity/bases/new");
    fireEvent.click(screen.getByRole("button", { name: "Save cost basis type" }));
    expect(await screen.findByText("Choose a legal entity.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    server.handler = () => json(409, { code: "PRC-0054", detail: "a cost basis type with code CU-LME already exists in this legal entity" });
    fill("Legal entity", LE);
    fill("Code", "CU-LME");
    fill("Name", "LME copper cash");
    fill("Quoted per", "MT");
    fireEvent.click(screen.getByRole("button", { name: "Save cost basis type" }));
    expect(await findAlert()).toHaveTextContent("PRC-0054");
  });

  it("records an index quote and reloads the quotes", async () => {
    let posted = false;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/index-values")) {
        posted = true;
        return json(201, quote);
      }
      if (url.pathname.endsWith("/index-values")) return json(200, { items: posted ? [quote] : [] });
      if (url.pathname.endsWith(`/commodity/bases/${BASIS}`)) return json(200, basis);
      return undefined;
    };
    renderAt(`/pricing/commodity/bases/${BASIS}`);
    expect(await screen.findByText("No index quotes")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record quote" }));
    expect(await screen.findByText("Enter the day as YYYY-MM-DD.")).toBeInTheDocument();
    fill("Day", "2026-06-08");
    fill("Index value", "9100");
    fill("Source", "LME");
    fireEvent.click(screen.getByRole("button", { name: "Record quote" }));
    expect(await screen.findByText("Recorded 9100 for 2026-06-08 as revision 2.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ asOf: "2026-06-08", value: "9100", source: "LME" });
    expect(idempotencyKey(post)).toBeTruthy();
    const quotes = await findTable({ name: "Index quotes" });
    await waitFor(() => expect(quotes).toHaveTextContent("9100"));
  });
});

describe("Commodity price calculations", () => {
  it("lists calculations with their formula and status", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/commodity/calculations")) return json(200, { items: [calc, { ...calc, id: "c-2", name: "Old", withdrawn: true }] });
      return undefined;
    };
    const { container } = renderAt("/pricing/commodity/calculations");
    const table = await findTable({ name: "Commodity price calculations" });
    await waitFor(() => expect(table).toHaveTextContent("Copper rod EA"));
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("index x 0.0012 + 10%, 2 decimals");
    expect(rows[1]).toHaveTextContent("Active");
    expect(rows[2]).toHaveTextContent("Withdrawn");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/commodity/calculations");
    expect(await screen.findByText("No price calculations")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/pricing/commodity/calculations");
    expect(await findAlert()).toHaveTextContent("COM-0500");
  });

  it("validates like the service", () => {
    const ok = { ...emptyCalculation, legalEntityId: LE, basisId: BASIS, name: "Cu", factor: "0.0012", productId: "ROD", unit: "EA" };
    expect(validateCalculation(ok)).toEqual({});
    expect(
      Object.keys(validateCalculation({ ...ok, factor: "0", rounding: 7, maxIndexAgeDays: 0, customerKind: "customer", margin: "x", currency: "usd" })).sort(),
    ).toEqual(["currency", "customerKey", "factor", "margin", "maxIndexAgeDays", "rounding"]);
    expect(validateCalculation({ ...ok, marginKind: "PERCENT", margin: "1001" }).margin).toBeTruthy();
    expect(toCalculationInput({ ...ok, customerKey: "ignored", margin: "" })).toMatchObject({ customerKey: "", margin: "0" });
  });

  it("creates a calculation and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/commodity/calculations")) return json(201, calc);
      if (url.pathname.endsWith(`/calculations/${CALC}/runs`)) return json(200, { items: [] });
      if (url.pathname.endsWith(`/calculations/${CALC}`)) return json(200, calc);
      return undefined;
    };
    const { container } = renderAt("/pricing/commodity/calculations/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Name", "Copper rod EA");
    fill("Cost basis type id", BASIS);
    fill("Conversion factor", "0.0012");
    fill("Margin percentage", "10");
    fill("Decimals", "2");
    fill("Product", "ROD");
    fill("Unit", "EA");
    fireEvent.click(screen.getByRole("button", { name: "Save calculation" }));
    expect(await screen.findByRole("heading", { name: "Copper rod EA" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      legalEntityId: LE,
      name: "Copper rod EA",
      basisId: BASIS,
      factor: "0.0012",
      marginKind: "PERCENT",
      margin: "10",
      rounding: 2,
      maxIndexAgeDays: 7,
      customerKind: "all",
      customerKey: "",
      productId: "ROD",
      unit: "EA",
      currency: "USD",
    });
    expect(idempotencyKey(post)).toBeTruthy();
  });

  it("shows an unknown cost basis type as a failure", async () => {
    server.handler = () => json(404, { code: "PRC-0050", detail: "no commodity cost basis type x" });
    renderAt("/pricing/commodity/calculations/new");
    fill("Legal entity", LE);
    fill("Name", "Copper rod EA");
    fill("Cost basis type id", BASIS);
    fill("Conversion factor", "0.0012");
    fill("Product", "ROD");
    fill("Unit", "EA");
    fireEvent.click(screen.getByRole("button", { name: "Save calculation" }));
    expect(await findAlert()).toHaveTextContent("PRC-0050");
  });
});

describe("Commodity calculation detail", () => {
  function serve(extra: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined = () => undefined) {
    let runs: CommodityRun[] = [];
    server.handler = (url, init) => {
      const custom = extra(url, init);
      if (custom) return custom;
      if (init.method === "POST" && url.pathname.endsWith(":preview")) return json(200, plan);
      if (init.method === "POST" && url.pathname.endsWith(":run")) {
        runs = [run];
        return json(201, run);
      }
      if (init.method === "POST" && url.pathname.endsWith(":withdraw")) return json(200, { ...calc, withdrawn: true, version: 2 });
      if (url.pathname.endsWith("/runs")) return json(200, { items: runs });
      if (url.pathname.endsWith(`/calculations/${CALC}`)) return json(200, calc);
      return undefined;
    };
  }

  it("previews a run, then runs it and links the journal", async () => {
    serve();
    const { container } = renderAt(`/pricing/commodity/calculations/${CALC}`);
    expect(await screen.findByRole("heading", { name: "Copper rod EA" })).toBeInTheDocument();
    expect(await screen.findByText("No runs yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill("Run date", "2026-06-10");
    fireEvent.click(screen.getByRole("button", { name: "Preview run" }));
    const changes = await findTable({ name: "Prices the run would change" });
    expect(changes).toHaveTextContent("12.01");
    expect(screen.getByText(/gives a price of/)).toHaveTextContent("Index 9100 from 2026-06-08 (revision 2) gives a price of 12.01 USD.");
    expect(server.calls.find((c) => c.url.pathname.endsWith(":preview"))!.body).toEqual({ asOf: "2026-06-10" });

    fireEvent.click(screen.getByRole("button", { name: "Run calculation" }));
    expect(await screen.findByText(/Run complete: 1 price changed to 12.01/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith(":run"))!;
    expect(call.body).toEqual({ asOf: "2026-06-10" });
    expect(idempotencyKey(call)).toBeTruthy();
    const table = await findTable({ name: "Calculation runs" });
    await waitFor(() => expect(table).toHaveTextContent("Review journal"));
    expect(within(table).getByRole("link", { name: "Review journal" })).toHaveAttribute("href", `/pricing/journals/${JOURNAL}`);
    expect(table).toHaveTextContent("u-runner");
  });

  it("carries the stale-index warning into the run result", async () => {
    serve((url, init) =>
      init.method === "POST" && url.pathname.endsWith(":run")
        ? json(201, { ...run, stale: true, warnings: [{ code: "PRC-0016", detail: "the newest index value is from 2026-05-01" }] })
        : undefined,
    );
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    await screen.findByRole("heading", { name: "Copper rod EA" });
    fill("Run date", "2026-06-10");
    fireEvent.click(screen.getByRole("button", { name: "Run calculation" }));
    expect(await screen.findByText(/Run complete: 1 price changed to 12.01\. Another pricing manager reviews/)).toHaveTextContent("PRC-0016: the newest index value is from 2026-05-01.");
  });

  it("warns when the index is stale", async () => {
    serve((url, init) =>
      init.method === "POST" && url.pathname.endsWith(":preview")
        ? json(200, { ...plan, stale: true, indexAgeDays: 40, warnings: [{ code: "PRC-0016", detail: "the newest index value is from 2026-05-01, older than allowed" }] })
        : undefined,
    );
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    await screen.findByRole("heading", { name: "Copper rod EA" });
    fill("Run date", "2026-06-10");
    fireEvent.click(screen.getByRole("button", { name: "Preview run" }));
    expect(await screen.findByText("PRC-0016")).toBeInTheDocument();
    expect(screen.getByText(/older than allowed/)).toBeInTheDocument();
  });

  it("shows a missing index as a failure", async () => {
    serve((url, init) =>
      init.method === "POST" && url.pathname.endsWith(":preview") ? json(422, { code: "PRC-0052", detail: "no index value for the cost basis on or before 2026-06-10" }) : undefined,
    );
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    await screen.findByRole("heading", { name: "Copper rod EA" });
    fill("Run date", "2026-06-10");
    fireEvent.click(screen.getByRole("button", { name: "Preview run" }));
    expect(await findAlert()).toHaveTextContent("PRC-0052");
  });

  it("checks the run date before calling the service", async () => {
    serve();
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    await screen.findByRole("heading", { name: "Copper rod EA" });
    fill("Run date", "");
    fireEvent.click(screen.getByRole("button", { name: "Run calculation" }));
    expect(await screen.findByText("Enter the run date as YYYY-MM-DD.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("withdraws the calculation at its version and removes the run controls", async () => {
    serve();
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    await screen.findByRole("heading", { name: "Copper rod EA" });
    fireEvent.click(screen.getByRole("button", { name: "Withdraw calculation" }));
    expect(await screen.findByText(/This calculation is withdrawn/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("takes no further runs");
    expect(server.calls.find((c) => c.url.pathname.endsWith(":withdraw"))!.body).toEqual({ version: 1 });
    expect(screen.queryByRole("button", { name: "Run calculation" })).toBeNull();
  });

  it("shows the load error with a retry", async () => {
    server.handler = () => json(404, { code: "PRC-0051", detail: "no commodity price calculation x" });
    renderAt(`/pricing/commodity/calculations/${CALC}`);
    expect(await findAlert()).toHaveTextContent("PRC-0051");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
