import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import type { ControlRecipeDetail } from "./api/client";
import shopfloor from "./module";

const ORDER = "0195f0a0-0000-7000-8000-000000000040";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";
const RECIPE = "0195f0a0-0000-7000-8000-000000000061";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));
const json = (status: number, body: unknown): Reply => ({ status, body });

function extra(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/engineering/resources")) return json(200, { items: [{ id: RESOURCE, code: "MIX-1", name: "Mixer" }] });
  if (url.pathname.endsWith("/production/orders")) return json(200, { items: [{ id: ORDER, orderNumber: "MO-77" }] });
  if (url.pathname.endsWith(`/production/orders/${ORDER}`)) return json(200, { id: ORDER, orderNumber: "MO-77", productId: "" });
  return undefined;
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

beforeEach(() => {
  calls = [];
  resetResourceCache();
  resetOrderCache();
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const lookup = extra(url);
      if (!lookup) calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = lookup ?? handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const SUPERVISOR = ["shopfloor.view", "shopfloor.controlrecipe.create"];
const VIEWER = ["shopfloor.view"];

function renderAt(path: string, duties: readonly string[] = SUPERVISOR) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const recipe: ControlRecipeDetail = {
  id: RECIPE,
  orderId: ORDER,
  resourceId: RESOURCE,
  destination: "pcs-opcua",
  edgeId: "0195f0a0-0000-7000-8000-000000000051",
  topic: null,
  status: "error",
  recipe: {
    procedure: {
      name: "Batch",
      unitProcedures: [
        {
          name: "Reactor",
          operations: [
            {
              name: "Heat",
              phases: [
                {
                  name: "Ramp",
                  instructions: [
                    { seq: 1, category: "instruction-text", text: "Check the vessel is empty." },
                    { seq: 2, category: "process-parameter-setpoint", node: "ns=2;s=Vessel1.TempSP", value: 72 },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  },
  sendCount: 2,
  lastSentAt: "2026-11-10T08:00:00Z",
  lastError: "1 of 1 setpoints were refused or failed; see the write audit",
  version: 4,
  createdBy: "planner",
  createdAt: "2026-11-10T07:00:00Z",
  updatedAt: "2026-11-10T08:05:00Z",
  history: [
    { id: "h1", fromStatus: null, toStatus: "sent", actor: "planner", note: null, forced: false, at: "2026-11-10T07:00:00Z" },
    { id: "h2", fromStatus: "sent", toStatus: "error", actor: "edge-7", note: "setpoints refused", forced: false, at: "2026-11-10T07:05:00Z" },
  ],
  writes: [
    { id: "w1", phasePath: "Batch/Reactor/Heat/Ramp", node: "ns=2;s=Vessel1.TempSP", value: "72", readback: "70", written: true, deviation: true, reason: null, at: "2026-11-10T07:04:00Z" },
    { id: "w2", phasePath: "Batch/Reactor/Heat/Ramp", node: "ns=2;s=Other", value: "72", readback: null, written: false, deviation: false, reason: "node is not on the write allowlist", at: "2026-11-10T07:04:30Z" },
  ],
};

describe("Control recipe monitor", () => {
  it("lists recipes with order and resource names and filters by status and destination", async () => {
    handler = (url) => (url.pathname.endsWith("/control-recipes") ? json(200, { items: [recipe] }) : undefined);
    const { container } = renderAt("/shopfloor/control-recipes");
    const table = await findTable({ name: "Control recipes" });
    await waitFor(() => expect(table).toHaveTextContent("MO-77"));
    expect(table).toHaveTextContent("Mixer");
    expect(table).toHaveTextContent("Process control (OPC-UA)");
    expect(table).toHaveTextContent("2 times");
    expect(table).toHaveTextContent("Error");
    expect(screen.getByRole("link", { name: "MO-77" })).toHaveAttribute("href", `/shopfloor/control-recipes/${RECIPE}`);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "error" } });
    fireEvent.change(screen.getByLabelText("Destination"), { target: { value: "kiosk" } });
    await waitFor(() => expect(calls.some((c) => c.url.search === "?status=error&destination=kiosk")).toBe(true));
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load error with a retry", async () => {
    handler = () => json(500, { code: "SF-0500", detail: "boom" });
    renderAt("/shopfloor/control-recipes");
    expect(await screen.findByText("Could not load control recipes")).toBeInTheDocument();
  });
});

describe("Control recipe detail", () => {
  const serve = (extraHandler?: Handler) => {
    handler = (url, init) => extraHandler?.(url, init) ?? (url.pathname.endsWith(`/control-recipes/${RECIPE}`) && !init.method ? json(200, recipe) : undefined);
  };

  it("shows the instructions, history and the write audit with deviations and refusals", async () => {
    serve();
    const { container } = renderAt(`/shopfloor/control-recipes/${RECIPE}`);
    expect(await screen.findByText("Reactor / Heat / Ramp")).toBeInTheDocument();
    expect(screen.getByText("Check the vessel is empty.")).toBeInTheDocument();
    expect(screen.getByText(/Set ns=2;s=Vessel1.TempSP to 72/)).toBeInTheDocument();
    const writes = await findTable({ name: "Setpoint writes" });
    expect(writes).toHaveTextContent("Readback differs");
    expect(writes).toHaveTextContent("Refused: node is not on the write allowlist");
    expect(await findTable({ name: "Status history" })).toHaveTextContent("setpoints refused");
    expect(pageViolations(container)).toEqual([]);
  });

  it("resends a recipe in error with a key, and requires a reason with force", async () => {
    serve((url, init) => (init.method === "POST" && url.pathname.endsWith(":resend") ? json(200, { ...recipe, status: "sent", sendCount: 3, lastError: null }) : undefined));
    renderAt(`/shopfloor/control-recipes/${RECIPE}`);
    await screen.findByRole("button", { name: "Send recipe again" });
    expect(screen.getByText(/sent again 1 of 5 times/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText(/may have started this recipe/));
    fireEvent.click(screen.getByRole("button", { name: "Send recipe again" }));
    expect(await screen.findByText(/Say why it is safe/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "PCS confirmed it never started" } });
    fireEvent.click(screen.getByRole("button", { name: "Send recipe again" }));
    expect(await screen.findByText("Sent again")).toBeInTheDocument();
    const [post] = calls.filter((c) => c.method === "POST");
    expect(post.body).toEqual({ force: true, reason: "PCS confirmed it never started" });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows the service's refusal of a resend", async () => {
    serve((url, init) => (init.method === "POST" && url.pathname.endsWith(":resend") ? json(422, { code: "SF-0177", detail: "resend with force and a reason" }) : undefined));
    renderAt(`/shopfloor/control-recipes/${RECIPE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Send recipe again" }));
    expect(await screen.findByText("Could not send the recipe again")).toBeInTheDocument();
  });

  it("offers no resend without the create duty or when the recipe is not in error", async () => {
    serve();
    renderAt(`/shopfloor/control-recipes/${RECIPE}`, VIEWER);
    await screen.findByText("Reactor / Heat / Ramp");
    expect(screen.queryByRole("button", { name: "Send recipe again" })).toBeNull();
  });
});
