import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Andon, Downtime, Machine, OeeBucket, PayExport } from "./api/client";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import shopfloor from "./module";
import { validateMachine } from "./NewMachinePage";
import { dayWindow } from "./OeePage";

const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";
const SITE = "0195f0a0-0000-7000-8000-000000000011";
const DOWN = "0195f0a0-0000-7000-8000-000000000012";
const BATCH = "0195f0a0-0000-7000-8000-000000000013";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

const isExtra = (url: URL) => url.pathname.endsWith("/engineering/resources") || url.pathname.includes("/production/orders");
function extra(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/engineering/resources")) return { status: 200, body: { items: [{ id: RESOURCE, code: "MIX-1", name: "Mixer" }] } };
  if (url.pathname.endsWith("/production/orders")) return { status: 200, body: { items: [{ id: ORDER, orderNumber: "MO-77" }] } };
  if (url.pathname.endsWith(`/production/orders/${ORDER}`)) return { status: 200, body: { id: ORDER, orderNumber: "MO-77", productId: "" } };
  return undefined;
}
const ORDER = "0195f0a0-0000-7000-8000-000000000040";

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  resetResourceCache();
  resetOrderCache();
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      if (!isExtra(url)) calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? extra(url) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
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
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const pick = async (label: string, value: string) => {
  const select = screen.getByLabelText(label);
  await waitFor(() => expect(select.querySelector(`option[value="${value}"]`)).not.toBeNull());
  fireEvent.change(select, { target: { value } });
};

const machine: Machine = {
  id: "0195f0a0-0000-7000-8000-000000000020",
  machineId: "M-101",
  resourceId: RESOURCE,
  siteId: SITE,
  idealCycleSeconds: "60",
  idleThresholdSeconds: 300,
  lastSeq: 184233,
  createdAt: "2026-10-20T08:00:00Z",
};

const downtime = (extra: Partial<Downtime> = {}): Downtime => ({
  id: DOWN,
  machineId: machine.id,
  resourceId: RESOURCE,
  startedAt: "2026-10-21T08:10:00Z",
  endedAt: null,
  reasonCode: null,
  category: "unplanned",
  cause: "down",
  andonRaised: true,
  ...extra,
});

describe("Machines", () => {
  it("lists machines with their resource, cycle time and last sequence", async () => {
    handler = (url) => (url.pathname.endsWith("/machines") ? json(200, { items: [machine] }) : undefined);
    const { container } = renderAt("/shopfloor/machines");
    const table = await findTable({ name: "Machines" });
    await waitFor(() => expect(table).toHaveTextContent("M-101"));
    expect(table).toHaveTextContent("60 s");
    expect(table).toHaveTextContent("300 s");
    expect(table).toHaveTextContent("184233");
    expect(screen.getByRole("link", { name: "Register machine" })).toHaveAttribute("href", "/shopfloor/machines/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/machines");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No machines yet")).toBeInTheDocument();
  });

  it("validates a machine the way the service does", () => {
    const base = { machineId: " M-101 ", resourceId: RESOURCE, siteId: "", idealCycleSeconds: "1.5" };
    expect(validateMachine(base).input).toEqual({ machineId: "M-101", resourceId: RESOURCE, siteId: null, idealCycleSeconds: "1.5" });
    for (const cycle of ["0", "0.000", "-1", "86401", "1.2345", "abc"]) {
      expect(validateMachine({ ...base, idealCycleSeconds: cycle }).errors.idealCycleSeconds).toBeDefined();
    }
    const { errors } = validateMachine({ machineId: "", resourceId: "x", siteId: "y", idealCycleSeconds: "60" });
    expect(Object.keys(errors).sort()).toEqual(["machineId", "resourceId", "siteId"]);
  });

  it("registers a machine with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/machines")) return json(201, machine);
      if (url.pathname.endsWith("/machines")) return json(200, { items: [machine] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/machines/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Machine code", "M-101");
    await pick("Work center", RESOURCE);
    fill("Ideal cycle time (seconds)", "60");
    fireEvent.click(screen.getByRole("button", { name: "Register machine" }));
    expect(await findTable({ name: "Machines" })).toBeInTheDocument();
    const [create] = posts("/machines");
    expect(create.body).toEqual({ machineId: "M-101", resourceId: RESOURCE, siteId: null, idealCycleSeconds: "60" });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("puts an already registered code on the field and sends nothing for an invalid form", async () => {
    renderAt("/shopfloor/machines/new");
    fireEvent.click(screen.getByRole("button", { name: "Register machine" }));
    expect(await screen.findByText("Choose the work center.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    handler = () => json(409, { code: "SF-0109", detail: "machine code or resource is already registered" });
    fill("Machine code", "M-101");
    await pick("Work center", RESOURCE);
    fill("Ideal cycle time (seconds)", "60");
    fireEvent.click(screen.getByRole("button", { name: "Register machine" }));
    expect(await screen.findByText("This machine code or resource is already registered.")).toBeInTheDocument();
  });
});

describe("Downtimes", () => {
  it("labels idle time found by the sweep as Idle, not Down", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [] });
      if (url.pathname.endsWith("/downtimes")) return json(200, { items: [downtime({ cause: "idle", andonRaised: false })] });
      return undefined;
    };
    renderAt("/shopfloor/downtimes");
    const table = await findTable({ name: "Downtimes" });
    expect(within(table).getByText("Idle")).toBeInTheDocument();
    expect(within(table).queryByText("Down")).not.toBeInTheDocument();
  });

  it("lists open downtimes and codes a reason with an Idempotency-Key", async () => {
    let coded = false;
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/downtimes/${DOWN}:code`)) {
        coded = true;
        return json(200, downtime({ reasonCode: "JAM", category: "planned" }));
      }
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [] });
      if (url.pathname.endsWith("/downtimes")) return json(200, { items: coded ? [] : [downtime()] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/downtimes");
    const table = await findTable({ name: "Downtimes" });
    await waitFor(() => expect(table).toHaveTextContent("Not coded"));
    expect(calls[0].url.search).toBe("?open=true");
    expect(within(table).getByText("Raised")).toBeInTheDocument();

    fireEvent.click(within(table).getByRole("button", { name: /Code reason for downtime/ }));
    await screen.findByLabelText("Reason code");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText("Enter a reason code of up to 32 characters.")).toBeInTheDocument();
    expect(posts(":code")).toHaveLength(0);

    fill("Reason code", " JAM ");
    fill("Category", "planned");
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/is coded JAM/)).toBeInTheDocument();
    const [code] = posts(":code");
    expect(code.body).toEqual({ reasonCode: "JAM", category: "planned" });
    expect(header(code, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(await screen.findByText("No machine is down")).toBeInTheDocument();
  });

  it("shows a refused coding next to the form", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(404, { code: "SF-0110", detail: "downtime not found" });
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [] });
      return json(200, { items: [downtime({ endedAt: "2026-10-21T08:32:00Z", reasonCode: "JAM" })] });
    };
    renderAt(`/shopfloor/downtimes?open=false&downtime=${DOWN}`);
    expect(await screen.findByRole("heading", { name: /Code downtime from/ })).toBeInTheDocument();
    expect(await screen.findByLabelText("Reason code")).toHaveValue("JAM");
    expect(calls[0].url.search).toBe("");
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/downtime not found/)).toBeInTheDocument();
  });
});

describe("Andon board", () => {
  const alert: Andon = {
    id: "0195f0a0-0000-7000-8000-000000000030",
    resourceId: RESOURCE,
    siteId: SITE,
    kind: "downtime",
    severity: "warning",
    downtimeId: DOWN,
    jobId: null,
    detail: null,
    startedAt: "2026-10-21T08:10:00Z",
  };

  it("lists open alerts with a link to code the downtime", async () => {
    handler = (url) => (url.pathname.endsWith("/andon") ? json(200, { items: [alert] }) : undefined);
    const { container } = renderAt("/shopfloor/andon");
    const table = await findTable({ name: "Open andon alerts" });
    await waitFor(() => expect(table).toHaveTextContent("Machine down over 10 min"));
    expect(within(table).getByRole("link", { name: "Code reason" })).toHaveAttribute("href", `/shopfloor/downtimes?downtime=${DOWN}&open=false`);
    expect(screen.getByRole("list", { name: "Andon summary" })).toHaveTextContent("Open alerts 1");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/andon");
    expect(await screen.findByText("No open alerts")).toBeInTheDocument();
  });
});

describe("OEE", () => {
  const bucket: OeeBucket = {
    resourceId: RESOURCE,
    bucketStart: "2026-10-21T08:00:00Z",
    plannedMinutes: "60",
    runMinutes: "52.5",
    downMinutes: "7.5",
    totalCount: 50,
    goodCount: 48,
    availability: "0.875",
    performance: "1",
    quality: "0.96",
    oee: "0.84",
    overRate: true,
    sourceEvents: 12,
  };

  it("computes a local day window", () => {
    const w = dayWindow("2026-10-21");
    expect(w && Date.parse(w.to) - Date.parse(w.from)).toBe(24 * 3_600_000);
    expect(dayWindow("")).toBeNull();
  });

  it("loads the series for every registered machine and shows ratios as percentages", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/machines")) return json(200, { items: [machine] });
      if (url.pathname.endsWith("/oee")) return json(200, { items: [bucket] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/oee");
    const table = await findTable({ name: "OEE buckets" });
    await waitFor(() => expect(table).toHaveTextContent("84.0 %"));
    expect(table).toHaveTextContent("M-101");
    expect(table).toHaveTextContent("87.5 %");
    expect(table).toHaveTextContent("100.0 % capped");
    expect(table).toHaveTextContent("48 / 50");
    const oee = calls.find((c) => c.url.pathname.endsWith("/oee"));
    expect(oee?.url.searchParams.get("resourceIds")).toBe(RESOURCE);
    expect(oee?.url.searchParams.get("bucket")).toBe("15m");
    expect(screen.getByRole("list", { name: "Day totals" })).toHaveTextContent("Down minutes 7.5Good units 48Rejected units 2");
    expect(pageViolations(container)).toEqual([]);
  });

  it("points to machine setup when no machine is registered", async () => {
    handler = (url) => (url.pathname.endsWith("/machines") ? json(200, { items: [] }) : undefined);
    renderAt("/shopfloor/oee");
    expect(await screen.findByText("No machines registered")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith("/oee"))).toBe(false);
  });

  it("shows an OEE load error", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/machines")) return json(200, { items: [machine] });
      return json(400, { code: "COM-0001", detail: "request failed validation" });
    };
    renderAt("/shopfloor/oee");
    expect(await screen.findByText(/request failed validation/)).toBeInTheDocument();
  });
});

describe("Payroll export", () => {
  const batch: PayExport = {
    batchId: BATCH,
    fromDate: "2026-10-19",
    toDate: "2026-10-25",
    lineCount: 2,
    createdBy: "payroll-1",
    createdAt: "2026-10-26T09:00:00Z",
  };
  const csv = `batchId,workerId,workDate,payType,hours,multiplier,amount,currency\n${BATCH},w-1,2026-10-20,base,8.00,1.000000,160.000000,USD\n`;

  it("lists batches and exports a range into a new batch with its CSV", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/pay:export")) return json(201, { ...batch, csv });
      if (url.pathname.endsWith(`/pay-exports/${BATCH}`)) return json(200, { ...batch, csv });
      if (url.pathname.endsWith("/pay-exports")) return json(200, { items: [batch] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/pay-exports");
    const table = await findTable({ name: "Export batches" });
    await waitFor(() => expect(table).toHaveTextContent("2026-10-19 to 2026-10-25"));
    expect(pageViolations(container)).toEqual([]);

    fill("From", "2026-10-19");
    fill("To", "2026-10-25");
    fireEvent.click(screen.getByRole("button", { name: "Export approved pay" }));
    expect(await screen.findByRole("heading", { name: "Export batch" })).toBeInTheDocument();
    const [exported] = posts("/pay:export");
    expect(exported.body).toEqual({ from: "2026-10-19", to: "2026-10-25" });
    expect(header(exported, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(await screen.findByLabelText("Payroll CSV")).toHaveTextContent("base,8.00,1.000000,160.000000,USD");
    expect(screen.getByRole("link", { name: "Download CSV" })).toHaveAttribute("download", `payroll-${BATCH}.csv`);
  });

  it("explains an empty export and refuses a range over 31 days", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return json(422, { code: "SF-0111", detail: "no approved pay lines left to export in the range" });
      return json(200, { items: [] });
    };
    renderAt("/shopfloor/pay-exports");
    expect(await screen.findByText("No exports yet")).toBeInTheDocument();
    fill("From", "2026-10-19");
    fill("To", "2026-10-25");
    fireEvent.click(screen.getByRole("button", { name: "Export approved pay" }));
    expect(await screen.findByText(/already in a batch, or no day in the range is approved yet/)).toBeInTheDocument();

    fill("To", "2026-11-25");
    expect(screen.getByText("Use at most 31 days.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export approved pay" })).toBeDisabled();
  });

  it("shows a missing batch", async () => {
    handler = () => json(404, { code: "SF-0112", detail: "export batch not found" });
    renderAt(`/shopfloor/pay-exports/${BATCH}`);
    expect(await screen.findByText(/export batch not found/)).toBeInTheDocument();
  });
});
