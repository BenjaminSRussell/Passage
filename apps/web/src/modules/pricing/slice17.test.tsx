import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Campaign, RebateAgreement } from "./api/client";
import { addDays, today } from "./format";
import { idempotencyKey, json, pageViolations, renderAt, server, text, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CMP = "0190f0a0-0000-7000-8000-000000000b01";
const A1 = "0190f0a0-0000-7000-8000-000000000401";
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

const closed: RebateAgreement = {
  id: A1,
  legalEntityId: LE,
  name: "Acme quarterly",
  partyRole: "CUSTOMER",
  customerId: "0190f0a0-0000-7000-8000-000000000501",
  basis: "NET_AMOUNT",
  period: "QUARTER",
  tiers: [{ from: "0", ratePct: "1", amountPerUnit: "0" }],
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-05-31",
  accrualAccount: "2150",
  expenseAccount: "4150",
  status: "CLOSED",
  version: 3,
};

function fill(label: string, value: string) {
  const field = screen.queryByLabelText(label) ?? screen.getByLabelText(new RegExp(`^${label}`));
  fireEvent.change(field, { target: { value } });
}

function campaignServer(handler?: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined) {
  server.handler = (url, init) => {
    const custom = handler?.(url, init);
    if (custom) return custom;
    if (url.pathname.endsWith(`/campaigns/${CMP}`)) return json(200, campaign);
    if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
    return undefined;
  };
}

describe("Edit campaign", () => {
  it("sends only the changed terms with the version and an Idempotency-Key, then shows the new terms", async () => {
    campaignServer((url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/campaigns/${CMP}:revise`)) return json(200, { ...campaign, name: "Spring push II", budget: "7500.25", validTo: addDays(day, 10), version: 2 });
      return undefined;
    });
    const { container } = renderAt(`/pricing/campaigns/${CMP}`);
    expect(await screen.findByRole("heading", { name: "Spring push" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill("Name", "Spring push II");
    fill("Valid to", addDays(day, 10));
    fill("Budget \\(USD\\)", "7500.25");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "Spring push II" })).toBeInTheDocument();
    expect(screen.getAllByRole("status").some((s) => /updated/.test(s.textContent ?? ""))).toBe(true);
    const revise = server.calls.find((c) => c.method === "POST")!;
    expect(revise.body).toEqual({ version: 1, name: "Spring push II", validTo: addDays(day, 10), budget: "7500.25" });
    expect(idempotencyKey(revise)).toBeTruthy();
    expect(screen.getByText("version 2", { exact: false })).toBeInTheDocument();
  });

  it("checks the terms before calling the service", async () => {
    campaignServer();
    renderAt(`/pricing/campaigns/${CMP}`);
    await screen.findByRole("heading", { name: "Spring push" });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Change at least one term.")).toBeInTheDocument();
    fill("Valid to", addDays(day, -30));
    fill("Budget \\(USD\\)", "lots");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The end date cannot be before the start date.")).toBeInTheDocument();
    expect(screen.getByText("Enter the budget as a number, 0 or more.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("keeps the days a started campaign already priced", async () => {
    campaignServer((_url, init) =>
      init.method === "POST"
        ? json(422, { code: "COM-0001", detail: "request is invalid", errors: { validTo: "must be the day before today or later" } })
        : undefined,
    );
    renderAt(`/pricing/campaigns/${CMP}`);
    await screen.findByRole("heading", { name: "Spring push" });
    expect(screen.getByLabelText(/^Valid from/)).toHaveAttribute("readonly");
    expect(screen.getByLabelText(/^Customer group/)).toHaveAttribute("readonly");
    expect(screen.getByText(`Fixed: the campaign has priced since ${campaign.validFrom}.`)).toBeInTheDocument();
    fill("Valid to", addDays(day, -3));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(`End it on ${addDays(day, -1)} or later: the days already priced stay in the campaign.`)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Valid to", addDays(day, 2));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("must be the day before today or later")).toBeInTheDocument();
  });

  it("reports a refused edit and keeps the form", async () => {
    campaignServer((_url, init) => (init.method === "POST" ? json(412, { code: "conflict_version", detail: "campaign version does not match" }) : undefined));
    renderAt(`/pricing/campaigns/${CMP}`);
    await screen.findByRole("heading", { name: "Spring push" });
    fill("Name", "Other");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The campaign changed since you opened it")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("(conflict_version)");
    expect(screen.getByLabelText("Name")).toHaveValue("Other");
  });
});

describe("Reopen a closed rebate agreement", () => {
  function agreementServer(handler?: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined) {
    server.handler = (url, init) => {
      const custom = handler?.(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, closed);
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
  }

  it("asks for a date after the close date before calling the service", async () => {
    agreementServer();
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fireEvent.click(screen.getByRole("button", { name: "Reopen agreement" }));
    expect(await screen.findByText("Enter the new end date as YYYY-MM-DD.")).toBeInTheDocument();
    fill("New valid to", "2027-05-31");
    fireEvent.click(screen.getByRole("button", { name: "Reopen agreement" }));
    expect(await screen.findByText("Choose a day after 2027-05-31.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("reopens the agreement and shows the open state", async () => {
    agreementServer((url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/agreements/${A1}:reopen`)) return json(200, { ...closed, status: "ACTIVE", validTo: "2027-12-31", version: 4 });
      return undefined;
    });
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("New valid to", "2027-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Reopen agreement" }));
    expect(await screen.findByRole("button", { name: "Close agreement" })).toBeInTheDocument();
    expect(screen.getAllByRole("status").some((s) => /open again; invoices up to 2027-12-31 accrue/.test(s.textContent ?? ""))).toBe(true);
    const reopen = server.calls.find((c) => c.method === "POST")!;
    expect(reopen.body).toEqual({ version: 3, validTo: "2027-12-31" });
    expect(idempotencyKey(reopen)).toBeTruthy();
  });

  it("reports a refused reopen", async () => {
    agreementServer((_url, init) => (init.method === "POST" ? json(409, { code: "PRC-0031", detail: "rebate agreement is ACTIVE" }) : undefined));
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("New valid to", "2027-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Reopen agreement" }));
    expect(await screen.findByText("The agreement is closed")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("(PRC-0031)");
  });
});

describe("Trade agreements export", () => {
  const CSV = "recordId,type,status\r\nr1,PRICE,ACTIVE\r\nr2,LINE_DISC,ACTIVE\r\n";

  it("downloads every matching record with the on-screen filters", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/condition-records:export"))
        return text(200, CSV, { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="trade-agreements-2027-03-01.csv"' });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    const blobs: Blob[] = [];
    Object.assign(URL, {
      createObjectURL: vi.fn((blob: Blob) => {
        blobs.push(blob);
        return "blob:agreements";
      }),
      revokeObjectURL: vi.fn(),
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.download).toBe("trade-agreements-2027-03-01.csv");
    });
    renderAt("/pricing/agreements");
    fireEvent.change(await screen.findByLabelText("Status"), { target: { value: "ACTIVE" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "ACTIVE")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const call = server.calls.find((c) => c.url.pathname.endsWith("/condition-records:export"))!;
    expect(call.url.searchParams.get("status")).toBe("ACTIVE");
    expect(call.url.searchParams.has("limit")).toBe(false);
    expect(blobs).toHaveLength(1);
    expect(await screen.findByText("Exported 2 trade agreements to trade-agreements-2027-03-01.csv.")).toBeInTheDocument();
    click.mockRestore();
  });

  it("says when the file holds fewer records than matched, and reports a refused export", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/condition-records:export")) return text(200, CSV, { "Content-Type": "text/csv", "X-Passage-Truncated": "true" });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    Object.assign(URL, { createObjectURL: vi.fn(() => "blob:x"), revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderAt("/pricing/agreements");
    fireEvent.click(await screen.findByRole("button", { name: "Export CSV" }));
    expect(await screen.findByText(/More records matched than the file holds/)).toBeInTheDocument();
    click.mockRestore();

    server.handler = (url) => (url.pathname.endsWith("/condition-records:export") ? json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" }) : json(200, { items: [] }));
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));
    expect(await screen.findByText("Could not export the trade agreements")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("(AUTH-0003)");
  });
});
