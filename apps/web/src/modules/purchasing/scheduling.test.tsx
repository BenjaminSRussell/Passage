import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { reduces, toScheduleInput, validateSchedule, type RowDraft } from "./SchedulingPages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PRODUCT = "33333333-3333-4333-8333-333333333333";
const SITE = "44444444-4444-4444-8444-444444444444";
const SA_ID = "99999999-9999-4999-8999-999999999999";
const BASE = "/api/v1/procurement/scheduling-agreements";

afterEach(() => vi.unstubAllGlobals());

const agreement = {
  id: SA_ID,
  number: "SA-00000003",
  legalEntityId: LE,
  vendorId: VENDOR,
  productId: PRODUCT,
  siteId: SITE,
  currency: "USD",
  unit: "EA",
  unitPrice: "2.5",
  targetQty: "1200",
  scheduledQty: "300",
  receivedQty: "40",
  validFrom: "2027-01-01",
  profile: { frcHorizonDays: 90, jitHorizonDays: 14, firmZoneDays: 14, tradeoffZoneDays: 28, releaseFrequency: "WEEKLY" },
  zonesAsOf: "2027-01-04",
  version: 3,
  lines: [
    { lineNo: 1, deliveryDate: "2027-01-11", zone: "FIRM", qty: "100", receivedQty: "40", openQty: "60" },
    { lineNo: 2, deliveryDate: "2027-01-25", zone: "TRADEOFF", qty: "100", receivedQty: "0", openQty: "100" },
    { lineNo: 3, deliveryDate: "2027-03-01", zone: "FORECAST", qty: "100", receivedQty: "0", openQty: "100", reductionReason: "VENDOR_CAPACITY" },
  ],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

const release = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  agreementId: SA_ID,
  releaseNo: 1,
  number: "REL-SA-00000003-1",
  kind: "JIT",
  runDate: "2027-01-04",
  agreementVersion: 3,
  cumulativeReset: false,
  cumulativeReleased: "300",
  cumulativeReceived: "40",
};

const releaseDoc = {
  ...release,
  lines: [
    { lineNo: 1, scheduleLineNo: 1, deliveryDate: "2027-01-11", zone: "FIRM", inHorizon: true, qty: "100", cumulativeReleased: "100", cumulativeReceived: "40" },
    { lineNo: 2, scheduleLineNo: 2, deliveryDate: "2027-01-25", zone: "TRADEOFF", inHorizon: false, qty: "100", cumulativeReleased: "200", cumulativeReceived: "40" },
  ],
};

/** Serves the agreement, its releases and release documentation; other calls go to onWrite. */
function serve(onWrite: (url: string, init: RequestInit) => Response = () => json({}), releases: unknown[] = [release]) {
  return stubFetch((url, init) => {
    if (init.method === "PUT" || init.method === "POST") return onWrite(url, init);
    if (url === `${BASE}/${SA_ID}`) return json(agreement, 200, { ETag: '"3"' });
    if (url === `${BASE}/${SA_ID}/releases`) return json({ items: releases });
    if (url === `${BASE}/${SA_ID}/releases/1`) return json(releaseDoc);
    return problem(404, "COM-0404", `unexpected ${url}`);
  });
}

describe("scheduling agreement monitor", () => {
  it("shows a loading table, then open quantity, last release and next due, and opens the agreement", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const { router, container } = renderAt("/purchasing/scheduling-agreements");
    expect(await screen.findByRole("table", { name: "Scheduling agreements" })).toHaveAttribute("aria-busy", "true");
    resolve(
      json({
        items: [
          {
            id: SA_ID,
            number: "SA-00000003",
            legalEntityId: LE,
            vendorId: VENDOR,
            productId: PRODUCT,
            siteId: SITE,
            unit: "EA",
            validFrom: "2027-01-01",
            releaseFrequency: "WEEKLY",
            scheduledQty: "300",
            receivedQty: "40",
            openQty: "260",
            releaseCount: 1,
            lastReleaseNumber: "REL-SA-00000003-1",
            lastReleaseKind: "JIT",
            lastRunDate: "2027-01-04",
            nextReleaseDue: "2027-01-11",
            version: 3,
          },
        ],
      }),
    );
    const table = await findTable({ name: "Scheduling agreements" });
    await waitFor(() => expect(within(table).getByText("SA-00000003")).toBeInTheDocument());
    const row = within(table).getAllByRole("row")[1];
    expect(row).toHaveTextContent("260 EA");
    expect(row).toHaveTextContent("REL-SA-00000003-1 (2027-01-04)");
    expect(row).toHaveTextContent("2027-01-11, weekly");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "SA-00000003" }));
    expect(router.state.location.pathname).toBe(`/purchasing/scheduling-agreements/${SA_ID}`);
  });

  it("explains an empty monitor", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/scheduling-agreements");
    expect(await screen.findByRole("heading", { name: "No scheduling agreements yet" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New scheduling agreement" })).toHaveAttribute("href", "/purchasing/scheduling-agreements/new");
  });

  it("shows the problem code on error and retries", async () => {
    const fetchMock = stubFetch(() => problem(503, "COM-0503", "service unavailable"));
    renderAt("/purchasing/scheduling-agreements");
    expect(await screen.findByText(/service unavailable/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});

describe("new scheduling agreement", () => {
  it("validates before calling the service", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/scheduling-agreements/new");
    fireEvent.change(await screen.findByLabelText("Firm zone (days)"), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Save scheduling agreement" }));
    expect(await screen.findByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the site id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the agreed price per unit.")).toBeInTheDocument();
    expect(screen.getByText("The trade-off zone cannot end before the firm zone.")).toBeInTheDocument();
    expect(screen.getByText("Enter a positive quantity.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("posts the agreement, profile and schedule with an Idempotency-Key and opens it", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(agreement, 201, { ETag: '"1"' }) : json(agreement)));
    const { router } = renderAt("/purchasing/scheduling-agreements/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product id"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Site id"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Unit price"), { target: { value: "2.50" } });
    fireEvent.change(screen.getByLabelText("Valid from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Release frequency"), { target: { value: "DAILY" } });
    fireEvent.change(screen.getByLabelText("Firm zone (days)"), { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText("Delivery date"), { target: { value: "2027-01-11" } });
    fireEvent.change(screen.getByLabelText("Quantity (EA)"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "Add delivery" }));
    const second = screen.getByRole("group", { name: "Delivery 2" });
    fireEvent.change(within(second).getByLabelText("Delivery date"), { target: { value: "2027-01-18" } });
    fireEvent.change(within(second).getByLabelText("Quantity (EA)"), { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Save scheduling agreement" }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/scheduling-agreements/${SA_ID}`));
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(BASE);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    expect(JSON.parse(String(init.body))).toEqual({
      legalEntityId: LE,
      vendorId: VENDOR,
      productId: PRODUCT,
      siteId: SITE,
      currency: "USD",
      unit: "EA",
      unitPrice: "2.50",
      validFrom: "2027-01-01",
      profile: { frcHorizonDays: 90, jitHorizonDays: 14, firmZoneDays: 7, tradeoffZoneDays: 28, releaseFrequency: "DAILY" },
      lines: [
        { deliveryDate: "2027-01-11", qty: "100" },
        { deliveryDate: "2027-01-18", qty: "120" },
      ],
    });
  });

  it("maps a service field error back onto the form", async () => {
    stubFetch(() => problem(422, "COM-0001", "lines[0].qty must be greater than zero", { "lines[0].qty": "must be greater than zero" }));
    renderAt("/purchasing/scheduling-agreements/new");
    for (const [label, value] of [
      ["Legal entity id", LE],
      ["Vendor id", VENDOR],
      ["Product id", PRODUCT],
      ["Site id", SITE],
      ["Unit price", "1"],
      ["Delivery date", "2099-01-01"],
      ["Quantity (EA)", "5"],
    ]) {
      fireEvent.change(await screen.findByLabelText(label), { target: { value: value } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Save scheduling agreement" }));
    expect(await screen.findByText("must be greater than zero")).toBeInTheDocument();
    expect(screen.getAllByRole("alert").some((a) => a.textContent?.includes("COM-0001"))).toBe(true);
  });
});

describe("scheduling agreement detail", () => {
  it("shows the summary, the schedule with zones and the releases", async () => {
    serve();
    const { container } = renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    expect(await screen.findByRole("heading", { name: "Scheduling agreement SA-00000003" })).toBeInTheDocument();
    expect(screen.getByText("2.5 USD per EA")).toBeInTheDocument();
    const schedule = screen.getByRole("table", { name: "Schedule (zones as of 2027-01-04)" });
    const rows = within(schedule).getAllByRole("row");
    expect(within(rows[1]).getByText("Firm")).toBeInTheDocument();
    expect(rows[1]).toHaveTextContent("60 EA");
    expect(within(rows[2]).getByText("Trade-off")).toBeInTheDocument();
    expect(rows[3]).toHaveTextContent("VENDOR_CAPACITY");
    const releases = await findTable({ name: "Releases" });
    await waitFor(() => expect(within(releases).getByRole("link", { name: "REL-SA-00000003-1" })).toHaveAttribute("href", `/purchasing/scheduling-agreements/${SA_ID}/releases/1`));
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks for a reason before a firm line is reduced, then saves with If-Match and shows trade-off warnings", async () => {
    const fetchMock = serve(() =>
      json({ ...agreement, version: 4, warnings: ["schedule line 2 (2027-01-25) was reduced inside the trade-off zone"] }, 200, { ETag: '"4"' }),
    );
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit schedule" }));
    const line1 = screen.getByRole("group", { name: "Line 1" });
    fireEvent.change(within(line1).getByLabelText("Quantity (EA)"), { target: { value: "50" } });
    fireEvent.click(within(screen.getByRole("group", { name: "Line 2" })).getByRole("button", { name: "Remove line 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("This line is in the firm zone: give a reason code to reduce, move or remove it.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit).method === "PUT")).toHaveLength(0);

    fireEvent.change(within(line1).getByLabelText("Reason code"), { target: { value: "VENDOR_CAPACITY" } });
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("Schedule saved as version 4.")).toBeInTheDocument();
    expect(screen.getByText(/reduced inside the trade-off zone/)).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls.find(([, i]) => (i as RequestInit).method === "PUT") as [string, RequestInit];
    expect(url).toBe(`${BASE}/${SA_ID}/schedule`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"3"');
    expect(JSON.parse(String(init.body)).lines).toEqual([
      { lineNo: 1, deliveryDate: "2027-01-11", qty: "50", reason: "VENDOR_CAPACITY" },
      { lineNo: 2, deliveryDate: "2027-01-25", qty: "0" },
      { lineNo: 3, deliveryDate: "2027-03-01", qty: "100" },
    ]);
  });

  it("shows the conflict code when someone else changed the schedule", async () => {
    serve(() => problem(409, "PRC-VERSION-CONFLICT", "scheduling agreement is at version 4, not 3"));
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit schedule" }));
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText(/PRC-VERSION-CONFLICT/)).toBeInTheDocument();
  });

  it("runs a release with an Idempotency-Key and reloads the releases", async () => {
    const second = { ...release, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", releaseNo: 2, number: "REL-SA-00000003-2", kind: "FRC", cumulativeReset: true };
    let releases: unknown[] = [release];
    const fetchMock = stubFetch((url, init) => {
      if (init.method === "POST") {
        releases = [release, second];
        return json({ ...second, lines: [] }, 201);
      }
      if (url === `${BASE}/${SA_ID}/releases`) return json({ items: releases });
      return json(agreement, 200, { ETag: '"3"' });
    });
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    fireEvent.change(await screen.findByLabelText("Release kind"), { target: { value: "FRC" } });
    fireEvent.change(screen.getByLabelText("Run date"), { target: { value: "2027-01-12" } });
    fireEvent.click(screen.getByLabelText(/Record a cumulative reset/));
    fireEvent.click(screen.getByRole("button", { name: "Run release" }));
    expect(await screen.findByText("Release REL-SA-00000003-2 created: cumulative released 300 EA.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST") as [string, RequestInit];
    expect(url).toBe(`${BASE}/${SA_ID}:release`);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(init.body))).toEqual({ kind: "FRC", runDate: "2027-01-12", cumulativeReset: true });
    const table = screen.getByRole("table", { name: "Releases" });
    await waitFor(() => expect(within(table).getByRole("link", { name: "REL-SA-00000003-2" })).toBeInTheDocument());
    expect(within(table).getByText("Cumulative reset")).toBeInTheDocument();
  });

  it("says when an unchanged schedule returned the existing release", async () => {
    serve(() => json(releaseDoc, 200));
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    await screen.findByRole("link", { name: "REL-SA-00000003-1" });
    fireEvent.click(screen.getByRole("button", { name: "Run release" }));
    expect(await screen.findByText("The schedule has not changed since REL-SA-00000003-1; no new release was created.")).toBeInTheDocument();
  });

  it("shows a refused release with its code", async () => {
    serve(() => problem(422, "PRC-SA-003", "cumulative released 1050 cannot decrease from 1200 without a recorded reset"));
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Run release" }));
    expect(await screen.findByText("cumulative released 1050 cannot decrease from 1200 without a recorded reset")).toBeInTheDocument();
    expect(screen.getByText("PRC-SA-003")).toBeInTheDocument();
  });

  it("explains a missing agreement", async () => {
    stubFetch(() => problem(404, "PRC-SA-NOT-FOUND", "scheduling agreement not found"));
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}`);
    expect(await screen.findByRole("heading", { name: "Scheduling agreement not found" })).toBeInTheDocument();
  });
});

describe("release documentation", () => {
  it("lists the release lines with zones, horizon and cumulative quantities", async () => {
    serve();
    const { container } = renderAt(`/purchasing/scheduling-agreements/${SA_ID}/releases/1`);
    expect(await screen.findByRole("heading", { name: "Release REL-SA-00000003-1" })).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Release lines" });
    const rows = within(table).getAllByRole("row");
    await waitFor(() => expect(rows[2]).toHaveTextContent("200 EA"));
    expect(rows[1]).toHaveTextContent("Yes");
    expect(rows[2]).toHaveTextContent("No");
    expect(screen.getByRole("link", { name: "Back to the agreement" })).toHaveAttribute("href", `/purchasing/scheduling-agreements/${SA_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a missing release", async () => {
    stubFetch((url) => (url.endsWith("/releases/7") ? problem(404, "PRC-SA-REL-NOT-FOUND", "no release 7") : json(agreement)));
    renderAt(`/purchasing/scheduling-agreements/${SA_ID}/releases/7`);
    expect(await screen.findByRole("heading", { name: "Release not found" })).toBeInTheDocument();
  });
});

describe("schedule editor rules", () => {
  const saved = agreement.lines[0] as RowDraft["saved"];
  const row = (patch: Partial<RowDraft>): RowDraft => ({ key: "k", lineNo: 1, deliveryDate: "2027-01-11", qty: "100", reason: "", removed: false, saved, ...patch });

  it("treats a lower quantity, a new date or a removal as a reduction", () => {
    expect(reduces(row({}))).toBe(false);
    expect(reduces(row({ qty: "120" }))).toBe(false);
    expect(reduces(row({ qty: "90" }))).toBe(true);
    expect(reduces(row({ deliveryDate: "2027-01-12" }))).toBe(true);
    expect(reduces(row({ removed: true }))).toBe(true);
    expect(reduces(row({ saved: undefined, lineNo: undefined }))).toBe(false);
  });

  it("refuses quantities below receipts and removing a line with receipts", () => {
    expect(validateSchedule([row({ qty: "30", reason: "X" })])["lines[0].qty"]).toBe("40 were already received against this line.");
    expect(validateSchedule([row({ removed: true, reason: "X" }), row({ key: "n", lineNo: undefined, saved: undefined })])["lines[0].qty"]).toBe(
      "This line has receipts and cannot be removed.",
    );
    expect(validateSchedule([row({ removed: true, reason: "X" })]).lines).toBe("Keep at least one delivery.");
  });

  it("sends a removed line as quantity 0 at its saved date and drops discarded new rows", () => {
    const input = toScheduleInput([
      row({ removed: true, deliveryDate: "2027-02-01", reason: " CANCELLED " }),
      { key: "n", deliveryDate: "2027-04-01", qty: "10", reason: "", removed: false },
    ]);
    expect(input).toEqual([
      { lineNo: 1, deliveryDate: "2027-01-11", qty: "0", reason: "CANCELLED" },
      { lineNo: undefined, deliveryDate: "2027-04-01", qty: "10", reason: undefined },
    ]);
  });
});
