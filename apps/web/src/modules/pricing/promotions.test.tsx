import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Promotion } from "./api/client";
import { emptyPromotion, toPromotionInput, validatePromotion, baselineWeeks } from "./NewPromotionPage";
import { toActuals, validateActuals } from "./PromotionDetailPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const FUND = "0190f0a0-0000-7000-8000-000000000901";
const PROMO = "0190f0a0-0000-7000-8000-000000000a01";
const WEEKS = Array.from({ length: 13 }, () => "10");

const plan: Promotion = {
  id: PROMO,
  legalEntityId: LE,
  name: "Spring push",
  eventCategory: "FEATURE",
  eventType: "TPR",
  customerScope: "",
  fundId: FUND,
  productId: "P-1",
  currency: "USD",
  startDate: "2027-03-01",
  endDate: "2027-03-28",
  baselineWeeklyUnits: WEEKS,
  baseline: "10",
  liftPct: "25",
  plannedSpend: "500",
  forecastUnits: "50",
};
const CMP = "0190f0a0-0000-7000-8000-000000000b01";

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Promotions", () => {
  it("lists promotions with forecast and actual lift and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/promotions")) return json(200, { items: [plan, { ...plan, id: "p-2", name: "Summer", effectivenessPct: "-12.5", actuals: { units: "35", weeks: 4, spend: "0" } }] });
      if (url.pathname.endsWith(`/promotions/${PROMO}`)) return json(200, plan);
      return undefined;
    };
    const { container } = renderAt("/pricing/promotions");
    const table = await findTable({ name: "Promotions" });
    await waitFor(() => expect(table).toHaveTextContent("Spring push"));
    expect(table).toHaveTextContent("FEATURE / TPR");
    expect(table).toHaveTextContent("+25%");
    expect(table).toHaveTextContent("Awaiting actuals");
    expect(table).toHaveTextContent("-12.5%");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Spring push" }));
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
  });

  it("shows the campaign of a plan as a link", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/promotions")) return json(200, { items: [{ ...plan, campaignId: CMP }] });
      return undefined;
    };
    renderAt("/pricing/promotions");
    const table = await findTable({ name: "Promotions" });
    await waitFor(() => expect(within(table).getByRole("link", { name: CMP })).toHaveAttribute("href", `/pricing/campaigns/${CMP}`));
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/promotions");
    expect(await screen.findByText("No promotions")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/pricing/promotions");
    expect(await findAlert()).toHaveTextContent("database unavailable (COM-0500)");
  });
});

describe("New promotion", () => {
  it("needs 13 baseline weeks before calling the service", async () => {
    const { container } = renderAt("/pricing/promotions/new");
    fill("Baseline weekly units", "10, 10, 10");
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    expect(await screen.findByText("Enter 13 weekly unit counts, oldest first (you entered 3).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a promotion against a fund and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/promotions")) return json(201, plan);
      if (url.pathname.endsWith(`/promotions/${PROMO}`)) return json(200, plan);
      return undefined;
    };
    renderAt("/pricing/promotions/new");
    fill("Legal entity id", LE);
    fill("Promotion name", "Spring push");
    fill("Event category", "FEATURE");
    fill("Event type", "TPR");
    fill("Fund id", FUND);
    fill("Product id", "P-1");
    fill("First day", "2027-03-01");
    fill("Last day", "2027-03-28");
    fill("Forecast lift %", "25");
    fill("Planned spend", "500");
    fill("Baseline weekly units", WEEKS.join("\n"));
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({
      legalEntityId: LE,
      name: "Spring push",
      eventCategory: "FEATURE",
      eventType: "TPR",
      customerScope: "",
      fundId: FUND,
      productId: "P-1",
      currency: "USD",
      startDate: "2027-03-01",
      endDate: "2027-03-28",
      baselineWeeklyUnits: WEEKS,
      liftPct: "25",
      plannedSpend: "500",
    });
  });

  it("prefills the campaign from the link and sends it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/promotions")) return json(201, { ...plan, campaignId: CMP });
      if (url.pathname.endsWith(`/promotions/${PROMO}`)) return json(200, { ...plan, campaignId: CMP });
      return undefined;
    };
    renderAt(`/pricing/promotions/new?campaign=${CMP}&legalEntity=${LE}&currency=EUR`);
    expect(screen.getByLabelText(/^Campaign id/)).toHaveValue(CMP);
    expect(screen.getByLabelText("Legal entity id")).toHaveValue(LE);
    expect(screen.getByLabelText("Currency")).toHaveValue("EUR");
    fill("Promotion name", "Spring push");
    fill("Product id", "P-1");
    fill("First day", "2027-03-01");
    fill("Last day", "2027-03-28");
    fill("Baseline weekly units", WEEKS.join("\n"));
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toMatchObject({ campaignId: CMP, legalEntityId: LE, currency: "EUR", productId: "P-1" });
    expect(screen.getByRole("link", { name: "Campaign" })).toHaveAttribute("href", `/pricing/campaigns/${CMP}`);
  });

  it("refuses a malformed campaign id before calling the service", async () => {
    renderAt("/pricing/promotions/new?campaign=not-a-uuid");
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    expect(await screen.findByText("Enter the campaign id (a UUID) or leave it empty.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("explains a killed campaign", async () => {
    server.handler = () => json(409, { code: "PRC-0069", detail: "campaign Spring is killed" });
    renderAt(`/pricing/promotions/new?campaign=${CMP}`);
    fill("Legal entity id", LE);
    fill("Promotion name", "x");
    fill("Product id", "P-1");
    fill("First day", "2027-03-01");
    fill("Last day", "2027-03-01");
    fill("Baseline weekly units", WEEKS.join(","));
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The campaign is killed");
    expect(alert).toHaveTextContent("(PRC-0069)");
  });

  it("explains a closed fund", async () => {
    server.handler = () => json(409, { code: "PRC-0011", detail: "fund Spring TPM is closed" });
    renderAt("/pricing/promotions/new");
    fill("Legal entity id", LE);
    fill("Promotion name", "x");
    fill("Fund id", FUND);
    fill("Product id", "P-1");
    fill("First day", "2027-03-01");
    fill("Last day", "2027-03-01");
    fill("Baseline weekly units", WEEKS.join(","));
    fireEvent.click(screen.getByRole("button", { name: "Save promotion" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The fund is closed");
    expect(alert).toHaveTextContent("(PRC-0011)");
  });
});

describe("Promotion detail", () => {
  it("shows the plan and records actuals with their effectiveness", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/promotions/${PROMO}:actuals`)) {
        return json(200, { ...plan, actuals: { units: "240", weeks: 4, spend: "480" }, effectivenessPct: "500" });
      }
      if (url.pathname.endsWith(`/promotions/${PROMO}`)) return json(200, plan);
      return undefined;
    };
    const { container } = renderAt(`/pricing/promotions/${PROMO}`);
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    expect(screen.getByText("No actuals yet. The nightly job will fill them from posted invoices; until then record them below.")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Baseline weeks" })).getAllByRole("row")).toHaveLength(14);
    expect(screen.getByRole("link", { name: "Fund" })).toHaveAttribute("href", `/pricing/funds/${FUND}`);
    expect(pageViolations(container)).toEqual([]);

    fill("Units invoiced", "240");
    fill("Weeks", "0");
    fireEvent.click(screen.getByRole("button", { name: "Record actuals" }));
    expect(await screen.findByText("Enter the number of weeks, from 1 to 260.")).toBeInTheDocument();
    fill("Weeks", "4");
    fill("Spend (USD)", "480");
    fireEvent.click(screen.getByRole("button", { name: "Record actuals" }));
    expect(await screen.findByText("Actuals recorded: +500% against the baseline.")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ units: "240", weeks: 4, spend: "480" });
  });

  it("shows a load error with a retry", async () => {
    server.handler = () => json(404, { code: "PRC-0040", detail: `no promotion plan ${PROMO}` });
    renderAt(`/pricing/promotions/${PROMO}`);
    expect(await findAlert()).toHaveTextContent("(PRC-0040)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("promotion form rules", () => {
  it("mirrors the service rules", () => {
    const ok = { ...emptyPromotion, legalEntityId: LE, name: "P", productId: "P-1", startDate: "2027-03-01", endDate: "2027-03-07", baseline: WEEKS.join(" ") };
    expect(validatePromotion(ok)).toEqual({});
    expect(validatePromotion({ ...ok, endDate: "2027-02-28" }).endDate).toBeDefined();
    expect(validatePromotion({ ...ok, liftPct: "-101" }).liftPct).toBeDefined();
    expect(validatePromotion({ ...ok, liftPct: "-20" })).toEqual({});
    expect(validatePromotion({ ...ok, fundId: "not-a-uuid" }).fundId).toBeDefined();
    expect(validatePromotion({ ...ok, campaignId: "not-a-uuid" }).campaignId).toBeDefined();
    expect(validatePromotion({ ...ok, campaignId: CMP })).toEqual({});
    expect(toPromotionInput({ ...ok, campaignId: ` ${CMP} ` }).campaignId).toBe(CMP);
    expect(toPromotionInput(ok).campaignId).toBeUndefined();
    expect(toPromotionInput(ok).fundId).toBeUndefined();
    expect(baselineWeeks("1,2;3\n4  5")).toEqual(["1", "2", "3", "4", "5"]);
    expect(validateActuals({ units: "1", weeks: "261", spend: "" }).weeks).toBeDefined();
    expect(toActuals({ units: "1", weeks: "2", spend: "" })).toEqual({ units: "1", weeks: 2, spend: "0" });
  });
});
