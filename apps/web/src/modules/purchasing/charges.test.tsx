import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const ID = "44444444-4444-4444-8444-444444444444";
const AUTO = "55555555-5555-4555-8555-555555555555";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const code = {
  id: ID,
  legalEntityId: LE,
  code: "FREIGHT",
  name: "Freight",
  calc: "FIXED",
  defaultValue: "40",
  debitType: "ITEM",
  creditType: "VENDOR",
  allocationRule: "QTY",
  status: "ACTIVE",
  version: 1,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};

const auto = {
  id: AUTO,
  legalEntityId: LE,
  chargeCodeId: ID,
  accountCode: "VENDOR",
  accountId: VENDOR,
  minOrderAmount: "500",
  currency: "USD",
  validFrom: "2026-03-01",
  status: "ACTIVE",
  version: 1,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};

describe("charge codes list", () => {
  it("shows a loading table, then the codes, and opens one", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const { router } = renderAt("/purchasing/charge-codes");
    expect(await screen.findByRole("table", { name: "Charge codes" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [code] }));
    const row = await screen.findByText("FREIGHT");
    expect(screen.getByText("40 fixed")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/charge-codes/${ID}`));
  });

  it("explains the empty state without accessibility violations", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/charge-codes");
    expect(await screen.findByText("No charge codes yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem code and retries", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/charge-codes");
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
  });

  it("filters by status and text on the server and loads the next page", async () => {
    const second = { ...code, id: "66666666-6666-4666-8666-666666666666", code: "HANDLING", name: "Handling" };
    const mock = stubFetch((url) => (String(url).includes("cursor=") ? json({ items: [second] }) : json({ items: [code], nextCursor: ID })));
    renderAt("/purchasing/charge-codes");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("HANDLING")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "INACTIVE" } });
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("status=INACTIVE"))).toBe(true));
    fireEvent.change(document.getElementById("cc-text-filter") as HTMLInputElement, { target: { value: "fre" } });
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("q=fre"))).toBe(true));
  });
});

describe("charge code create", () => {
  it("validates before sending, including the account a LEDGER side needs", async () => {
    const mock = stubFetch(() => json({}));
    const { container } = renderAt("/purchasing/charge-codes/new");
    fireEvent.change(await screen.findByLabelText("Debit side"), { target: { value: "LEDGER" } });
    fireEvent.click(screen.getByRole("button", { name: "Save charge code" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Use 1 to 20 letters, digits, _ or -.")).toBeInTheDocument();
    expect(screen.getByText("Enter the name.")).toBeInTheDocument();
    expect(screen.getByText("Enter the debit account id (a UUID).")).toBeInTheDocument();
    expect(mock).not.toHaveBeenCalled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks for a ledger credit with a vendor debit and sends a vendor only with a vendor side", async () => {
    const mock = stubFetch(() => json(code, 201));
    const { router } = renderAt("/purchasing/charge-codes/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "REBILL" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Rebill" } });
    fireEvent.change(screen.getByLabelText("Debit side"), { target: { value: "VENDOR" } });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Save charge code" }));
    expect(await screen.findByText("A vendor debit needs a ledger credit.")).toBeInTheDocument();
    expect(mock).not.toHaveBeenCalled();
    // Back to an item debit and a ledger credit: no vendor side, so the vendor field goes and nothing is sent for it.
    fireEvent.change(screen.getByLabelText("Debit side"), { target: { value: "ITEM" } });
    fireEvent.change(screen.getByLabelText("Credit side"), { target: { value: "LEDGER" } });
    expect(screen.queryByLabelText("Vendor id")).toBeNull();
    fireEvent.change(screen.getByLabelText("Credit account id"), { target: { value: AUTO } });
    fireEvent.click(screen.getByRole("button", { name: "Save charge code" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/charge-codes/${ID}`));
    const sent = JSON.parse((mock.mock.calls[0][1] as RequestInit).body as string);
    expect(sent).toMatchObject({ debitType: "ITEM", creditType: "LEDGER", creditAccountId: AUTO });
    expect(sent.vendorId).toBeUndefined();
  });

  it("posts the code with an Idempotency-Key and opens it", async () => {
    const mock = stubFetch(() => json(code, 201, { ETag: '"1"' }));
    const { router } = renderAt("/purchasing/charge-codes/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "freight" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Freight " } });
    fireEvent.change(screen.getByLabelText("Default value"), { target: { value: "40" } });
    fireEvent.change(screen.getByLabelText("Allocated to lines by"), { target: { value: "QTY" } });
    fireEvent.click(screen.getByRole("button", { name: "Save charge code" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/charge-codes/${ID}`));
    const [url, init] = mock.mock.calls[0];
    expect(url).toBe(`${BASE}/charge-codes`);
    expect((init as RequestInit).headers).toHaveProperty("Idempotency-Key");
    expect(JSON.parse((init as RequestInit).body as string)).toMatchObject({
      legalEntityId: LE,
      code: "FREIGHT",
      name: "Freight",
      calc: "FIXED",
      defaultValue: "40",
      debitType: "ITEM",
      creditType: "VENDOR",
      allocationRule: "QTY",
    });
  });

  it("shows a duplicate code as a problem with its code", async () => {
    stubFetch(() => problem(409, "PRC-CHG-001", "the legal entity already has a charge code FREIGHT"));
    renderAt("/purchasing/charge-codes/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "FREIGHT" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Freight" } });
    fireEvent.click(screen.getByRole("button", { name: "Save charge code" }));
    expect(await screen.findByText(/already has a charge code.*PRC-CHG-001/)).toBeInTheDocument();
  });
});

describe("charge code detail", () => {
  it("changes the code under If-Match and lists the auto charges that use it", async () => {
    const mock = stubFetch((url, init) => {
      if (init.method === "PUT") return json({ ...code, name: "Inbound freight", version: 2 }, 200, { ETag: '"2"' });
      if (String(url).includes("/auto-charges")) return json({ items: [auto] });
      return json(code, 200, { ETag: '"1"' });
    });
    const { container } = renderAt(`/purchasing/charge-codes/${ID}`);
    expect(await screen.findByRole("heading", { name: "Charge code FREIGHT" })).toBeInTheDocument();
    expect(await screen.findByText("Vendor 22222222")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Inbound freight" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "INACTIVE" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Charge code saved as version 2.")).toBeInTheDocument();
    const put = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "PUT")!;
    expect(put[0]).toBe(`${BASE}/charge-codes/${ID}`);
    expect((put[1] as RequestInit).headers).toMatchObject({ "If-Match": '"1"' });
    expect(JSON.parse((put[1] as RequestInit).body as string)).toMatchObject({ name: "Inbound freight", status: "INACTIVE" });
  });

  it("explains a stale version and reloads on request", async () => {
    let reads = 0;
    stubFetch((url, init) => {
      if (init.method === "PUT") return problem(412, "PRC-CHG-002", "charge code is at version 3, not 1");
      if (String(url).includes("/auto-charges")) return json({ items: [] });
      reads += 1;
      return reads === 1 ? json(code) : json({ ...code, name: "Someone's name", version: 3 });
    });
    renderAt(`/purchasing/charge-codes/${ID}`);
    await screen.findByRole("heading", { name: "Charge code FREIGHT" });
    expect(await screen.findByText("No auto charges")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Someone else changed this charge code.*PRC-CHG-002/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload charge code" }));
    expect(await screen.findByDisplayValue("Someone's name")).toBeInTheDocument();
    expect(screen.queryByText(/PRC-CHG-002/)).toBeNull();
  });

  it("shows not-found as a load failure", async () => {
    stubFetch(() => problem(404, "PRC-CHG-NOT-FOUND", "charge code not found"));
    renderAt(`/purchasing/charge-codes/${ID}`);
    expect(await screen.findAllByText(/PRC-CHG-NOT-FOUND/)).not.toHaveLength(0);
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });
});

describe("auto charges", () => {
  it("lists them with the charge code, empty and error states, and opens one", async () => {
    stubFetch((url) => {
      if (String(url).endsWith(`/auto-charges/${AUTO}`)) return json(auto);
      if (String(url).includes("/charge-codes")) return json(String(url).includes(`/charge-codes/${ID}`) ? code : { items: [code] });
      return json({ items: [auto] });
    });
    const { router } = renderAt("/purchasing/auto-charges");
    const row = await screen.findByText("Vendor 22222222");
    expect(await screen.findByText("FREIGHT")).toBeInTheDocument();
    expect(screen.getByText("500 USD")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/auto-charges/${AUTO}`));
  });

  it("explains the empty state and a failure", async () => {
    stubFetch(() => json({ items: [] }));
    const { container, unmount } = renderAt("/purchasing/auto-charges");
    expect(await screen.findByText("No auto charges yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();
    stubFetch(() => problem(500, "COM-0500", "boom"));
    renderAt("/purchasing/auto-charges");
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
  });

  it("requires a currency for an amount of money, then posts the auto charge", async () => {
    const mock = stubFetch((url, init) => {
      if (init.method === "POST") return json(auto, 201, { ETag: '"1"' });
      if (String(url).endsWith(`/auto-charges/${AUTO}`)) return json(auto);
      if (String(url).endsWith(`/charge-codes/${ID}`)) return json(code);
      return json({ items: [code] });
    });
    const { router } = renderAt(`/purchasing/auto-charges/new?chargeCodeId=${ID}`);
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    await screen.findByRole("option", { name: "FREIGHT - Freight" });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Save auto charge" }));
    expect(await screen.findByText("Enter the currency; this charge is an amount of money.")).toBeInTheDocument();
    expect(mock.mock.calls.some(([, i]) => (i as RequestInit).method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Minimum order amount"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Save auto charge" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/auto-charges/${AUTO}`));
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`${BASE}/auto-charges`);
    expect((post[1] as RequestInit).headers).toHaveProperty("Idempotency-Key");
    expect(JSON.parse((post[1] as RequestInit).body as string)).toMatchObject({
      legalEntityId: LE,
      chargeCodeId: ID,
      accountCode: "VENDOR",
      accountId: VENDOR,
      currency: "USD",
      minOrderAmount: "500",
    });
  });

  it("needs no vendor for every-vendor charges", async () => {
    stubFetch(() => json({ items: [{ ...code, calc: "PERCENT", defaultValue: "2" }] }));
    renderAt(`/purchasing/auto-charges/new?chargeCodeId=${ID}`);
    fireEvent.change(await screen.findByLabelText("Applies to"), { target: { value: "ALL" } });
    expect(screen.queryByLabelText("Vendor id")).toBeNull();
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    await screen.findByRole("option", { name: "FREIGHT - Freight" });
    fireEvent.click(screen.getByRole("button", { name: "Save auto charge" }));
    expect(screen.queryByText("Enter the vendor or vendor group id (a UUID).")).toBeNull();
    expect(screen.queryByText("Enter the currency; this charge is an amount of money.")).toBeNull();
  });

  it("changes the window and status under If-Match, and reports a stale version", async () => {
    let put = 0;
    const mock = stubFetch((url, init) => {
      if (init.method === "PUT") {
        put += 1;
        return put === 1 ? json({ ...auto, status: "INACTIVE", version: 2 }, 200, { ETag: '"2"' }) : problem(412, "PRC-CHG-003", "stale");
      }
      if (String(url).includes("/charge-codes/")) return json(code);
      return json(auto, 200, { ETag: '"1"' });
    });
    const { container } = renderAt(`/purchasing/auto-charges/${AUTO}`);
    expect(await screen.findByText("FREIGHT - Freight")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "INACTIVE" } });
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: "2026-12-31" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Auto charge saved as version 2.")).toBeInTheDocument();
    const call = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "PUT")!;
    expect((call[1] as RequestInit).headers).toMatchObject({ "If-Match": '"1"' });
    expect(JSON.parse((call[1] as RequestInit).body as string)).toMatchObject({ status: "INACTIVE", validTo: "2026-12-31", accountCode: "VENDOR" });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Someone else changed this auto charge.*PRC-CHG-003/)).toBeInTheDocument();
  });
});

const po = {
  id: "88888888-8888-4888-8888-888888888888",
  number: "PO-00000001",
  legalEntityId: LE,
  vendorId: VENDOR,
  currency: "USD",
  approvalStatus: "DRAFT",
  documentStatus: "NONE",
  versionNo: 0,
  changeManagement: true,
  subtotal: "50",
  discountTotal: "0",
  chargesTotal: "40",
  taxTotal: "0",
  total: "90",
  rowVersion: 1,
  lines: [],
  charges: [
    { seq: 1, calc: "FIXED", value: "40", amount: "40", chargeCode: "FREIGHT", chargeCodeId: ID, debitType: "ITEM", creditType: "VENDOR", allocationRule: "QTY", auto: true },
  ],
  createdAt: "2026-03-01T10:00:00Z",
};

describe("charges on a purchase order", () => {
  it("picks a charge code and asks for auto charges", async () => {
    const mock = stubFetch((url, init) => {
      if (init.method === "POST") return json(po, 201);
      if (String(url).endsWith(`/purchase-orders/${po.id}`)) return json(po);
      if (String(url).includes("/charge-codes")) return json({ items: [code] });
      return json({ items: [] });
    });
    renderAt("/purchasing/purchase-orders/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Steel" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Unit price"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Add charge" }));
    await screen.findByRole("option", { name: "FREIGHT - Freight" });
    fireEvent.change(screen.getByLabelText("Charge code"), { target: { value: ID } });
    expect(screen.getByLabelText("Value")).toHaveValue("40");
    expect(screen.getByLabelText("Calculation")).toBeDisabled();
    fireEvent.click(screen.getByLabelText(/Add the charges from auto charges/));
    fireEvent.click(screen.getByRole("button", { name: "Save purchase order" }));
    await waitFor(() => expect(mock.mock.calls.some(([, i]) => (i as RequestInit).method === "POST")).toBe(true));
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    const body = JSON.parse((post[1] as RequestInit).body as string);
    expect(body.charges).toEqual([{ chargeCodeId: ID, value: "40" }]);
    expect(body.applyAutoCharges).toBe(true);
  });

  it("keeps a code on the order that is no longer active and says auto charges are not added twice", async () => {
    const draft = {
      ...po,
      autoChargeCodes: [ID],
      lines: [
        { lineNo: 1, description: "Steel", qty: "5", unit: "EA", unitPrice: "10", priceUnit: "1", discountPct: "0", grossAmount: "50", discountAmount: "0", lineAmount: "50", taxAmount: "0", deliveryDate: "2026-04-01", priceSource: "MANUAL", status: "OPEN" },
      ],
    };
    const mock = stubFetch((url, init) => {
      if (init.method === "PATCH") return json(draft);
      if (String(url).endsWith(`/purchase-orders/${po.id}`)) return json(draft);
      return json({ items: [] });
    });
    renderAt(`/purchasing/purchase-orders/${po.id}/edit`);
    expect(await screen.findByRole("option", { name: "FREIGHT (on this order)" })).toBeInTheDocument();
    expect(screen.getByText(/Auto charges have added a charge code to this order before/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/Add the charges from auto charges/));
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(mock.mock.calls.some(([, i]) => (i as RequestInit).method === "PATCH")).toBe(true));
    const patch = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "PATCH")!;
    const body = JSON.parse((patch[1] as RequestInit).body as string);
    expect(body.charges).toEqual([{ chargeCodeId: ID, value: "40" }]);
    expect(body.applyAutoCharges).toBe(true);
  });

  it("shows the code and sides of each charge on the order", async () => {
    stubFetch((url) => (String(url).endsWith(`/purchase-orders/${po.id}`) ? json(po) : json({ items: [] })));
    renderAt(`/purchasing/purchase-orders/${po.id}`);
    expect(await screen.findByText("FREIGHT (auto)")).toBeInTheDocument();
    expect(screen.getByText("Item / Vendor")).toBeInTheDocument();
    expect(screen.getByText("Qty")).toBeInTheDocument();
  });
});
