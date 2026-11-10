import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CommissionRule, CommissionTransaction, SalesGroup } from "./api/client";
import { describeReps, ruleLevel } from "./commissionFormat";
import { emptySalesGroup, toSalesGroupInput, validateSalesGroup } from "./NewSalesGroupPage";
import { validateRule } from "./SalesGroupDetailPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const SG = "0190f0a0-0000-7000-8000-000000000701";

const group: SalesGroup = {
  id: SG,
  legalEntityId: LE,
  name: "SG-East",
  reps: [
    { repId: "R1", sharePct: "60" },
    { repId: "R2", sharePct: "40" },
  ],
};

const defaultRule: CommissionRule = { id: "rule-1", legalEntityId: LE, salesGroupId: SG, customerGroup: "", itemGroup: "", basis: "REVENUE", ratePct: "5" };

function txn(patch: Partial<CommissionTransaction>): CommissionTransaction {
  return {
    id: "t-1",
    legalEntityId: LE,
    invoiceLineId: "INV-1/1",
    invoiceDate: "2027-03-10",
    periodId: "2027-03",
    customerId: "C1",
    customerGroup: "",
    itemGroup: "",
    salesGroupId: SG,
    repId: "R1",
    sharePct: "60",
    basis: "REVENUE",
    lineNet: "10000",
    basisAmount: "10000",
    ratePct: "5",
    amount: "300",
    currency: "USD",
    debitAccount: "6200",
    creditAccount: "2170",
    status: "POSTED",
    ...patch,
  };
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Sales groups", () => {
  it("lists sales groups with their reps and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/commission/sales-groups")) return json(200, { items: [group] });
      if (url.pathname.endsWith(`/commission/sales-groups/${SG}`)) return json(200, group);
      if (url.pathname.endsWith("/commission/rules")) return json(200, { items: [defaultRule] });
      return undefined;
    };
    const { container } = renderAt("/pricing/commission/sales-groups");
    const table = await findTable({ name: "Sales groups" });
    await waitFor(() => expect(table).toHaveTextContent("SG-East"));
    expect(table).toHaveTextContent("R1 60% · R2 40%");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "SG-East" }));
    expect(await screen.findByRole("heading", { name: "SG-East" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/commission/sales-groups");
    expect(await screen.findByText("No sales groups")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.commission.view" });
    renderAt("/pricing/commission/sales-groups");
    expect(await findAlert()).toHaveTextContent("missing duty pricing.commission.view (AUTH-0003)");
  });
});

describe("New sales group", () => {
  it("checks the reps before calling the service", async () => {
    const { container } = renderAt("/pricing/commission/sales-groups/new");
    fireEvent.click(screen.getByRole("button", { name: "Save sales group" }));
    expect(await screen.findByText("Enter the rep's worker id (up to 64 characters).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a 60/40 group with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/commission/sales-groups")) return json(201, group);
      if (url.pathname.endsWith(`/commission/sales-groups/${SG}`)) return json(200, group);
      if (url.pathname.endsWith("/commission/rules")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/pricing/commission/sales-groups/new");
    fill("Legal entity", LE);
    fill("Sales group name", "SG-East");
    fill("Rep id", "R1");
    fill("Share %", "60");
    fireEvent.click(screen.getByRole("button", { name: "Add rep" }));
    fireEvent.change(screen.getAllByLabelText("Rep id")[1], { target: { value: "R2" } });
    fireEvent.change(screen.getAllByLabelText("Share %")[1], { target: { value: "40" } });
    expect(screen.getByText("Shares add up to 100%.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save sales group" }));
    expect(await screen.findByRole("heading", { name: "SG-East" })).toBeInTheDocument();
    expect(await screen.findByText("No rules yet")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, name: "SG-East", reps: group.reps });
  });

  it("refuses shares that do not add up to 100", async () => {
    renderAt("/pricing/commission/sales-groups/new");
    fill("Legal entity", LE);
    fill("Sales group name", "x");
    fill("Rep id", "R1");
    fill("Share %", "60");
    fireEvent.click(screen.getByRole("button", { name: "Save sales group" }));
    expect(await screen.findByText("The shares add up to 60%; they must add up to 100%.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });
});

describe("Sales group detail", () => {
  it("shows reps and rules in search order and adds a rule", async () => {
    let rules: CommissionRule[] = [defaultRule];
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/commission/sales-groups/${SG}`)) return json(200, group);
      if (init.method === "POST" && url.pathname.endsWith("/commission/rules")) {
        const created = { ...defaultRule, id: "rule-2", customerGroup: "KEY", basis: "MARGIN" as const, ratePct: "7" };
        rules = [created, ...rules];
        return json(201, created);
      }
      if (url.pathname.endsWith("/commission/rules")) return json(200, { items: rules });
      return undefined;
    };
    const { container } = renderAt(`/pricing/commission/sales-groups/${SG}`);
    expect(await screen.findByRole("heading", { name: "SG-East" })).toBeInTheDocument();
    const reps = screen.getByRole("table", { name: "Reps and shares" });
    expect(within(reps).getAllByRole("row").map((r) => r.textContent)).toEqual(["RepShare", "R160%", "R240%"]);
    const ruleTable = await findTable({ name: "Commission rules" });
    await waitFor(() => expect(ruleTable).toHaveTextContent("All customers"));
    expect(server.calls.find((c) => c.url.pathname.endsWith("/commission/rules"))?.url.searchParams.get("salesGroupId")).toBe(SG);
    expect(pageViolations(container)).toEqual([]);

    fill("Customer commission group", "KEY");
    fireEvent.change(screen.getByLabelText("Basis"), { target: { value: "MARGIN" } });
    fill("Rate %", "7");
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(await screen.findByText("Rule added: 7% for KEY and all items.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ salesGroupId: SG, customerGroup: "KEY", itemGroup: "", basis: "MARGIN", ratePct: "7" });
    expect(idempotencyKey(post!)).toBeTruthy();
    await waitFor(() => expect(screen.getByRole("table", { name: "Commission rules" })).toHaveTextContent("KEY"));
  });

  it("shows a duplicate rule conflict", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/commission/sales-groups/${SG}`)) return json(200, group);
      if (init.method === "POST") return json(409, { code: "PRC-0038", detail: "sales group already has a rule for customer group '' and item group ''" });
      if (url.pathname.endsWith("/commission/rules")) return json(200, { items: [defaultRule] });
      return undefined;
    };
    renderAt(`/pricing/commission/sales-groups/${SG}`);
    await screen.findByRole("heading", { name: "SG-East" });
    fill("Rate %", "3");
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(await findAlert()).toHaveTextContent("(PRC-0038)");
  });

  it("shows a load error with a retry", async () => {
    server.handler = () => json(404, { code: "PRC-0036", detail: `no sales group ${SG}` });
    renderAt(`/pricing/commission/sales-groups/${SG}`);
    expect((await screen.findAllByRole("alert"))[0]).toHaveTextContent("PRC-0036");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Commission transactions", () => {
  it("lists the transactions with an exact total and filters by rep and month", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/commission/sales-groups")) return json(200, { items: [group] });
      if (url.pathname.endsWith("/commission/transactions")) {
        if (url.searchParams.get("repId") === "R9") return json(200, { items: [] });
        return json(200, {
          items: [
            txn({}),
            txn({ id: "t-2", repId: "R2", sharePct: "40", amount: "200" }),
            txn({ id: "t-3", invoiceLineId: "CN-1/1", reversesInvoiceLineId: "INV-1/1", lineNet: "-1000", basisAmount: "-1000", amount: "-30" }),
          ],
        });
      }
      return undefined;
    };
    const { container } = renderAt("/pricing/commission/transactions");
    const table = await findTable({ name: "Commission transactions" });
    await waitFor(() => expect(table).toHaveTextContent("SG-East"));
    expect(table).toHaveTextContent("CN-1/1 (credits INV-1/1)");
    expect(table).toHaveTextContent("Dr 6200 / Cr 2170");
    const totals = screen.getByRole("list", { name: "Commission totals" });
    expect(totals).toHaveTextContent("Commission 470 USD");
    expect(totals).toHaveTextContent("Invoice lines 2");
    expect(pageViolations(container)).toEqual([]);

    fill("Month", "2027-13");
    fireEvent.click(screen.getByRole("button", { name: "Show transactions" }));
    expect(await screen.findByText("Use a month such as 2027-03.")).toBeInTheDocument();
    fill("Month", "2027-03");
    fill("Rep", "R9");
    fireEvent.click(screen.getByRole("button", { name: "Show transactions" }));
    expect(await screen.findByText("No commission transactions")).toBeInTheDocument();
    const last = server.calls.filter((c) => c.url.pathname.endsWith("/commission/transactions")).at(-1);
    expect(last?.url.searchParams.get("repId")).toBe("R9");
    expect(last?.url.searchParams.get("period")).toBe("2027-03");
  });

  it("takes the sales group from the address and shows an error with a retry", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/commission/sales-groups")) return json(200, { items: [group] });
      return json(503, { code: "COM-0500", detail: "database unavailable" });
    };
    renderAt(`/pricing/commission/transactions?salesGroupId=${SG}`);
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
    expect(server.calls.find((c) => c.url.pathname.endsWith("/commission/transactions"))?.url.searchParams.get("salesGroupId")).toBe(SG);
  });
});

describe("commission form rules", () => {
  it("mirrors the service rules for shares and rules", () => {
    const ok = { ...emptySalesGroup, legalEntityId: LE, name: "SG", reps: [{ repId: "R1", sharePct: "33.333333" }, { repId: "R2", sharePct: "66.666667" }] };
    expect(validateSalesGroup(ok)).toEqual({});
    expect(validateSalesGroup({ ...ok, reps: [{ repId: "R1", sharePct: "50" }, { repId: "R1", sharePct: "50" }] })["reps[1].repId"]).toBe("Each rep appears once.");
    expect(validateSalesGroup({ ...ok, reps: [{ repId: "R1", sharePct: "0" }] })["reps[0].sharePct"]).toBeDefined();
    expect(validateSalesGroup({ ...ok, reps: [{ repId: "EU/R1", sharePct: "100" }] })["reps[0].repId"]).toMatch(/no \/ or :/);
    expect(toSalesGroupInput({ ...ok, name: " SG " }).name).toBe("SG");
    expect(validateRule({ customerGroup: "", itemGroup: "", basis: "REVENUE", ratePct: "101" }).ratePct).toBeDefined();
    expect(validateRule({ customerGroup: "", itemGroup: "", basis: "REVENUE", ratePct: "0" })).toEqual({});
  });

  it("names the search level and the reps", () => {
    expect(ruleLevel({ customerGroup: "A", itemGroup: "B" })).toBe(1);
    expect(ruleLevel({ customerGroup: "A", itemGroup: "" })).toBe(2);
    expect(ruleLevel({ customerGroup: "", itemGroup: "B" })).toBe(3);
    expect(ruleLevel({ customerGroup: "", itemGroup: "" })).toBe(4);
    expect(describeReps(group.reps)).toBe("R1 60% · R2 40%");
  });
});
