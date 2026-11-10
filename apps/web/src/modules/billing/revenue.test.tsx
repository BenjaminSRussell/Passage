import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { periodEndOf, type RecognitionRun, type RevenueContract, type RollForward } from "./api/client";
import { validateQuery } from "./DeferredRevenuePage";
import { lastMonth } from "./format";
import { emptyContract, toContractInput, validateContract } from "./NewRevenueContractPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-000000000002";
const ORDER = "0190f0a0-0000-7000-8000-000000000003";
const PRODUCT = "0190f0a0-0000-7000-8000-0000000000a1";
const CONTRACT = "0190f0a0-0000-7000-8000-000000000c01";
const INSTALL = "0190f0a0-0000-7000-8000-000000000b02";

/** The BR-29.1 bundle after creation: hardware recognized, installation pending, support ratable. */
const contract: RevenueContract = {
  id: CONTRACT,
  legalEntityId: LE,
  orderId: ORDER,
  customerId: CUST,
  currency: "USD",
  transactionPrice: "10800.00",
  recognized: "7200.00",
  remaining: "3600.00",
  residualRule: "LARGEST",
  ratableBasis: "DAILY",
  contractDate: "2027-03-10",
  status: "OPEN",
  version: 1,
  warnings: [],
  obligations: [
    {
      id: "0190f0a0-0000-7000-8000-000000000b01",
      lineNo: 1,
      productId: PRODUCT,
      ssp: "8000.00",
      sspSource: "EXPLICIT",
      allocated: "7200.00",
      recognized: "7200.00",
      method: "POINT_IN_TIME",
      satisfiedOn: "2027-03-10",
      schedule: [{ id: "s1", seq: 1, date: "2027-03-10", amount: "7200.00", status: "RECOGNIZED", recognizedOn: "2027-03-10" }],
    },
    {
      id: INSTALL,
      lineNo: 2,
      productId: PRODUCT,
      ssp: "1500.00",
      sspSource: "EXPLICIT",
      allocated: "1350.00",
      recognized: "0.00",
      method: "MILESTONE",
      schedule: [{ id: "s2", seq: 1, amount: "1350.00", status: "SCHEDULED" }],
    },
    {
      id: "0190f0a0-0000-7000-8000-000000000b03",
      lineNo: 3,
      productId: PRODUCT,
      ssp: "2500.00",
      sspSource: "EXPLICIT",
      allocated: "2250.00",
      recognized: "0.00",
      method: "RATABLE",
      termStart: "2027-03-10",
      termEnd: "2028-03-09",
      schedule: [
        { id: "s3", seq: 1, date: "2027-03-31", amount: "133.06", status: "SCHEDULED" },
        { id: "s4", seq: 2, date: "2027-04-30", amount: "187.50", status: "SCHEDULED" },
      ],
    },
  ],
};

const satisfied: RevenueContract = {
  ...contract,
  version: 2,
  recognized: "8550.00",
  remaining: "2250.00",
  obligations: contract.obligations.map((o) =>
    o.id === INSTALL
      ? { ...o, recognized: "1350.00", satisfiedOn: "2027-03-25", schedule: [{ ...o.schedule[0], date: "2027-03-25", status: "RECOGNIZED", recognizedOn: "2027-03-25" }] }
      : o,
  ),
};

/** The contract's own March 2027 roll-forward (AC-29.4 for one contract). */
const contractMarch: RollForward = {
  legalEntityId: LE,
  contractId: CONTRACT,
  currency: "USD",
  period: "2027-03",
  start: "2027-03-01",
  end: "2027-03-31",
  opening: "0.00",
  additions: "3600.00",
  recognized: "1483.06",
  adjustments: "0.00",
  closing: "2116.94",
};

function isRollForward(url: URL) {
  return url.pathname === "/api/v1/revrec/roll-forward";
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Revenue contracts list", () => {
  it("lists contracts with recognized and remaining and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/revrec/contracts") return json(200, { items: [contract] });
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) return json(200, contract);
      return undefined;
    };
    const { container } = renderAt("/billing/revenue-contracts");
    const table = await findTable({ name: "Revenue contracts" });
    await waitFor(() => expect(table).toHaveTextContent("10800.00 USD"));
    expect(table).toHaveTextContent("7200.00 USD");
    expect(table).toHaveTextContent("3600.00 USD");
    expect(table).toHaveTextContent("Open");
    expect(server.calls[0].url.searchParams.get("limit")).toBe("100");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "2027-03-10" }));
    expect(await screen.findByRole("heading", { name: "Revenue contract" })).toBeInTheDocument();
  });

  it("filters by status and a complete customer id only", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/billing/revenue-contracts");
    expect(await screen.findByText("No revenue contracts yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "COMPLETE" } });
    expect(await screen.findByText("No contracts match these filters")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("COMPLETE");
    const calls = server.calls.length;
    fill("Customer", "0190f0a0");
    expect(screen.queryByText("Enter a full customer id (a UUID) to filter.")).toBeNull();
    expect(server.calls.length).toBe(calls);
    fill("Customer", CUST);
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("customerId")).toBe(CUST));
  });

  it("filters by obligation method and loads the next page through the cursor", async () => {
    const older = { ...contract, id: "0190f0a0-0000-7000-8000-000000000c00", contractDate: "2027-02-01", transactionPrice: "500.00" };
    server.handler = (url) =>
      url.searchParams.get("cursor") === "next" ? json(200, { items: [older] }) : json(200, { items: [contract], nextCursor: "next" });
    const { container } = renderAt("/billing/revenue-contracts");
    const table = await findTable({ name: "Revenue contracts" });
    await waitFor(() => expect(table).toHaveTextContent("10800.00 USD"));
    fireEvent.change(screen.getByLabelText("Obligation method"), { target: { value: "RATABLE" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("method")).toBe("RATABLE"));
    fireEvent.click(await screen.findByRole("button", { name: "Load more contracts" }));
    await waitFor(() => expect(table).toHaveTextContent("500.00 USD"));
    const more = server.calls.at(-1)!.url.searchParams;
    expect(more.get("cursor")).toBe("next");
    expect(more.get("method")).toBe("RATABLE");
    expect(screen.queryByRole("button", { name: "Load more contracts" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the error state with a retry", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty revrec.view" });
    renderAt("/billing/revenue-contracts");
    expect(await findAlert()).toHaveTextContent("missing duty revrec.view (AUTH-0003)");
    server.handler = () => json(200, { items: [contract] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Revenue contracts" })).toHaveTextContent("10800.00 USD");
  });
});

describe("Revenue contract detail", () => {
  it("shows the allocation and schedules and satisfies a milestone with If-Match", async () => {
    let reads = 0;
    server.handler = (url, init) => {
      if (isRollForward(url)) return json(200, contractMarch);
      if (url.pathname === `/api/v1/revrec/milestones/${INSTALL}:satisfy` && init.method === "POST") {
        return json(200, { contractId: CONTRACT, contractVersion: 2, obligationId: INSTALL, scheduleId: "s2", amount: "1350.00", recognizedOn: "2027-03-25" });
      }
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) {
        reads += 1;
        return json(200, reads === 1 ? contract : satisfied);
      }
      return undefined;
    };
    const { container } = renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    const obligations = await findTable({ name: "Performance obligations" });
    expect(obligations).toHaveTextContent("7200.00 USD");
    expect(obligations).toHaveTextContent("1350.00 USD");
    expect(obligations).toHaveTextContent("Awaiting milestone");
    expect(screen.getByRole("table", { name: "Schedule for line 3" })).toHaveTextContent("133.06");
    expect(screen.getByRole("table", { name: "Schedule for line 2" })).toHaveTextContent("On satisfaction");
    expect(pageViolations(container)).toEqual([]);

    fill("Satisfied on", "2027-03-25");
    fireEvent.click(screen.getByRole("button", { name: "Satisfy milestone (1350.00 USD)" }));
    expect(await findStatus()).toHaveTextContent("Line 2 satisfied on 2027-03-25: 1350.00 USD");
    const post = server.calls.find((c) => c.method === "POST");
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ satisfiedOn: "2027-03-25" });
    await waitFor(() => expect(screen.queryByRole("button", { name: /Satisfy milestone/ })).not.toBeInTheDocument());
    expect(screen.getByRole("table", { name: "Performance obligations" })).toHaveTextContent("Satisfied 2027-03-25");
  });

  it("explains a refused satisfaction and reloads a changed contract", async () => {
    server.handler = (url, init) => {
      if (isRollForward(url)) return json(200, contractMarch);
      if (init.method === "POST") return json(412, { code: "REV-0103", detail: "the contract changed since it was read; reload and review it" });
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) return json(200, contract);
      return undefined;
    };
    renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    fireEvent.click(await screen.findByRole("button", { name: "Satisfy milestone (1350.00 USD)" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The contract changed since you opened it");
    expect(alert).toHaveTextContent("(REV-0103)");
    await waitFor(() => expect(server.calls.filter((c) => c.method === "GET" && c.url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`).length).toBe(2));
  });

  it("shows a date error next to the field", async () => {
    server.handler = (url, init) => {
      if (isRollForward(url)) return json(200, contractMarch);
      if (init.method === "POST") return json(422, { code: "REV-0006", detail: "a milestone cannot be satisfied before the contract date 2027-03-10" });
      return json(200, contract);
    };
    renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    const satisfy = await screen.findByRole("button", { name: "Satisfy milestone (1350.00 USD)" });
    fill("Satisfied on", "");
    fireEvent.click(satisfy);
    expect(screen.getByText("Enter the date the milestone was met as YYYY-MM-DD.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Satisfied on", "2027-03-01");
    fireEvent.click(screen.getByRole("button", { name: "Satisfy milestone (1350.00 USD)" }));
    expect(await findAlert()).toHaveTextContent("The date is before the contract date");
  });

  it("warns about a residual SSP and shows a load failure with a retry", async () => {
    server.handler = (url) =>
      isRollForward(url) ? json(200, contractMarch) : json(200, { ...contract, warnings: [{ code: "BIL-0009", detail: "SSP missing for line 2; residual method used" }] });
    renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    expect(await screen.findByText("SSP missing for line 2; residual method used (BIL-0009)")).toBeInTheDocument();

    server.handler = () => json(404, { code: "REV-0100", detail: "contract not found" });
    renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    const alerts = await screen.findAllByRole("alert");
    expect(alerts.at(-1)).toHaveTextContent("contract not found (REV-0100)");
  });
});

describe("Revenue contract roll-forward", () => {
  it("opens on the contract's month, shows its basis and changes month", async () => {
    server.handler = (url) => {
      if (isRollForward(url)) {
        const period = url.searchParams.get("period") ?? "";
        return json(200, period === "2027-04" ? { ...contractMarch, period, opening: "2116.94", additions: "0.00", recognized: "187.50", closing: "1929.44" } : contractMarch);
      }
      return json(200, { ...contract, ratableBasis: "MONTHLY" });
    };
    const { container } = renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    const march = await screen.findByRole("list", { name: "Deferred revenue 2027-03" });
    expect(march).toHaveTextContent("3600.00 USD");
    expect(march).toHaveTextContent("2116.94 USD");
    const call = server.calls.find((c) => isRollForward(c.url));
    expect(Object.fromEntries(call!.url.searchParams)).toEqual({ contractId: CONTRACT, period: "2027-03" });
    expect(screen.getByText("Monthly (equal per month)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Month", "2027-4");
    fireEvent.click(screen.getByRole("button", { name: "Show month" }));
    expect(screen.getByText("Enter the month as YYYY-MM, for example 2027-03.")).toBeInTheDocument();
    fill("Month", "2027-04");
    fireEvent.click(screen.getByRole("button", { name: "Show month" }));
    expect(await screen.findByRole("list", { name: "Deferred revenue 2027-04" })).toHaveTextContent("1929.44 USD");
  });

  it("shows a roll-forward failure with a retry while the contract stays readable", async () => {
    let fail = true;
    server.handler = (url) => {
      if (isRollForward(url)) return fail ? json(503, { code: "HTTP-503", detail: "revrec unavailable" }) : json(200, contractMarch);
      return json(200, contract);
    };
    renderAt(`/billing/revenue-contracts/${CONTRACT}`);
    expect(await findAlert()).toHaveTextContent("revrec unavailable (HTTP-503)");
    expect(screen.getByRole("table", { name: "Performance obligations" })).toHaveTextContent("7200.00 USD");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Deferred revenue 2027-03" })).toHaveTextContent("1483.06 USD");
  });
});

describe("New revenue contract", () => {
  it("checks the form before calling the service", () => {
    const v = { ...emptyContract("2027-03-10"), transactionPrice: "10800" };
    v.obligations = [
      { productId: "x", ssp: "0", sspSource: "EXPLICIT", method: "RATABLE", termStart: "2027-03-10", termEnd: "2027-03-01", satisfiedOn: "" },
      // A residual SSP is derived by the service: an empty one is not an error.
      { productId: PRODUCT, ssp: "", sspSource: "RESIDUAL", method: "POINT_IN_TIME", termStart: "", termEnd: "", satisfiedOn: "" },
      { productId: PRODUCT, ssp: "", sspSource: "RESIDUAL", method: "POINT_IN_TIME", termStart: "", termEnd: "", satisfiedOn: "2027-03-10" },
    ];
    expect(Object.keys(validateContract(v)).sort()).toEqual([
      "customerId",
      "legalEntityId",
      "obligations",
      "obligations[0].productId",
      "obligations[0].ssp",
      "obligations[0].term",
      "obligations[1].satisfiedOn",
      "orderId",
    ]);
    const input = toContractInput({ ...v, obligations: [{ ...v.obligations[1], ssp: "1", satisfiedOn: "2027-03-10", termStart: "2027-01-01" }] });
    expect(input.obligations[0]).toEqual({ productId: PRODUCT, sspSource: "RESIDUAL", method: "POINT_IN_TIME", satisfiedOn: "2027-03-10" });
  });

  it("does not ask for a residual obligation's SSP and sends none", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/revrec/contracts" && init.method === "POST") return json(201, contract);
      if (isRollForward(url)) return json(200, contractMarch);
      return json(200, contract);
    };
    const { container } = renderAt("/billing/revenue-contracts/new");
    fill("Legal entity id", LE);
    fill("Sales order id", ORDER);
    fill("Customer id", CUST);
    fill("Contract price", "10800.00");
    fill("Product id", PRODUCT);
    fireEvent.change(screen.getByLabelText("SSP source"), { target: { value: "RESIDUAL" } });
    expect(screen.queryByLabelText("Standalone selling price")).not.toBeInTheDocument();
    expect(screen.getByText(/contract price less the other obligations' prices/)).toBeInTheDocument();
    fill("Transfer date", "2027-03-10");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    expect(await screen.findByRole("heading", { name: "Revenue contract" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ obligations: [{ productId: PRODUCT, sspSource: "RESIDUAL", method: "POINT_IN_TIME" }] });
    expect(post?.body).not.toHaveProperty("obligations.0.ssp");
  });

  it("creates a contract with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/revrec/contracts" && init.method === "POST") return json(201, contract);
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) return json(200, contract);
      return undefined;
    };
    const { container } = renderAt("/billing/revenue-contracts/new");
    fill("Legal entity id", LE);
    fill("Sales order id", ORDER);
    fill("Customer id", CUST);
    fill("Contract price", "10800.00");
    fill("Contract date", "2027-03-10");
    fill("Product id", PRODUCT);
    fill("Standalone selling price", "8000");
    fill("Transfer date", "2027-03-10");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    expect(await screen.findByRole("heading", { name: "Revenue contract" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toMatch(/^[0-9a-f-]{36}$/);
    expect(post?.body).toEqual({
      legalEntityId: LE,
      orderId: ORDER,
      customerId: CUST,
      currency: "USD",
      transactionPrice: "10800.00",
      residualRule: "LARGEST",
      ratableBasis: "DAILY",
      contractDate: "2027-03-10",
      obligations: [{ productId: PRODUCT, ssp: "8000", sspSource: "EXPLICIT", method: "POINT_IN_TIME", satisfiedOn: "2027-03-10" }],
    });
  });

  it("sends the monthly ratable basis when chosen", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/revrec/contracts" && init.method === "POST") return json(201, contract);
      if (isRollForward(url)) return json(200, contractMarch);
      return json(200, contract);
    };
    renderAt("/billing/revenue-contracts/new");
    fill("Legal entity id", LE);
    fill("Sales order id", ORDER);
    fill("Customer id", CUST);
    fill("Contract price", "2250");
    fill("Product id", PRODUCT);
    fill("Standalone selling price", "2500");
    fireEvent.change(screen.getByLabelText("Recognition"), { target: { value: "RATABLE" } });
    fill("Term start", "2027-03-10");
    fill("Term end", "2028-03-09");
    fireEvent.change(screen.getByLabelText("Ratable basis"), { target: { value: "MONTHLY" } });
    fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    expect(await screen.findByRole("heading", { name: "Revenue contract" })).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toMatchObject({ ratableBasis: "MONTHLY", obligations: [{ method: "RATABLE", termStart: "2027-03-10" }] });
  });

  it("shows service field errors next to the fields and a rule violation as an alert", async () => {
    let attempt = 0;
    server.handler = () => {
      attempt += 1;
      return attempt === 1
        ? json(400, { code: "COM-0001", detail: "request failed validation", errors: { "obligations[0].productId": "must be a UUID" } })
        : json(422, { code: "REV-0003", detail: "at most one obligation per contract may use the RESIDUAL SSP source" });
    };
    renderAt("/billing/revenue-contracts/new");
    fill("Legal entity id", LE);
    fill("Sales order id", ORDER);
    fill("Customer id", CUST);
    fill("Contract price", "100");
    fill("Product id", PRODUCT);
    fill("Standalone selling price", "1");
    fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    expect(await screen.findByText("must be a UUID")).toBeInTheDocument();
    const firstKey = idempotencyKey(server.calls[0]);
    fireEvent.click(screen.getByRole("button", { name: "Create contract" }));
    expect(await findAlert()).toHaveTextContent("(REV-0003)");
    expect(idempotencyKey(server.calls[1])).not.toBe(firstKey);
  });

  it("adds obligations with ratable terms and removes them", () => {
    renderAt("/billing/revenue-contracts/new");
    fireEvent.click(screen.getByRole("button", { name: "Add obligation" }));
    expect(screen.getByText("Obligation 2")).toBeInTheDocument();
    fireEvent.change(screen.getAllByLabelText("Recognition")[1], { target: { value: "RATABLE" } });
    expect(screen.getByLabelText("Term start")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove obligation 2" }));
    expect(screen.queryByText("Obligation 2")).not.toBeInTheDocument();
  });
});

describe("Deferred revenue", () => {
  const march: RollForward = {
    legalEntityId: LE,
    currency: "USD",
    period: "2027-03",
    start: "2027-03-01",
    end: "2027-03-31",
    opening: "0.00",
    additions: "3600.00",
    recognized: "1483.06",
    adjustments: "0.00",
    closing: "2116.94",
  };
  const run: RecognitionRun = { id: "run-1", legalEntityId: LE, periodEnd: "2027-03-31", lineCount: 1, createdAt: "2027-04-01T01:00:00Z" };

  it("knows month ends and checks the query", () => {
    expect(periodEndOf("2027-02")).toBe("2027-02-28");
    expect(periodEndOf("2028-02")).toBe("2028-02-29");
    expect(periodEndOf("2027-12")).toBe("2027-12-31");
    expect(lastMonth(new Date(2027, 0, 15))).toBe("2026-12");
    expect(Object.keys(validateQuery({ legalEntityId: "x", currency: "usd", period: "2027-13" })).sort()).toEqual(["currency", "legalEntityId", "period"]);
  });

  it("starts empty, then shows the AC-29.4 roll-forward and runs recognition", async () => {
    let runs: RecognitionRun[] = [];
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/revrec/roll-forward") return json(200, march);
      if (url.pathname === "/api/v1/revrec/recognition-runs" && init.method === "POST") {
        runs = [run];
        return json(201, run);
      }
      if (url.pathname === "/api/v1/revrec/recognition-runs") return json(200, { items: runs });
      return undefined;
    };
    const { container } = renderAt("/billing/deferred-revenue");
    expect(screen.getByText("Choose an entity and month")).toBeInTheDocument();
    fill("Legal entity id", LE);
    fill("Month", "2027-03");
    fireEvent.click(screen.getByRole("button", { name: "Show roll-forward" }));
    const tiles = await screen.findByRole("list", { name: "Deferred revenue 2027-03" });
    expect(tiles).toHaveTextContent("3600.00 USD");
    expect(tiles).toHaveTextContent("1483.06 USD");
    expect(tiles).toHaveTextContent("2116.94 USD");
    const rf = server.calls.find((c) => c.url.pathname === "/api/v1/revrec/roll-forward");
    expect(Object.fromEntries(rf!.url.searchParams)).toEqual({ legalEntityId: LE, currency: "USD", period: "2027-03" });
    expect(await screen.findByText("No recognition runs yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Run recognition through 2027-03-31" }));
    expect(await findStatus()).toHaveTextContent("Recognition run through 2027-03-31: 1 ratable line recognized.");
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ legalEntityId: LE, periodEnd: "2027-03-31" });
    await waitFor(() => expect(screen.getByRole("table", { name: "Recognition runs" })).toHaveTextContent("2027-04-01 01:00:00 UTC"));
  });

  it("explains a run for a month that has not ended and a roll-forward failure", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "REV-0007", detail: "the period ending 2099-01-31 has not ended" });
      if (url.pathname === "/api/v1/revrec/roll-forward") return json(403, { code: "AUTH-0003", detail: "the caller is scoped to another legal entity" });
      return json(200, { items: [] });
    };
    renderAt("/billing/deferred-revenue");
    fill("Legal entity id", LE);
    fill("Month", "2099-01");
    fireEvent.click(screen.getByRole("button", { name: "Show roll-forward" }));
    expect(await findAlert()).toHaveTextContent("the caller is scoped to another legal entity (AUTH-0003)");
    fireEvent.click(screen.getByRole("button", { name: "Run recognition through 2099-01-31" }));
    expect(await screen.findByText("That month has not ended yet")).toBeInTheDocument();
  });

  it("checks the query before calling the service", () => {
    renderAt("/billing/deferred-revenue");
    fill("Month", "March");
    fireEvent.click(screen.getByRole("button", { name: "Show roll-forward" }));
    expect(screen.getByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the month as YYYY-MM, for example 2027-03.")).toBeInTheDocument();
    expect(server.calls).toEqual([]);
  });
});
