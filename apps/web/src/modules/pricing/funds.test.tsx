import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Fund, FundUsage } from "./api/client";
import { validateAttribution } from "./FundDetailPage";
import { emptyFund, toFundInput, validateFund } from "./NewFundPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const FUND = "0190f0a0-0000-7000-8000-000000000901";

const fund: Fund = {
  id: FUND,
  legalEntityId: LE,
  name: "Spring TPM",
  type: "TRADE",
  currency: "USD",
  customerScope: "",
  owner: "tm-lead",
  budgetPerPeriod: "1000",
  allowOverspend: false,
  closed: false,
  version: 1,
};

const usage: FundUsage = {
  fund,
  items: [
    { period: "2027-03", source: "CLAIM", amount: "600" },
    { period: "2027-03", source: "DEDUCTION", amount: "25" },
  ],
  periods: [{ fundId: FUND, period: "2027-03", budget: "1000", used: "625", remaining: "375", closed: false }],
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Trade funds", () => {
  it("lists funds with budget and status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/funds")) return json(200, { items: [fund, { ...fund, id: "f-2", name: "Coop", closed: true, allowOverspend: true }] });
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    const { container } = renderAt("/pricing/funds");
    const table = await findTable({ name: "Trade funds" });
    await waitFor(() => expect(table).toHaveTextContent("Spring TPM"));
    expect(table).toHaveTextContent("1000 USD");
    expect(table).toHaveTextContent("Blocked");
    expect(table).toHaveTextContent("Closed");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Spring TPM" }));
    expect(await screen.findByRole("heading", { name: "Spring TPM" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/funds");
    expect(await screen.findByText("No trade funds")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view or pricing.fund.maintain" });
    renderAt("/pricing/funds");
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New trade fund", () => {
  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/pricing/funds/new");
    fireEvent.click(screen.getByRole("button", { name: "Save fund" }));
    expect(await screen.findByText("Enter the budget per period as a number such as 25000.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a fund with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/funds")) return json(201, fund);
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, { ...usage, items: [], periods: [] });
      return undefined;
    };
    renderAt("/pricing/funds/new");
    fill("Legal entity id", LE);
    fill("Fund name", "Spring TPM");
    fill("Owner", "tm-lead");
    fill("Budget per period", "1000");
    fireEvent.click(screen.getByRole("button", { name: "Save fund" }));
    expect(await screen.findByRole("heading", { name: "Spring TPM" })).toBeInTheDocument();
    expect(await screen.findAllByText("No usage yet")).toHaveLength(2);
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, name: "Spring TPM", type: "TRADE", currency: "USD", customerScope: "", owner: "tm-lead", budgetPerPeriod: "1000", allowOverspend: false });
  });

  it("shows the service's field errors", async () => {
    server.handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { currency: "must be three upper-case letters" } });
    renderAt("/pricing/funds/new");
    fill("Legal entity id", LE);
    fill("Fund name", "x");
    fill("Owner", "o");
    fill("Budget per period", "1");
    fireEvent.click(screen.getByRole("button", { name: "Save fund" }));
    expect(await screen.findByText("must be three upper-case letters")).toBeInTheDocument();
  });
});

describe("Trade fund detail", () => {
  it("shows usage by period and source and records usage", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/funds/${FUND}:attribute`)) {
        return json(201, { fundId: FUND, period: "2027-03", budget: "1000", used: "725", remaining: "275", closed: false });
      }
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    const { container } = renderAt(`/pricing/funds/${FUND}`);
    expect(await screen.findByRole("heading", { name: "Spring TPM" })).toBeInTheDocument();
    const periods = screen.getByRole("table", { name: "Usage by period" });
    expect(within(periods).getAllByRole("row").map((r) => r.textContent)).toEqual(["PeriodBudgetUsedRemaining", "2027-031000625375"]);
    expect(screen.getByRole("table", { name: "Usage by source" })).toHaveTextContent("Deduction");
    expect(pageViolations(container)).toEqual([]);

    fill("Period", "2027-3");
    fireEvent.click(screen.getByRole("button", { name: "Record usage" }));
    expect(await screen.findByText("Enter the fiscal period as YYYY-MM.")).toBeInTheDocument();
    fill("Period", "2027-03");
    fireEvent.change(screen.getByLabelText("Source"), { target: { value: "REBATE_ACCRUAL" } });
    fill("Reference", "ACC-7");
    fill("Amount (USD)", "100");
    fireEvent.click(screen.getByRole("button", { name: "Record usage" }));
    expect(await screen.findByText("Recorded. 2027-03: 725 used of 1000 USD, 275 remaining.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ period: "2027-03", source: "REBATE_ACCRUAL", reference: "ACC-7", amount: "100" });
    expect(idempotencyKey(post!)).toBeTruthy();
  });

  it("explains an over-budget refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "PRC-0012", detail: "fund Spring TPM budget exceeded by 100.00" });
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    renderAt(`/pricing/funds/${FUND}`);
    await screen.findByRole("heading", { name: "Spring TPM" });
    fill("Period", "2027-03");
    fill("Reference", "CLM-9");
    fill("Amount (USD)", "475");
    fireEvent.click(screen.getByRole("button", { name: "Record usage" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Over the period budget");
    expect(alert).toHaveTextContent("budget exceeded by 100.00");
  });

  it("revises the budget at the loaded version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/funds/${FUND}:revise`)) return json(200, { ...fund, budgetPerPeriod: "1500", allowOverspend: true, version: 2 });
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    renderAt(`/pricing/funds/${FUND}`);
    await screen.findByRole("heading", { name: "Spring TPM" });
    fill("Budget per period", "1500");
    fireEvent.click(screen.getByLabelText("Allow overspend"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    expect(await screen.findByText("Budget per period is now 1500 USD; overspend allowed.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ version: 1, budgetPerPeriod: "1500", allowOverspend: true });
  });

  it("closes the fund, hiding attribution, and reopens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/funds/${FUND}:close`)) return json(200, { ...fund, closed: true, version: 2 });
      if (init.method === "POST" && url.pathname.endsWith(`/funds/${FUND}:reopen`)) return json(200, { ...fund, closed: false, version: 3 });
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    renderAt(`/pricing/funds/${FUND}`);
    await screen.findByRole("heading", { name: "Spring TPM" });
    fireEvent.click(screen.getByRole("button", { name: "Close fund" }));
    expect(await screen.findByText("The fund is closed. Its usage is frozen and new usage is refused.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record usage" })).not.toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith(":close"))?.body).toEqual({ version: 1 });
    fireEvent.click(screen.getByRole("button", { name: "Reopen fund" }));
    expect(await screen.findByText("The fund is open again and takes new usage.")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname.endsWith(":reopen"))?.body).toEqual({ version: 2 });
    expect(screen.getByRole("button", { name: "Record usage" })).toBeInTheDocument();
  });

  it("shows a stale-version refusal and a load error with a retry", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "fund version does not match" });
      if (url.pathname.endsWith(`/funds/${FUND}/usage`)) return json(200, usage);
      return undefined;
    };
    const first = renderAt(`/pricing/funds/${FUND}`);
    await screen.findByRole("heading", { name: "Spring TPM" });
    fireEvent.click(screen.getByRole("button", { name: "Close fund" }));
    expect(await findAlert()).toHaveTextContent("fund version does not match");
    first.unmount();
    server.handler = () => json(404, { code: "PRC-0039", detail: `no trade fund ${FUND}` });
    renderAt(`/pricing/funds/${FUND}`);
    expect(await findAlert()).toHaveTextContent("PRC-0039");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("fund form rules", () => {
  it("mirrors the service rules", () => {
    const ok = { ...emptyFund, legalEntityId: LE, name: "F", owner: "o", budgetPerPeriod: "0" };
    expect(validateFund(ok)).toEqual({});
    expect(validateFund({ ...ok, currency: "usd" }).currency).toBeDefined();
    expect(validateFund({ ...ok, budgetPerPeriod: "-1" }).budgetPerPeriod).toBeDefined();
    expect(toFundInput({ ...ok, name: " F " }).name).toBe("F");
    expect(validateAttribution({ period: "2027-03", source: "CLAIM", reference: "R", amount: "0" }).amount).toBeDefined();
    expect(validateAttribution({ period: "2027-12", source: "CLAIM", reference: "R", amount: "0.01" })).toEqual({});
  });
});
