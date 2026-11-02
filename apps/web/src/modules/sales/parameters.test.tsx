import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { DUTY_PARAMETER_MAINTAIN, validateParameters } from "./ParametersPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(status === 204 ? null : JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string, duties: string[] = ["sales.order.read", DUTY_PARAMETER_MAINTAIN]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>Elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}


const params = {
  legalEntityId: LE,
  quotationExpiryDays: 30,
  quotationProbabilityDefault: 50,
  creditReleaseValidityDays: 7,
  creditHoldSlaBusinessDays: 2,
  defaultOrderType: "ORDER",
  version: 0,
};

describe("validateParameters", () => {
  const ok = { quotationExpiryDays: "30", quotationProbabilityDefault: "50", creditReleaseValidityDays: "7", creditHoldSlaBusinessDays: "2", defaultOrderType: "ORDER" as const, creditCheckPoints: ["confirm" as const, "release" as const], intakeDedupWindowDays: "30" };
  it("accepts the defaults and returns numbers", () => {
    expect(validateParameters(ok).input).toEqual({ quotationExpiryDays: 30, quotationProbabilityDefault: 50, creditReleaseValidityDays: 7, creditHoldSlaBusinessDays: 2, defaultOrderType: "ORDER", creditCheckPoints: ["confirm", "release"], intakeDedupWindowDays: 30 });
  });
  it("needs the confirm check point and sends the points in service order", () => {
    expect(Object.keys(validateParameters({ ...ok, creditCheckPoints: [] }).errors)).toEqual(["creditCheckPoints"]);
    expect(Object.keys(validateParameters({ ...ok, creditCheckPoints: ["entry", "release"] }).errors)).toEqual(["creditCheckPoints"]);
    expect(validateParameters({ ...ok, creditCheckPoints: ["release", "confirm", "entry"] }).input?.creditCheckPoints).toEqual(["entry", "confirm", "release"]);
  });
  it("accepts the PRD bounds", () => {
    expect(validateParameters({ ...ok, quotationExpiryDays: "365", quotationProbabilityDefault: "0", creditReleaseValidityDays: "30", creditHoldSlaBusinessDays: "10", intakeDedupWindowDays: "90" }).input).toBeDefined();
    expect(validateParameters({ ...ok, intakeDedupWindowDays: "1" }).input?.intakeDedupWindowDays).toBe(1);
  });
  it("refuses values outside the service's ranges", () => {
    const { errors, input } = validateParameters({ ...ok, quotationExpiryDays: "366", quotationProbabilityDefault: "101", creditReleaseValidityDays: "31", creditHoldSlaBusinessDays: "11", intakeDedupWindowDays: "91" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["creditHoldSlaBusinessDays", "creditReleaseValidityDays", "intakeDedupWindowDays", "quotationExpiryDays", "quotationProbabilityDefault"]);
    expect(Object.keys(validateParameters({ ...ok, intakeDedupWindowDays: "0" }).errors)).toEqual(["intakeDedupWindowDays"]);
    expect(Object.keys(validateParameters({ ...ok, creditHoldSlaBusinessDays: "x", creditReleaseValidityDays: "0" }).errors).sort()).toEqual(["creditHoldSlaBusinessDays", "creditReleaseValidityDays"]);
  });
});

describe("Sales parameters page", () => {
  it("asks for a legal entity, loads the defaults at version 0 and creates with If-Match 0", async () => {
    handler = (_url, init) =>
      init.method === "PUT" ? { status: 200, body: { ...params, quotationExpiryDays: 45, version: 1, updatedAt: "2027-03-02T09:00:00Z" }, etag: '"1"' } : { status: 200, body: params, etag: '"0"' };
    const { container } = renderAt("/sales/parameters");
    expect(screen.getByText("Choose a legal entity")).toBeInTheDocument();
    fill("Legal entity", "not-a-uuid");
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText("Enter a legal entity UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText(/has not saved parameters/)).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("legalEntityId")).toBe(LE);
    expect(pageViolations(container)).toEqual([]);

    expect(screen.queryByText(/Last changed/)).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Returned" })).not.toBeInTheDocument();
    fill(/Quotation expiry/, "45");
    fill(/Default win probability/, "35");
    expect(screen.getByLabelText(/Order intake dedup window/)).toHaveValue("30");
    fill(/Order intake dedup window/, "90");
    expect(screen.getByLabelText("Confirm")).toBeChecked();
    expect(screen.getByLabelText("Entry")).not.toBeChecked();
    fireEvent.click(screen.getByLabelText("Entry"));
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Sales parameters saved.")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"0"');
    expect(put?.body).toEqual({ quotationExpiryDays: 45, quotationProbabilityDefault: 35, creditReleaseValidityDays: 7, creditHoldSlaBusinessDays: 2, defaultOrderType: "ORDER", creditCheckPoints: ["entry", "confirm", "release"], intakeDedupWindowDays: 90 });
  });

  it("shows the values without a save action to a caller lacking the maintain duty", async () => {
    handler = () => ({ status: 200, body: { ...params, version: 3, updatedBy: "admin", updatedAt: "2027-03-02T09:00:00Z" }, etag: '"3"' });
    renderAt("/sales/parameters", ["sales.order.read"]);
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Sales parameters" });
    expect(screen.queryByRole("button", { name: "Save parameters" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the sales.parameter.maintain duty/)).toBeInTheDocument();
    expect(screen.getByText(/Last changed/)).toBeInTheDocument();
    expect(screen.queryByText(/has not saved parameters/)).not.toBeInTheDocument();
  });

  it("does not call the service for an out-of-range value", async () => {
    handler = () => ({ status: 200, body: params, etag: '"0"' });
    renderAt("/sales/parameters");
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByText(/has not saved parameters/);
    fill(/Credit hold SLA/, "11");
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText(/business days from 1 to 10/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
  });

  it("shows packing slip and invoice as not active yet", async () => {
    handler = () => ({ status: 200, body: { ...params, creditCheckPoints: ["confirm", "release"], version: 1 }, etag: '"1"' });
    renderAt("/sales/parameters");
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Sales parameters" });
    for (const name of ["Packing slip (not active yet)", "Invoice (not active yet)"]) {
      expect(screen.getByLabelText(name)).toBeDisabled();
      expect(screen.getByLabelText(name)).not.toBeChecked();
    }
  });

  it("refuses to save without the confirm check point", async () => {
    handler = () => ({ status: 200, body: { ...params, creditCheckPoints: ["confirm"], version: 1 }, etag: '"1"' });
    renderAt("/sales/parameters");
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Sales parameters" });
    fireEvent.click(screen.getByLabelText("Confirm"));
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText(/Keep confirm: it is the only check point that places a credit hold/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
  });

  it("shows the stale-version problem", async () => {
    handler = (_u, init) =>
      init.method === "PUT" ? { status: 412, body: { code: "SLS-0006", detail: "the resource was changed by another user" } } : { status: 200, body: { ...params, version: 2 }, etag: '"2"' };
    renderAt("/sales/parameters");
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Sales parameters" });
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Someone else changed these parameters")).toBeInTheDocument();
  });

  it("shows loading, then the problem code with retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "store unavailable" } } : { status: 200, body: params, etag: '"0"' });
    renderAt("/sales/parameters");
    fill("Legal entity", LE);
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("form", { name: "Sales parameters" })).toBeInTheDocument();
  });
});
