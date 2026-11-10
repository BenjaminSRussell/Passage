import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { GateVisit, GateVisitDetail, GoodsInTransit } from "./api/client";
import { validateCheckIn, type CheckInValues } from "./CheckInPage";
import { validateGoodsInTransit } from "./NewGoodsInTransitPage";
import transport from "./module";

const SITE = "0195f0a0-0000-7000-8000-0000000000b1";
const VISIT = "0195f0a0-0000-7000-8000-0000000000a7";
const APPT = "0195f0a0-0000-7000-8000-0000000000a8";
const VOYAGE = "0195f0a0-0000-7000-8000-0000000000f2";
const GIT = "0195f0a0-0000-7000-8000-0000000000f5";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => json(status, { code, detail, status, errors });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {transport.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const visit = (extra: Partial<GateVisit> = {}): GateVisit => ({
  id: VISIT,
  siteId: SITE,
  direction: "OUTBOUND",
  carrierScac: "ABCD",
  trailerNo: "TR-1001",
  sealNo: "778812",
  driverName: "Dana Driver",
  driverLicenseId: "D1234567",
  appointmentId: APPT,
  scheduledAt: "2026-03-02T09:00:00Z",
  walkIn: false,
  arrival: { status: "LATE", deltaMin: 95, code: "TMS-0127" },
  checkedInAt: "2026-03-02T10:35:00Z",
  freeTimeMin: 120,
  status: "IN_YARD",
  version: 2,
  ...extra,
});

const detail = (extra: Partial<GateVisitDetail> = {}): GateVisitDetail => ({
  ...visit(),
  log: [
    { id: "e1", visitId: VISIT, kind: "CHECK_IN", occurredAt: "2026-03-02T10:35:00Z", recordedBy: "guard-1", detail: { arrival: "LATE" } },
    { id: "e2", visitId: VISIT, kind: "SEALED", occurredAt: "2026-03-02T11:00:00Z", recordedBy: "guard-1", detail: { sealNo: "778812" } },
  ],
  ...extra,
});

const order = (extra: Partial<GoodsInTransit> = {}): GoodsInTransit => ({
  id: GIT,
  voyageId: VOYAGE,
  voyageReference: "MAEU-2611E",
  siteId: SITE,
  incoterm: "FOB",
  transferEvent: "ORIGIN_SHIPMENT",
  inTransitWarehouse: `TRANSIT-${SITE}`,
  currency: "USD",
  status: "PENDING",
  lines: [
    { line: 1, value: "100", quantity: "10", costs: [{ costType: "FREIGHT", amount: "333.33" }, { costType: "DUTY", amount: "25" }], estimated: "358.33" },
    { line: 3, value: "250", quantity: "20", costs: [{ costType: "FREIGHT", amount: "333.34" }, { costType: "DUTY", amount: "62.5" }], estimated: "395.84" },
  ],
  goodsValue: "350",
  estimatedTotal: "754.17",
  version: 1,
  createdAt: "2026-03-01T08:00:00Z",
  ...extra,
});

describe("gate log", () => {
  it("lists trucks in the yard with arrival and filters by status, site and carrier", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/gate/visits" ? json(200, { items: [visit(), visit({ id: "v2", trailerNo: "T9", arrival: undefined, walkIn: true, sealNo: undefined })] }) : undefined);
    const { container } = renderAt("/transport/gate");
    const table = await findTable({ name: "Gate visits" });
    expect(within(table).getByText("TR-1001")).toBeInTheDocument();
    expect(within(table).getByText("Late")).toBeInTheDocument();
    expect(within(table).getByText("Walk-in")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("status")).toBe("IN_YARD");
    expect(violations(container)).toEqual([]);

    fill("Carrier SCAC", "abcd");
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("carrierScac")).toBe("ABCD"));
    fill("Site", SITE);
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("siteId")).toBe(SITE));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.has("status")).toBe(false));
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/transport/gate");
    expect(await screen.findByText("No trucks in the yard")).toBeInTheDocument();
    first.unmount();

    handler = () => problem(503, "COM-0503", "transport is down");
    renderAt("/transport/gate");
    expect(await screen.findByText(/transport is down/)).toBeInTheDocument();
    handler = () => json(200, { items: [visit()] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Gate visits" })).toBeInTheDocument();
  });
});

describe("check in", () => {
  const values = (extra: Partial<CheckInValues> = {}): CheckInValues => ({
    siteId: SITE,
    direction: "INBOUND",
    loadId: "",
    carrierScac: "abcd",
    trailerNo: " tr-1001 ",
    sealNo: "",
    driverName: "Dana Driver",
    driverLicenseId: "D1234567",
    appointmentId: "",
    scheduledAt: "",
    arrivedAt: "",
    freeTimeMin: "120",
    toleranceMin: "60",
    ...extra,
  });

  it("validates like the service and sends a walk-in without appointment fields", () => {
    const ok = validateCheckIn(values());
    expect(ok.input).toEqual({ siteId: SITE, direction: "INBOUND", carrierScac: "ABCD", trailerNo: "TR-1001", driverName: "Dana Driver", driverLicenseId: "D1234567", freeTimeMin: 120, toleranceMin: 60 });
    expect(validateCheckIn(values({ carrierScac: "A1" })).errors.carrierScac).toBeDefined();
    expect(validateCheckIn(values({ siteId: "site" })).errors.siteId).toBeDefined();
    expect(validateCheckIn(values({ appointmentId: APPT })).errors.appointmentId).toMatch(/together/);
    expect(validateCheckIn(values({ freeTimeMin: "2000" })).errors.freeTimeMin).toBeDefined();
    expect(validateCheckIn(values({ arrivedAt: "2999-01-01T00:00" })).errors.arrivedAt).toMatch(/future/);
    const appt = validateCheckIn(values({ appointmentId: APPT, scheduledAt: "2026-03-02T09:00" }));
    expect(appt.input?.appointmentId).toBe(APPT);
    expect(appt.input?.scheduledAt).toMatch(/^2026-03-0[12]T\d\d:00:00\.000Z$/);
  });

  it("checks in with one idempotency key and opens the visit", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/gate/check-in")) return json(201, visit());
      if (url.pathname.endsWith(`/gate/visits/${VISIT}`)) return json(200, detail());
      return undefined;
    };
    const { container } = renderAt("/transport/gate/check-in");
    expect(violations(container)).toEqual([]);
    fill("Site", SITE);
    fill("Carrier SCAC", "abcd");
    fill("Trailer or container", "tr-1001");
    fill("Driver name", "Dana Driver");
    fill("Driver license id", "D1234567");
    fireEvent.click(screen.getByRole("button", { name: "Check in" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/transport/gate/${VISIT}`));
    const [call] = posts("/gate/check-in");
    expect(call.body).toMatchObject({ siteId: SITE, carrierScac: "ABCD", trailerNo: "TR-1001", direction: "INBOUND" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("shows field errors from the service and a trailer already in the yard", async () => {
    handler = () => problem(409, "TMS-0118", "trailer TR-1001 of ABCD is already in the yard");
    renderAt("/transport/gate/check-in");
    fill("Site", SITE);
    fill("Carrier SCAC", "ABCD");
    fill("Trailer or container", "TR-1001");
    fill("Driver name", "Dana");
    fill("Driver license id", "L1");
    fireEvent.click(screen.getByRole("button", { name: "Check in" }));
    expect(await screen.findByText(/already in the yard/)).toBeInTheDocument();

    handler = () => problem(400, "COM-0001", "request failed validation", { trailerNo: "is required, up to 20 characters" });
    fireEvent.click(screen.getByRole("button", { name: "Check in" }));
    expect(await screen.findByText("is required, up to 20 characters")).toBeInTheDocument();
  });
});

describe("gate visit", () => {
  it("shows the arrival, seal and gate log", async () => {
    handler = () => json(200, detail());
    const { container } = renderAt(`/transport/gate/${VISIT}`);
    expect(await screen.findByRole("heading", { name: "Trailer TR-1001" })).toBeInTheDocument();
    expect(screen.getByText(/95 min late/)).toBeInTheDocument();
    const log = screen.getByRole("table", { name: "Gate log" });
    expect(within(log).getByText("Sealed")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("blocks a check-out with the wrong seal and reloads the gate log (AC-34.12)", async () => {
    let blocked = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/gate/check-out")) {
        blocked = true;
        return problem(422, "TMS-0129", 'seal number "778813" does not match recorded seal "778812"');
      }
      const log = detail().log;
      return json(200, blocked ? detail({ log: [...log, { id: "e3", visitId: VISIT, kind: "CHECK_OUT_BLOCKED", occurredAt: "2026-03-02T12:00:00Z", recordedBy: "guard-1", detail: { declaredSealNo: "778813" } }] }) : detail());
    };
    renderAt(`/transport/gate/${VISIT}`);
    await screen.findByRole("heading", { name: "Trailer TR-1001" });
    fill("Declared seal", "778813");
    fireEvent.click(screen.getByRole("button", { name: "Check out" }));
    expect(await screen.findByText("Check-out blocked")).toBeInTheDocument();
    expect(await screen.findByText("Check-out blocked", { selector: "span" })).toBeInTheDocument();
    expect(posts("/gate/check-out")[0].body).toEqual({ visitId: VISIT, sealNo: "778813" });
  });

  it("checks out with the detention rate and shows the detention", async () => {
    let out = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/gate/check-out")) {
        out = true;
        return json(200, visit({ status: "CHECKED_OUT", checkedOutAt: "2026-03-02T13:55:00Z", detention: { dwellMin: 200, detentionMin: 80, hours: 2, charge: "170", code: "TMS-0126" } }));
      }
      return json(
        200,
        out ? detail({ status: "CHECKED_OUT", checkedOutAt: "2026-03-02T13:55:00Z", detention: { dwellMin: 200, detentionMin: 80, hours: 2, charge: "170", code: "TMS-0126" } }) : detail(),
      );
    };
    renderAt(`/transport/gate/${VISIT}`);
    await screen.findByRole("heading", { name: "Trailer TR-1001" });
    fill("Declared seal", "778812");
    fill("Detention rate per hour", "85.00");
    fireEvent.click(screen.getByRole("button", { name: "Check out" }));
    expect(await screen.findByText("Checked out. Detention 2 h (TMS-0126).")).toBeInTheDocument();
    expect(await screen.findByText(/80 min over free time, 2 h charged: 170.00/)).toBeInTheDocument();
    const [call] = posts("/gate/check-out");
    expect(call.body).toEqual({ visitId: VISIT, sealNo: "778812", hourlyRate: "85.00" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Check out" })).not.toBeInTheDocument();
  });

  it("records the seal of an outbound load before it can leave", async () => {
    let sealed = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/gate/visits/${VISIT}:seal`)) {
        sealed = true;
        return json(200, visit());
      }
      return json(200, sealed ? detail() : detail({ sealNo: undefined }));
    };
    renderAt(`/transport/gate/${VISIT}`);
    expect(await screen.findByText(/no seal yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record seal" }));
    expect(await screen.findByText("Enter the seal number, up to 20 characters.")).toBeInTheDocument();
    fill("Seal number", "778812");
    fireEvent.click(screen.getByRole("button", { name: "Record seal" }));
    expect(await screen.findByText("Seal 778812 recorded.")).toBeInTheDocument();
    expect(posts(":seal")[0].body).toEqual({ sealNo: "778812" });
  });

  it("shows a load error with a retry", async () => {
    handler = () => problem(404, "TMS-0100", "gate visit not found");
    renderAt(`/transport/gate/${VISIT}`);
    expect(await screen.findByText(/gate visit not found/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("goods in transit", () => {
  it("lists orders for a voyage with their estimated landed cost", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/goods-in-transit" ? json(200, { items: [order()] }) : undefined);
    const { container } = renderAt(`/transport/goods-in-transit?voyageId=${VOYAGE}`);
    const table = await findTable({ name: "Goods-in-transit orders" });
    expect(within(table).getByText("754.17 USD")).toBeInTheDocument();
    expect(within(table).getByText("Pending")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("voyageId")).toBe(VOYAGE);
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "IN_TRANSIT" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("IN_TRANSIT"));
  });

  it("shows the empty and error states", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/transport/goods-in-transit");
    expect(await screen.findByText("No goods-in-transit orders")).toBeInTheDocument();
    first.unmount();
    handler = () => problem(500, "COM-0500", "internal error");
    renderAt("/transport/goods-in-transit");
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
  });

  it("validates the order form", () => {
    expect(validateGoodsInTransit({ voyageId: VOYAGE, incoterm: "FOB", siteId: SITE, receiptLines: "3, 1" }).input).toEqual({
      voyageId: VOYAGE,
      incoterm: "FOB",
      siteId: SITE,
      receiptLines: [3, 1],
    });
    expect(validateGoodsInTransit({ voyageId: VOYAGE, incoterm: "FOB", siteId: SITE, receiptLines: "" }).input?.receiptLines).toBeUndefined();
    expect(validateGoodsInTransit({ voyageId: VOYAGE, incoterm: "DAP", siteId: SITE, receiptLines: "" }).errors.incoterm).toBeDefined();
    expect(validateGoodsInTransit({ voyageId: VOYAGE, incoterm: "FOB", siteId: SITE, receiptLines: "1, 1" }).errors.receiptLines).toMatch(/once/);
    expect(validateGoodsInTransit({ voyageId: VOYAGE, incoterm: "FOB", siteId: SITE, receiptLines: "1, x" }).errors.receiptLines).toBeDefined();
  });

  it("opens an order from a voyage and shows a line already in transit", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return problem(409, "TMS-0119", "receipt line 1 of voyage MAEU-2611E is already on goods-in-transit order x");
      return undefined;
    };
    renderAt(`/transport/goods-in-transit/new?voyageId=${VOYAGE}`);
    expect(screen.getByLabelText("Voyage id")).toHaveValue(VOYAGE);
    fill("Destination site", SITE);
    fill("Receipt lines", "1");
    fireEvent.click(screen.getByRole("button", { name: "Open order" }));
    expect(await screen.findByText(/already on goods-in-transit order x/)).toBeInTheDocument();

    handler = (_url, init) => {
      if (init.method === "POST") return json(201, order());
      return json(200, order());
    };
    fill("Receipt lines", "1, 3");
    fireEvent.click(screen.getByRole("button", { name: "Open order" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/transport/goods-in-transit/${GIT}`));
    const sent = posts("/goods-in-transit");
    expect(sent.at(-1)?.body).toEqual({ voyageId: VOYAGE, incoterm: "FOB", siteId: SITE, receiptLines: [1, 3] });
    expect(header(sent[0], "Idempotency-Key")).not.toBe(header(sent[1], "Idempotency-Key"));
  });

  it("ships an order and shows ownership moved (TMS-0121)", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":ship")) return json(200, order({ status: "IN_TRANSIT", shippedAt: "2026-03-02T06:00:00Z", code: "TMS-0121", version: 2 }));
      return json(200, order());
    };
    const { container } = renderAt(`/transport/goods-in-transit/${GIT}`);
    const lines = await findTable({ name: "Receipt lines in transit" });
    expect(within(lines).getByText("DUTY")).toBeInTheDocument();
    expect(within(lines).getByText("62.50")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Ship" }));
    expect(await screen.findByText(/Ownership moved to TRANSIT-.* \(FOB, TMS-0121\)/)).toBeInTheDocument();
    expect(posts(":ship")[0].body).toEqual({});
    expect(screen.queryByRole("button", { name: "Ship" })).not.toBeInTheDocument();
  });
});
