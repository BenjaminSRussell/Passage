import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { ScreeningHit } from "./api/client";
import { parseItems, parseLicenses, toExportInput, type ExportValues } from "./ExportCheckPage";
import compliance from "./module";
import { parseEntries, toScreenInput, type ScreenValues } from "./ScreeningCheckPage";
import { validateDecision } from "./ScreeningHitPage";

const PARTY = "55555555-0000-4000-8000-000000000005";
const HIT = "66666666-0000-4000-8000-000000000006";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
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
        {compliance.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const problem = (status: number, code: string, detail: string): Reply => ({ status, body: { type: "x", title: "x", status, code, detail } });

function hit(patch: Partial<ScreeningHit> = {}): ScreeningHit {
  return {
    hitId: HIT,
    subjectType: "PARTY",
    subjectId: PARTY,
    subjectName: "Volga Maritim Holdings",
    listCode: "OFAC_SDN",
    listEntryId: "SDN-7001",
    listEntryVersion: 2,
    listEntryName: "Volga Maritime Holdings",
    score: 0.92,
    matchedFields: ["name", "country"],
    state: "OPEN",
    detectedAt: "2026-11-01T09:00:00Z",
    updatedAt: "2026-11-01T09:00:00Z",
    version: 1,
    ...patch,
  };
}

describe("screening helpers", () => {
  it("parses one list entry per line with optional country, address and aliases", () => {
    expect(parseEntries("ofac_sdn | SDN-7001 | 2 | Volga Maritime Holdings | ru | 1 Port St | Volga Holdings; VMH\n\nUN_SC | UN-1 | 1 | Northern Star").entries).toEqual([
      { listCode: "OFAC_SDN", entryId: "SDN-7001", version: 2, name: "Volga Maritime Holdings", country: "RU", address: "1 Port St", aliases: ["Volga Holdings", "VMH"] },
      { listCode: "UN_SC", entryId: "UN-1", version: 1, name: "Northern Star" },
    ]);
    expect(parseEntries("OFAC_SDN").error).toBe("Line 1: start with the list code and the entry id.");
    expect(parseEntries("OFAC_SDN | 1 | 0 | X").error).toBe("Line 1: the version must be a whole number from 1.");
    expect(parseEntries("OFAC_SDN | 1 | 1 |").error).toBe("Line 1: give the listed name.");
    expect(parseEntries("OFAC_SDN | 1 | 1 | X | Russia").error).toBe("Line 1: the country must be a two-letter code such as RU.");
    expect(parseEntries("OFAC_SDN | 1 | 1 | X\nofac_sdn | 1 | 2 | Y").error).toBe("Line 2: entry 1 of OFAC_SDN is listed twice.");
  });

  it("builds the subject and refuses a missing id, name or entries", () => {
    const values: ScreenValues = { type: "PARTY", id: PARTY.toUpperCase(), name: " Volga ", address: "", country: "ru", entries: "OFAC_SDN | 1 | 1 | Volga" };
    expect(toScreenInput(values).subject).toEqual({ type: "PARTY", id: PARTY, name: "Volga", country: "RU" });
    const bad = toScreenInput({ ...values, id: "P-1", name: " ", entries: "" }).errors;
    expect(bad).toEqual({ id: "Enter the party's or document's UUID.", name: "Enter the name to screen.", entries: "Paste at least one list entry to screen against." });
  });

  it("needs a decision and a reason", () => {
    expect(validateDecision("", "")).toEqual({ decision: "Choose clear or confirm.", reason: "Record why: the decision is kept for five years." });
    expect(validateDecision("CONFIRM", "same IMO number")).toEqual({});
  });
});

describe("Screening hits", () => {
  it("lists open hits by default and filters by state and list", async () => {
    handler = () => ({ status: 200, body: { items: [hit()] } });
    const { container } = renderAt("/compliance/screening/hits");
    const table = await findTable({ name: "Screening hits" });
    expect(calls[0].url.pathname).toBe("/api/v1/trade/screening/hits");
    expect(calls[0].url.searchParams.get("state")).toBe("OPEN");
    expect(within(table).getByText("Volga Maritim Holdings")).toBeInTheDocument();
    expect(within(table).getByText("0.920")).toBeInTheDocument();
    expect(within(table).getByText("name, country")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("State"), { target: { value: "CONFIRMED" } });
    fireEvent.change(screen.getByLabelText(/^List/), { target: { value: "ofac_sdn" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await findTable({ name: "Screening hits" });
    const last = calls[calls.length - 1].url.searchParams;
    expect(last.get("state")).toBe("CONFIRMED");
    expect(last.get("listCode")).toBe("OFAC_SDN");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/compliance/screening/hits");
    expect(await screen.findByText("No hits match")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => problem(403, "AUTH-0003", "missing duty TradeComplianceView");
    renderAt("/compliance/screening/hits");
    expect((await findAlert()).textContent).toContain("missing duty TradeComplianceView (AUTH-0003)");
    handler = () => ({ status: 200, body: { items: [hit()] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Screening hits" })).toBeInTheDocument();
  });
});

describe("Screening hit", () => {
  it("compares the subject with the list entry and clears it as a false positive", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, etag: '"2"', body: hit({ state: "FALSE_POSITIVE", version: 2, decidedBy: "officer-1", reason: "different registry number", decidedAt: "2026-11-02T09:00:00Z" }) }
        : { status: 200, etag: '"1"', body: hit() };
    const { container } = renderAt(`/compliance/screening/hits/${HIT}`);
    expect(await screen.findByRole("heading", { name: "Hit on Volga Maritim Holdings" })).toBeInTheDocument();
    expect(screen.getByText("Volga Maritime Holdings")).toBeInTheDocument();
    expect(screen.getByText("OFAC_SDN v2")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect(await screen.findByText("Choose clear or confirm.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Decision"), { target: { value: "CLEAR_FALSE_POSITIVE" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "different registry number" } });
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect((await findStatus()).textContent).toContain("Cleared as a false positive");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/trade/screening/hits/${HIT}/decision`);
    expect(post?.body).toEqual({ decision: "CLEAR_FALSE_POSITIVE", reason: "different registry number" });
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    // Once decided the form is gone and the decision shows.
    expect(screen.queryByRole("form", { name: "Decide screening hit" })).toBeNull();
    expect(screen.getByText("different registry number")).toBeInTheDocument();
  });

  it("shows the refusal of a stale decision", async () => {
    handler = (_url, init) => (init.method === "POST" ? problem(412, "TRD-1009", "the hit was changed since you loaded it") : { status: 200, etag: '"1"', body: hit() });
    renderAt(`/compliance/screening/hits/${HIT}`);
    fireEvent.change(await screen.findByLabelText("Decision"), { target: { value: "CONFIRM" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "same IMO number" } });
    fireEvent.click(screen.getByRole("button", { name: "Record decision" }));
    expect((await findAlert()).textContent).toContain("changed since you loaded it (TRD-1009)");
  });

  it("shows a missing hit", async () => {
    handler = () => problem(404, "TRD-1008", "no screening hit with that id");
    renderAt(`/compliance/screening/hits/${HIT}`);
    expect((await findAlert()).textContent).toContain("no screening hit with that id (TRD-1008)");
    expect(screen.getByRole("link", { name: "Back to screening hits" })).toBeInTheDocument();
  });
});

describe("Screening check", () => {
  it("screens a party and lists the blocking hits", async () => {
    handler = () => ({ status: 200, body: { blocked: true, hits: [hit()] } });
    const { container } = renderAt("/compliance/screening/check");
    expect(screen.getByText("Nothing screened yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Subject id"), { target: { value: PARTY } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Volga Maritim Holdings" } });
    fireEvent.change(screen.getByLabelText("List entries"), { target: { value: "OFAC_SDN | SDN-7001 | 2 | Volga Maritime Holdings | RU" } });
    fireEvent.click(screen.getByRole("button", { name: "Screen" }));
    expect((await findStatus()).textContent).toBe("Blocked: 1 hit holds this party until an officer decides.");
    expect(calls[0].url.pathname).toBe("/api/v1/trade/screening/check");
    expect(calls[0].body).toEqual({
      subject: { type: "PARTY", id: PARTY, name: "Volga Maritim Holdings" },
      entries: [{ listCode: "OFAC_SDN", entryId: "SDN-7001", version: 2, name: "Volga Maritime Holdings", country: "RU" }],
    });
    const table = await findTable({ name: "Blocking hits" });
    expect(within(table).getByRole("link", { name: "Volga Maritime Holdings" })).toHaveAttribute("href", `/compliance/screening/hits/${HIT}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses a bad form without calling the service and shows service errors", async () => {
    handler = () => problem(422, "TRD-1004", "every list entry needs a list code, an entry id, a version from 1 and a name");
    renderAt("/compliance/screening/check");
    fireEvent.click(screen.getByRole("button", { name: "Screen" }));
    expect(await screen.findByText("Enter the party's or document's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Subject id"), { target: { value: PARTY } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "X" } });
    fireEvent.change(screen.getByLabelText("List entries"), { target: { value: "UN_SC | 1 | 1 | Y" } });
    fireEvent.click(screen.getByRole("button", { name: "Screen" }));
    expect((await findAlert()).textContent).toContain("(TRD-1004)");
  });
});

describe("export check helpers", () => {
  it("parses delivery lines and licenses with decimal strings", () => {
    expect(parseItems("SKU-1 | us_ear | 5a002 | 2 | 1800.00").items).toEqual([
      { itemId: "SKU-1", classification: { regime: "US_EAR", code: "5A002" }, qty: "2", value: "1800.00" },
    ]);
    expect(parseItems("SKU-1 | ITAR | XI | 1 | 1").error).toBe("Line 1: the regime must be US_EAR, EU_DUALUSE or HS.");
    expect(parseItems("SKU-1 | HS | 8471 | 1,5 | 1").error).toBe("Line 1: quantity and value must be numbers such as 2 and 1800.00.");
    expect(parseLicenses("LIC-1 | D123 | US_EAR | us_ear:5a002 | cn, in | 100 | 90 | | | 2027-06-30").licenses).toEqual([
      { licenseId: "LIC-1", number: "D123", regime: "US_EAR", classifications: ["US_EAR:5A002"], destinations: ["CN", "IN"], qtyLimit: "100", usedQty: "90", usedValue: "0", expiresAt: "2027-06-30T23:59:59Z" },
    ]);
    expect(parseLicenses("LIC-1 | | US_EAR | 5A002 | CN | | | | | 2027-06-30").error).toBe("Line 1: list classifications as REGIME:CODE, for example US_EAR:5A002.");
    expect(parseLicenses("LIC-1 | | US_EAR | US_EAR:5A002 | China | | | | | 2027-06-30").error).toBe("Line 1: list destinations as two-letter codes such as CN, IN.");
    expect(parseLicenses("LIC-1 | | US_EAR | US_EAR:5A002 | CN | | | | | 30/06/2027").error).toBe("Line 1: the expiry must be YYYY-MM-DD.");
  });

  it("checks the destination and region", () => {
    const values: ExportValues = { destination: "ua", region: "ua-43", items: "SKU-1 | US_EAR | 5A002 | 1 | 1", licenses: "", releaseRef: " OFAC-GL-25 " };
    expect(toExportInput(values).input).toEqual({
      destination: "UA",
      region: "UA-43",
      items: [{ itemId: "SKU-1", classification: { regime: "US_EAR", code: "5A002" }, qty: "1", value: "1" }],
      licenses: [],
      officerReleaseLicenseRef: "OFAC-GL-25",
    });
    expect(toExportInput({ ...values, region: "RU-MOW" }).errors.region).toBe("Use a region code of the destination, for example UA-43.");
    expect(toExportInput({ ...values, destination: "Ukraine", items: "" }).errors).toEqual({
      destination: "Use a two-letter country code such as CN.",
      region: "Use a region code of the destination, for example UA-43.",
      items: "Enter at least one delivery line.",
    });
  });
});

describe("Export license check", () => {
  it("shows a blocked line with TRD-1003 (AC-48.17)", async () => {
    handler = () => ({
      status: 200,
      body: {
        embargo: false,
        blocked: true,
        items: [{ itemId: "SKU-1", licenseRequired: true, blocked: true, warning: false, code: "TRD-1003", detail: "export license required for SKU-1 (US_EAR:5A002) to CN; none valid" }],
      },
    });
    const { container } = renderAt("/compliance/export-checks");
    expect(screen.getByText("No check yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Destination country"), { target: { value: "cn" } });
    fireEvent.change(screen.getByLabelText("Delivery lines"), { target: { value: "SKU-1 | US_EAR | 5A002 | 2 | 1800.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Check delivery" }));
    expect((await findStatus()).textContent).toBe("1 of 1 line is blocked.");
    expect(calls[0].url.pathname).toBe("/api/v1/trade/export-checks");
    expect(calls[0].body).toEqual({ destination: "CN", items: [{ itemId: "SKU-1", classification: { regime: "US_EAR", code: "5A002" }, qty: "2", value: "1800.00" }], licenses: [] });
    const table = await findTable({ name: "License determination" });
    expect(within(table).getByText("Blocked")).toBeInTheDocument();
    expect(within(table).getByText(/none valid \(TRD-1003\)/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports an embargo and the officer-only refusal", async () => {
    handler = () => ({ status: 200, body: { embargo: true, blocked: true, items: [{ itemId: "SKU-1", licenseRequired: true, blocked: true, warning: false, code: "TRD-1002", detail: "destination UA is under embargo; delivery blocked" }] } });
    renderAt("/compliance/export-checks");
    fireEvent.change(screen.getByLabelText("Destination country"), { target: { value: "UA" } });
    fireEvent.change(screen.getByLabelText(/^Destination region/), { target: { value: "UA-43" } });
    fireEvent.change(screen.getByLabelText("Delivery lines"), { target: { value: "SKU-1 | HS | 8471.30 | 1 | 10" } });
    fireEvent.click(screen.getByRole("button", { name: "Check delivery" }));
    expect((await findStatus()).textContent).toBe("The destination is under embargo; the delivery is blocked (TRD-1002).");

    handler = () => problem(403, "AUTH-0003", "missing duty TradeComplianceOfficer: only a trade compliance officer can release an embargoed delivery");
    fireEvent.change(screen.getByLabelText(/^Embargo release license reference/), { target: { value: "OFAC-GL-25" } });
    fireEvent.click(screen.getByRole("button", { name: "Check delivery" }));
    expect((await findAlert()).textContent).toContain("only a trade compliance officer can release an embargoed delivery (AUTH-0003)");
    expect(calls[1].body).toMatchObject({ officerReleaseLicenseRef: "OFAC-GL-25" });
  });
});
