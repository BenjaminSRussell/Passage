import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BrokerContract, ContractAccrual, ContractClaim, ContractClaimPreview, RoyaltyAgreement } from "./api/client";
import { describeBrokerRate, describeBrokerScope, describeRoyaltyTiers } from "./contractFormat";
import { contractClaimTotals, validateContractClaimFilter } from "./ContractClaimsPage";
import { emptyBroker, toBrokerInput, validateBroker } from "./NewBrokerPage";
import { emptyRoyalty, toRoyaltyInput, validateRoyalty } from "./NewRoyaltyPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const BK = "0190f0a0-0000-7000-8000-000000000b01";
const RY = "0190f0a0-0000-7000-8000-000000000c01";
const V1 = "0190f0a0-0000-7000-8000-000000000801";
const CL = "0190f0a0-0000-7000-8000-000000000d01";

const broker: BrokerContract = {
  id: BK,
  legalEntityId: LE,
  name: "Agent Smith",
  brokerVendorId: V1,
  productGroupId: "BRANDED",
  rateKind: "PERCENT_NET",
  rate: "2.5",
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  payableAccount: "2175",
  expenseAccount: "6210",
  status: "ACTIVE",
  version: 1,
};

const royalty: RoyaltyAgreement = {
  id: RY,
  legalEntityId: LE,
  name: "Brand licence",
  licensorVendorId: V1,
  productGroupId: "BRANDED",
  basis: "NET_SALES",
  period: "MONTH",
  tiers: [
    { from: "0", ratePct: "4", amountPerUnit: "0" },
    { from: "50000", ratePct: "5", amountPerUnit: "0" },
  ],
  minimumRoyalty: "5000",
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  payableAccount: "2176",
  expenseAccount: "6220",
  status: "ACTIVE",
  version: 1,
};

function claim(patch: Partial<ContractClaim> = {}): ContractClaim {
  return {
    id: CL,
    number: "BC-000001",
    legalEntityId: LE,
    contractKind: "BROKER",
    contractId: BK,
    vendorId: V1,
    periodId: "2027-03",
    accruedTotal: "150",
    priorClaimed: "0",
    amount: "150",
    currency: "USD",
    status: "OPEN",
    version: 1,
    createdBy: "u1",
    createdAt: "2027-04-01T08:00:00Z",
    ...patch,
  };
}

function preview(patch: Partial<ContractClaimPreview> = {}): ContractClaimPreview {
  return { contractKind: "BROKER", contractId: BK, vendorId: V1, periodId: "2027-03", accrued: "150", shortfall: "0", priorClaimed: "0", amount: "150", currency: "USD", claims: 0, closed: false, ...patch };
}

const accruals: ContractAccrual[] = [
  { id: "a1", contractKind: "BROKER", contractId: BK, periodId: "2027-03", lineKey: "INV-1/1", kind: "ACCRUAL", invoiceDate: "2027-03-10", basisValue: "10000", basisQuantity: "5", amount: "250", currency: "USD" },
  { id: "a2", contractKind: "BROKER", contractId: BK, periodId: "2027-03", lineKey: "CN-1/1", kind: "CREDIT", invoiceDate: "2027-03-20", basisValue: "-4000", basisQuantity: "-2", amount: "-100", currency: "USD", reversesLineKey: "INV-1/1" },
];

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("broker and royalty formats", () => {
  it("describes the rate, scope and tiers in words", () => {
    expect(describeBrokerRate(broker)).toBe("2.5% of net");
    expect(describeBrokerRate({ ...broker, rateKind: "PER_UNIT", rate: "0.35" })).toBe("0.35 USD per unit");
    expect(describeBrokerScope(broker)).toBe("All customers, product group BRANDED");
    expect(describeBrokerScope({ ...broker, customerId: V1, productGroupId: undefined, productId: "P1" })).toBe(`Customer ${V1}, product P1`);
    expect(describeRoyaltyTiers(royalty)).toBe("from 0: 4% · from 50000: 5%");
    expect(describeRoyaltyTiers({ ...royalty, basis: "QUANTITY", tiers: [{ from: "0", amountPerUnit: "0.5" }] })).toBe("from 0: 0.5 USD per unit");
  });

  it("sums claim totals per currency without floating point", () => {
    expect(contractClaimTotals([{ amount: "0.1", currency: "USD" }, { amount: "0.2", currency: "USD" }, { amount: "-5", currency: "EUR" }])).toBe("0.3 USD · -5 EUR");
    expect(validateContractClaimFilter({ kind: "", vendorId: "x", period: "March", status: "" })).toEqual({
      vendorId: "Enter the vendor id (a UUID).",
      period: "Use a period such as 2027-03, 2027-Q1 or 2027.",
    });
  });

  it("validates the broker and royalty drafts like the service", () => {
    expect(Object.keys(validateBroker(emptyBroker)).sort()).toEqual(["brokerVendorId", "legalEntityId", "name", "rate", "validFrom", "validTo"]);
    const ok = { ...emptyBroker, legalEntityId: LE, name: "A", brokerVendorId: V1, rate: "2.5", validFrom: "2027-01-01", validTo: "2027-12-31", customerScope: "group" as const, customerKey: V1, productScope: "product" as const, productKey: "P1" };
    expect(validateBroker(ok)).toEqual({});
    expect(toBrokerInput(ok)).toMatchObject({ customerGroupId: V1, productId: "P1", rateKind: "PERCENT_NET", payableAccount: "2175", expenseAccount: "6210" });
    expect(validateBroker({ ...ok, rate: "101" }).rate).toBe("Enter a percentage above 0 and up to 100.");
    expect(validateBroker({ ...ok, rateKind: "PER_UNIT", rate: "101" })).toEqual({});

    const base = { ...emptyRoyalty, legalEntityId: LE, name: "R", licensorVendorId: V1, productKey: "BRANDED", validFrom: "2027-01-01", validTo: "2027-12-31", tiers: [{ from: "0", value: "4" }, { from: "5", value: "5" }] };
    expect(validateRoyalty(base)).toEqual({});
    expect(toRoyaltyInput(base)).toMatchObject({ productGroupId: "BRANDED", minimumRoyalty: "0", tiers: [{ from: "0", ratePct: "4" }, { from: "5", ratePct: "5" }], expenseAccount: "6220" });
    expect(validateRoyalty({ ...base, tiers: [{ from: "5", value: "4" }, { from: "5", value: "5" }] })["tiers[1].from"]).toBe("Thresholds must increase from tier to tier.");
    expect(toRoyaltyInput({ ...base, basis: "QUANTITY", tiers: [{ from: "0", value: "0.5" }] }).tiers).toEqual([{ from: "0", amountPerUnit: "0.5" }]);
  });
});

describe("Broker contracts", () => {
  it("lists contracts, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/broker-contracts")) return json(200, { items: url.searchParams.get("status") === "CLOSED" ? [] : [broker] });
      if (url.pathname.endsWith(`/broker-contracts/${BK}`)) return json(200, broker);
      if (url.pathname.endsWith("/accruals") || url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/pricing/brokers");
    const table = await findTable({ name: "Broker contracts" });
    await waitFor(() => expect(table).toHaveTextContent("Agent Smith"));
    expect(table).toHaveTextContent("2.5% of net");
    expect(table).toHaveTextContent("All customers, product group BRANDED");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "CLOSED" } });
    expect(await screen.findByText("No broker contracts")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.search).toBe("?status=CLOSED");
  });

  it("shows the loading failure and retries", async () => {
    let fail = true;
    server.handler = (url) => {
      if (!url.pathname.endsWith("/broker-contracts")) return undefined;
      return fail ? json(500, { code: "COM-0001", detail: "database down" }) : json(200, { items: [broker] });
    };
    renderAt("/pricing/brokers");
    expect(await findAlert()).toHaveTextContent("database down (COM-0001)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Agent Smith")).toBeInTheDocument();
  });

  it("creates a contract with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/broker-contracts")) return json(201, broker);
      if (url.pathname.endsWith(`/broker-contracts/${BK}`)) return json(200, broker);
      if (url.pathname.endsWith("/accruals") || url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/pricing/brokers/new");
    fireEvent.click(screen.getByRole("button", { name: "Save contract" }));
    expect(await screen.findByText("Enter the broker's vendor id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a percentage above 0 and up to 100.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity id", LE);
    fill("Contract name", "Agent Smith");
    fill("Broker vendor id", V1);
    fireEvent.change(screen.getByLabelText("Products"), { target: { value: "group" } });
    fill("Product group", "BRANDED");
    fill("Rate %", "2.5");
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save contract" }));
    expect(await screen.findByRole("heading", { name: "Agent Smith" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ brokerVendorId: V1, productGroupId: "BRANDED", rateKind: "PERCENT_NET", rate: "2.5", payableAccount: "2175", expenseAccount: "6210" });
    expect(post && idempotencyKey(post)).toBeTruthy();
  });

  it("maps a service refusal onto the form field", async () => {
    server.handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/broker-contracts") ? json(422, { code: "COM-0001", detail: "request is invalid", errors: { customer: "name at most one of customerId and customerGroupId" } }) : undefined;
    renderAt("/pricing/brokers/new");
    fill("Legal entity id", LE);
    fill("Contract name", "Agent Smith");
    fill("Broker vendor id", V1);
    fill("Rate %", "2.5");
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save contract" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST")).toBe(true));
    expect(screen.queryByText("Could not save the contract")).not.toBeInTheDocument();
  });

  it("shows accruals, previews and creates a claim, and closes the contract", async () => {
    let closed = false;
    let claimed = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/broker-contracts/${BK}:close`)) {
        closed = true;
        return json(200, { ...broker, status: "CLOSED", version: 2 });
      }
      if (url.pathname.endsWith(`/broker-contracts/${BK}`)) return json(200, broker);
      if (url.pathname.endsWith(`/broker-contracts/${BK}/accruals`)) return json(200, { items: accruals });
      if (url.pathname.endsWith("/claim-preview")) return json(200, claimed ? preview({ amount: "0", priorClaimed: "150", claims: 1 }) : preview());
      if (init.method === "POST" && url.pathname.endsWith("/contract-claims")) {
        claimed = true;
        return json(201, claim());
      }
      if (url.pathname.endsWith("/contract-claims")) return json(200, { items: claimed ? [claim()] : [] });
      return undefined;
    };
    renderAt(`/pricing/brokers/${BK}`);
    const table = await findTable({ name: "Accruals of this contract" });
    await waitFor(() => expect(table).toHaveTextContent("INV-1/1"));
    expect(table).toHaveTextContent("Credit note line");
    expect(table).toHaveTextContent("-100 USD");
    expect(screen.getByText("Dr 6210 / Cr 2175")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close period" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    expect(await screen.findByText("Use a period such as 2027-03, 2027-Q1 or 2027.")).toBeInTheDocument();
    fill("Period", "2027-03");
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create claim for 150 USD" }));
    expect(await screen.findByText("Claim BC-000001 created")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/contract-claims"));
    expect(post?.body).toEqual({ contractKind: "BROKER", contractId: BK, period: "2027-03" });
    expect(post && idempotencyKey(post)).toBeTruthy();
    expect(await screen.findByText("The period is fully claimed.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close contract" }));
    expect(await screen.findByText("The contract is closed. It takes no further accruals; earlier periods can still be claimed.")).toBeInTheDocument();
    expect(closed).toBe(true);
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close"))?.body).toEqual({ version: 1 });
    expect(screen.queryByRole("button", { name: "Close contract" })).not.toBeInTheDocument();
  });

  it("has no accessibility violations on the detail page", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(`/broker-contracts/${BK}`)) return json(200, broker);
      if (url.pathname.endsWith("/accruals")) return json(200, { items: accruals });
      if (url.pathname.endsWith("/contract-claims")) return json(200, { items: [claim()] });
      return undefined;
    };
    const { container } = renderAt(`/pricing/brokers/${BK}`);
    await findTable({ name: "Claims of this contract" });
    await waitFor(() => expect(container).toHaveTextContent("BC-000001"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports a refused claim", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/broker-contracts/${BK}`)) return json(200, broker);
      if (url.pathname.endsWith("/accruals")) return json(200, { items: [] });
      if (url.pathname.endsWith("/claim-preview")) return json(200, preview());
      if (init.method === "POST") return json(409, { code: "PRC-0015", detail: "the accruals of 2027-03 are fully claimed" });
      if (url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/brokers/${BK}`);
    await screen.findByRole("heading", { name: "Agent Smith" });
    fill("Period", "2027-03");
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create claim for 150 USD" }));
    expect(await findAlert()).toHaveTextContent("the accruals of 2027-03 are fully claimed (PRC-0015)");
  });

  it("shows an error when the contract does not exist", async () => {
    server.handler = () => json(404, { code: "PRC-0058", detail: "no broker contract" });
    renderAt(`/pricing/brokers/${BK}`);
    expect(await findAlert()).toHaveTextContent("no broker contract (PRC-0058)");
  });
});

describe("Royalty agreements", () => {
  it("lists agreements with tiers and the minimum", async () => {
    server.handler = (url) => (url.pathname.endsWith("/royalty-agreements") ? json(200, { items: [royalty] }) : undefined);
    const { container } = renderAt("/pricing/royalties");
    const table = await findTable({ name: "Royalty agreements" });
    await waitFor(() => expect(table).toHaveTextContent("Brand licence"));
    expect(table).toHaveTextContent("from 0: 4% · from 50000: 5%");
    expect(table).toHaveTextContent("5000 USD");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    server.handler = (url) => (url.pathname.endsWith("/royalty-agreements") ? json(200, { items: [] }) : undefined);
    renderAt("/pricing/royalties");
    expect(await screen.findByText("No royalty agreements")).toBeInTheDocument();
  });

  it("creates an agreement with tiers and a minimum royalty", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/royalty-agreements")) return json(201, royalty);
      if (url.pathname.endsWith(`/royalty-agreements/${RY}`)) return json(200, royalty);
      if (url.pathname.endsWith("/accruals") || url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/pricing/royalties/new");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByText("Enter the licensor's vendor id (a UUID).")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity id", LE);
    fill("Agreement name", "Brand licence");
    fill("Licensor vendor id", V1);
    fill("Product group", "BRANDED");
    fill("Minimum royalty per period", "5000");
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fill("Rate %", "4");
    fireEvent.click(screen.getByRole("button", { name: "Add tier" }));
    fireEvent.change(screen.getAllByLabelText("From")[1], { target: { value: "50000" } });
    fireEvent.change(screen.getAllByLabelText("Rate %")[1], { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("heading", { name: "Brand licence" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      licensorVendorId: V1,
      productGroupId: "BRANDED",
      basis: "NET_SALES",
      minimumRoyalty: "5000",
      tiers: [{ from: "0", ratePct: "4" }, { from: "50000", ratePct: "5" }],
    });
    expect(post && idempotencyKey(post)).toBeTruthy();
  });

  it("closes a period, shows the shortfall notice, then claims it", async () => {
    let closed = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/royalty-agreements/${RY}:close-period`)) {
        closed = true;
        return json(200, {
          agreementId: RY,
          periodId: "2027-03",
          accrued: "3000",
          minimumRoyalty: "5000",
          shortfall: "2000",
          currency: "USD",
          alreadyClosed: false,
          notice: "PRC-0017: minimum royalty shortfall 2000 USD posted at period close",
        });
      }
      if (url.pathname.endsWith(`/royalty-agreements/${RY}`)) return json(200, royalty);
      if (url.pathname.endsWith("/accruals")) return json(200, { items: [] });
      if (url.pathname.endsWith("/claim-preview")) {
        return json(200, closed ? preview({ contractKind: "ROYALTY", contractId: RY, accrued: "3000", shortfall: "2000", amount: "5000", closed: true }) : preview({ contractKind: "ROYALTY", contractId: RY, accrued: "3000", amount: "3000" }));
      }
      if (init.method === "POST" && url.pathname.endsWith("/contract-claims")) return json(201, claim({ number: "RY-000001", contractKind: "ROYALTY", contractId: RY, amount: "5000" }));
      if (url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/royalties/${RY}`);
    await screen.findByRole("heading", { name: "Brand licence" });
    expect(screen.getByText("5000 USD")).toBeInTheDocument();
    fill("Period", "2027-03");
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    expect(await screen.findByText("Close this period before claiming it, so the minimum royalty is settled.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Create claim/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close period" }));
    expect(await screen.findByText("PRC-0017: minimum royalty shortfall 2000 USD posted at period close")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close-period"))?.body).toEqual({ period: "2027-03" });
    fireEvent.click(await screen.findByRole("button", { name: "Create claim for 5000 USD" }));
    expect(await screen.findByText("Claim RY-000001 created")).toBeInTheDocument();
  });

  it("reports a refused period close", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "COM-0001", detail: "period 2027-Q1 is not a MONTH period inside the agreement's validity" });
      if (url.pathname.endsWith(`/royalty-agreements/${RY}`)) return json(200, royalty);
      if (url.pathname.endsWith("/accruals") || url.pathname.endsWith("/contract-claims")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/royalties/${RY}`);
    await screen.findByRole("heading", { name: "Brand licence" });
    fill("Period", "2027-Q1");
    fireEvent.click(screen.getByRole("button", { name: "Close period" }));
    expect(await findAlert()).toHaveTextContent("is not a MONTH period inside the agreement's validity (COM-0001)");
  });
});

describe("Broker and royalty claims inquiry", () => {
  it("lists claims, filters them and shows the totals", async () => {
    const rows = [claim(), claim({ id: "c2", number: "RY-000001", contractKind: "ROYALTY", amount: "5000", status: "APPROVED" })];
    server.handler = (url) => {
      if (url.pathname.endsWith("/contract-claims")) return json(200, { items: url.searchParams.get("contractKind") === "ROYALTY" ? [rows[1]] : rows });
      return undefined;
    };
    const { container } = renderAt("/pricing/contract-claims");
    const table = await findTable({ name: "Broker and royalty claims" });
    await waitFor(() => expect(table).toHaveTextContent("RY-000001"));
    expect(screen.getByText("5150 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "ROYALTY" } });
    fireEvent.click(screen.getByRole("button", { name: "Show claims" }));
    await waitFor(() => expect(table).not.toHaveTextContent("BC-000001"));
    expect(server.calls.at(-1)?.url.search).toContain("contractKind=ROYALTY");

    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "March" } });
    fireEvent.click(screen.getByRole("button", { name: "Show claims" }));
    expect(await screen.findByText("Use a period such as 2027-03, 2027-Q1 or 2027.")).toBeInTheDocument();
  });

  it("shows the empty state and a loading failure", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/pricing/contract-claims");
    expect(await screen.findByText("No broker or royalty claims")).toBeInTheDocument();
  });

  it("shows the loading failure", async () => {
    server.handler = () => json(500, { code: "COM-0001", detail: "database down" });
    renderAt("/pricing/contract-claims");
    expect(await findAlert()).toHaveTextContent("database down (COM-0001)");
  });
});

describe("Broker and royalty claim approval", () => {
  it("approves an open claim at its version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/contract-claims/${CL}:approve`)) return json(200, claim({ status: "APPROVED", version: 2, approvedBy: "u2", approvedAt: "2027-04-02T08:00:00Z" }));
      if (url.pathname.endsWith(`/contract-claims/${CL}`)) return json(200, claim());
      return undefined;
    };
    const { container } = renderAt(`/pricing/contract-claims/${CL}`);
    await screen.findByRole("heading", { name: "Claim BC-000001" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Approve claim" }));
    expect(await screen.findByText("The claim is approved. Accounts payable settles it with the vendor.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ version: 1 });
    expect(post && idempotencyKey(post)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Approve claim" })).not.toBeInTheDocument();
  });

  it("shows why the creator cannot approve", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(403, { code: "PRC-0046", detail: "the user who created claim BC-000001 cannot approve it" });
      if (url.pathname.endsWith(`/contract-claims/${CL}`)) return json(200, claim());
      return undefined;
    };
    renderAt(`/pricing/contract-claims/${CL}`);
    await screen.findByRole("heading", { name: "Claim BC-000001" });
    fireEvent.click(screen.getByRole("button", { name: "Approve claim" }));
    expect(await findAlert()).toHaveTextContent("cannot approve it (PRC-0046)");
  });

  it("offers no approval for an approved claim and explains an over-accrual", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/contract-claims/${CL}`) ? json(200, claim({ status: "APPROVED", version: 2, approvedAt: "2027-04-02T08:00:00Z" })) : undefined);
    renderAt(`/pricing/contract-claims/${CL}`);
    await screen.findByRole("heading", { name: "Claim BC-000001" });
    expect(screen.queryByRole("button", { name: "Approve claim" })).not.toBeInTheDocument();
  });

  it("shows an error when the claim does not exist", async () => {
    server.handler = () => json(404, { code: "PRC-0044", detail: "no broker or royalty claim" });
    renderAt(`/pricing/contract-claims/${CL}`);
    expect(await findAlert()).toHaveTextContent("no broker or royalty claim (PRC-0044)");
  });
});
