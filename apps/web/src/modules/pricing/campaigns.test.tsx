import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Campaign, ConditionRecord } from "./api/client";
import { campaignState, describeTarget } from "./campaignFormat";
import { addDays, today } from "./format";
import { emptyCampaign, toCampaignInput, validateCampaign } from "./NewCampaignPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CMP = "0190f0a0-0000-7000-8000-000000000b01";
const day = today();

const campaign: Campaign = {
  id: CMP,
  legalEntityId: LE,
  name: "Spring push",
  validFrom: addDays(day, -5),
  validTo: addDays(day, 20),
  targetCustomerGroup: "RETAIL",
  budget: "5000",
  currency: "USD",
  killed: false,
  version: 1,
};

const promoRecord: ConditionRecord = {
  id: "0190f0a0-0000-7000-8000-000000000c01",
  legalEntityId: LE,
  type: "PRICE",
  customerKind: "all",
  customerKey: "",
  productKind: "product",
  productKey: "BOLT",
  variant: "",
  currency: "USD",
  unit: "EA",
  fromQuantity: "0",
  amount: "8.5",
  discPct1: "0",
  discPct2: "0",
  validFrom: "2026-01-01",
  validTo: "2026-12-31",
  searchNext: false,
  exclusive: false,
  status: "ACTIVE",
  version: 2,
  campaignId: CMP,
};

/** Fills the field with this exact label, or the one whose label starts with it (optional fields add a suffix). */
function fill(label: string, value: string) {
  const field = screen.queryByLabelText(label) ?? screen.getByLabelText(new RegExp(`^${label}`));
  fireEvent.change(field, { target: { value } });
}

describe("campaign state", () => {
  it("puts the kill switch before the validity and treats both ends as inclusive", () => {
    const c = { killed: false, validFrom: "2026-03-01", validTo: "2026-03-31" };
    expect(campaignState(c, "2026-02-28")).toBe("scheduled");
    expect(campaignState(c, "2026-03-01")).toBe("live");
    expect(campaignState(c, "2026-03-31")).toBe("live");
    expect(campaignState(c, "2026-04-01")).toBe("ended");
    expect(campaignState({ ...c, killed: true }, "2026-03-15")).toBe("killed");
    expect(describeTarget("")).toBe("All customers");
    expect(describeTarget("RETAIL")).toBe("Customer group RETAIL");
  });
});

describe("Campaigns", () => {
  it("lists campaigns with their status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/campaigns"))
        return json(200, { items: [campaign, { ...campaign, id: "c-2", name: "Autumn", killed: true }, { ...campaign, id: "c-3", name: "Winter", validFrom: addDays(day, 30), validTo: addDays(day, 60) }] });
      if (url.pathname.endsWith(`/campaigns/${CMP}`)) return json(200, campaign);
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [promoRecord] });
      return undefined;
    };
    const { container } = renderAt("/pricing/campaigns");
    const table = await findTable({ name: "Campaigns" });
    await waitFor(() => expect(table).toHaveTextContent("Spring push"));
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("Customer group RETAIL");
    expect(rows[1]).toHaveTextContent("5000 USD");
    expect(rows[1]).toHaveTextContent("Live");
    expect(rows[2]).toHaveTextContent("Killed");
    expect(rows[3]).toHaveTextContent("Scheduled");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Spring push" }));
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/campaigns");
    expect(await screen.findByText("No campaigns")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" });
    renderAt("/pricing/campaigns");
    expect(await findAlert()).toHaveTextContent("(AUTH-0003)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New campaign", () => {
  it("validates like the service", () => {
    const ok = { ...emptyCampaign, legalEntityId: LE, name: "Spring", validFrom: "2026-03-01", validTo: "2026-03-31", budget: "5000" };
    expect(validateCampaign(ok)).toEqual({});
    expect(Object.keys(validateCampaign({ ...ok, validTo: "2026-02-01", budget: "-1", currency: "usd", legalEntityId: "x" })).sort()).toEqual([
      "budget",
      "currency",
      "legalEntityId",
      "validTo",
    ]);
    expect(toCampaignInput({ ...ok, name: "  Spring  ", targetCustomerGroup: " RETAIL " })).toMatchObject({ name: "Spring", targetCustomerGroup: "RETAIL" });
  });

  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/pricing/campaigns/new");
    fireEvent.click(screen.getByRole("button", { name: "Save campaign" }));
    expect(await screen.findByText("Enter the budget as a number such as 5000.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a campaign with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/campaigns")) return json(201, campaign);
      if (url.pathname.endsWith(`/campaigns/${CMP}`)) return json(200, campaign);
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/pricing/campaigns/new");
    fill("Legal entity id", LE);
    fill("Campaign name", "Spring push");
    fill("Valid from", "2026-03-01");
    fill("Valid to", "2026-03-31");
    fill("Target customer group", "RETAIL");
    fill("Budget", "5000");
    fireEvent.click(screen.getByRole("button", { name: "Save campaign" }));
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      legalEntityId: LE,
      name: "Spring push",
      validFrom: "2026-03-01",
      validTo: "2026-03-31",
      targetCustomerGroup: "RETAIL",
      budget: "5000",
      currency: "USD",
    });
    expect(idempotencyKey(post)).toBeTruthy();
    expect(await screen.findByText("No agreements yet")).toBeInTheDocument();
  });

  it("shows service field errors next to the fields", async () => {
    server.handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { validity: "validTo must not be before validFrom" } });
    renderAt("/pricing/campaigns/new");
    fill("Legal entity id", LE);
    fill("Campaign name", "Spring push");
    fill("Valid from", "2026-03-01");
    fill("Valid to", "2026-03-31");
    fill("Budget", "5000");
    fireEvent.click(screen.getByRole("button", { name: "Save campaign" }));
    expect(await screen.findByText("validTo must not be before validFrom")).toBeInTheDocument();
  });
});

describe("Campaign detail", () => {
  it("shows the campaign's agreements and kills it at its version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/campaigns/${CMP}:kill`)) return json(200, { ...campaign, killed: true, version: 2 });
      if (url.pathname.endsWith(`/campaigns/${CMP}`)) return json(200, campaign);
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [promoRecord] });
      return undefined;
    };
    const { container } = renderAt(`/pricing/campaigns/${CMP}`);
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Campaign agreements" });
    expect(table).toHaveTextContent("BOLT");
    expect(table).toHaveTextContent("8.5 USD");
    expect(server.calls.find((c) => c.url.pathname.endsWith("/condition-records"))!.url.searchParams.get("campaignId")).toBe(CMP);
    expect(screen.getByRole("link", { name: "Add agreement" })).toHaveAttribute("href", `/pricing/agreements/new?campaign=${CMP}`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Kill campaign" }));
    expect(await screen.findByRole("button", { name: "Restore campaign" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("leave the next price resolution");
    expect(screen.getByText("Killed")).toBeInTheDocument();
    const kill = server.calls.find((c) => c.method === "POST")!;
    expect(kill.body).toEqual({ version: 1 });
    expect(idempotencyKey(kill)).toBeTruthy();
  });

  it("reports a refused restore and keeps the page", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "campaign version does not match" });
      if (url.pathname.endsWith(`/campaigns/${CMP}`)) return json(200, { ...campaign, killed: true, version: 4 });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/campaigns/${CMP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Restore campaign" }));
    expect(await findAlert()).toHaveTextContent("campaign version does not match (conflict_version)");
    expect(server.calls.find((c) => c.method === "POST")!.body).toEqual({ version: 4 });
  });

  it("shows the error state when the campaign is unknown", async () => {
    server.handler = () => json(404, { code: "PRC-0041", detail: "no campaign x" });
    renderAt(`/pricing/campaigns/${CMP}`);
    expect(await findAlert()).toHaveTextContent("(PRC-0041)");
  });
});

describe("Trade agreement in a campaign", () => {
  it("prefills the campaign from the link and sends it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/condition-records")) return json(201, { ...promoRecord, status: "DRAFT", version: 1 });
      if (url.pathname.endsWith(`/condition-records/${promoRecord.id}`)) return json(200, { ...promoRecord, status: "DRAFT", version: 1 });
      return undefined;
    };
    renderAt(`/pricing/agreements/new?campaign=${CMP}`);
    expect(screen.getByLabelText(/^Campaign id/)).toHaveValue(CMP);
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Product", "BOLT");
    fill("Price", "8.5");
    fill("Valid from", "2026-01-01");
    fill("Valid to", "2026-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST")).toBe(true));
    expect(server.calls.find((c) => c.method === "POST")!.body).toMatchObject({ campaignId: CMP, productKey: "BOLT" });
    expect(await screen.findByRole("link", { name: CMP })).toHaveAttribute("href", `/pricing/campaigns/${CMP}`);
  });

  it("refuses a malformed campaign id before calling the service", async () => {
    renderAt("/pricing/agreements/new?campaign=not-a-uuid");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Enter the campaign id (a UUID) or leave it empty.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });
});
