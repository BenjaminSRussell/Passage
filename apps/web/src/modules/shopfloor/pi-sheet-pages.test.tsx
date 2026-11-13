import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import type { ControlRecipeDetail, PISheet } from "./api/client";
import shopfloor from "./module";

const ORDER = "0195f0a0-0000-7000-8000-000000000040";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";
const SHEET = "0195f0a0-0000-7000-8000-000000000071";
const RECIPE = "0195f0a0-0000-7000-8000-000000000061";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));
const json = (status: number, body: unknown): Reply => ({ status, body });

function extra(url: URL): Reply | undefined {
  if (url.pathname.endsWith("/engineering/resources")) return json(200, { items: [{ id: RESOURCE, code: "ASM-1", name: "Assembly cell" }] });
  if (url.pathname.endsWith("/production/orders")) return json(200, { items: [{ id: ORDER, orderNumber: "MO-77" }] });
  if (url.pathname.endsWith(`/production/orders/${ORDER}`)) return json(200, { id: ORDER, orderNumber: "MO-77", productId: "" });
  return undefined;
}

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

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
      if (!lookup) calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = lookup ?? handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

const DESIGNER = ["shopfloor.view", "shopfloor.pisheet.maintain"];
const OPERATOR = ["shopfloor.view", "shopfloor.controlrecipe.execute", "shopfloor.process.create"];

function renderAt(path: string, duties: readonly string[]) {
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

const sheet = (over: Partial<PISheet> = {}): PISheet => ({
  id: SHEET,
  code: "PUMP-ASSY",
  version: 1,
  name: "Pump assembly",
  status: "draft",
  recipe: {
    procedure: {
      name: "Pump assembly",
      unitProcedures: [
        { name: "Cell", operations: [{ name: "Assemble", phases: [{ name: "Fit", instructions: [{ seq: 1, category: "instruction-text", text: "Check the casing." }] }] }] },
      ],
    },
  },
  rev: 3,
  createdBy: "maya",
  createdAt: "2026-10-20T09:00:00Z",
  updatedBy: "maya",
  updatedAt: "2026-10-21T09:00:00Z",
  releasedBy: null,
  releasedAt: null,
  ...over,
});

describe("PI sheets list", () => {
  it("lists the sheets with their status and filters by status", async () => {
    handler = (url) => (url.pathname.endsWith("/pi-sheets") ? json(200, { items: [sheet(), sheet({ id: "x2", version: 2, status: "released" })] }) : undefined);
    const { container } = renderAt("/shopfloor/pi-sheets", DESIGNER);
    const table = await findTable({ name: "PI sheets" });
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(within(table).getByText("Draft")).toBeInTheDocument();
    expect(within(table).getByText("Released")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New PI sheet" })).toHaveAttribute("href", "/shopfloor/pi-sheets/new");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "released" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=released"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers no new sheet without the duty, and says when the load failed", async () => {
    handler = () => json(500, { code: "COM-0503", detail: "down" });
    renderAt("/shopfloor/pi-sheets", ["shopfloor.view"]);
    expect(await screen.findByText("Could not load the PI sheets")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New PI sheet" })).toBeNull();
  });
});

describe("PI sheet designer", () => {
  it("edits a draft, saves it with the rev it read, and releases it after saving", async () => {
    let current = sheet();
    handler = (url, init) => {
      if (url.pathname.endsWith(`/pi-sheets/${SHEET}:release`)) return json(200, { ...current, status: "released", rev: 5 });
      if (url.pathname.endsWith(`/pi-sheets/${SHEET}`) && init.method === "PUT") {
        current = { ...current, ...JSON.parse(String(init.body)), rev: current.rev + 1 };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/pi-sheets/${SHEET}`)) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/pi-sheets/${SHEET}`, DESIGNER);
    expect(await screen.findByText("PUMP-ASSY, version 1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump assembly line 2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toMatchObject({ name: "Pump assembly line 2", rev: 3 });
    fireEvent.click(await screen.findByRole("button", { name: "Release" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":release"))).toBe(true));
    expect(calls.find((c) => c.url.pathname.endsWith(":release"))?.body).toEqual({ rev: 5 });
  });

  it("holds the release back while the sheet has problems, and says which", async () => {
    handler = (url) => (url.pathname.endsWith(`/pi-sheets/${SHEET}`) ? json(200, sheet({ recipe: { procedure: { name: "P", unitProcedures: [{ name: "U", operations: [{ name: "O", phases: [{ name: "F", instructions: [{ seq: 1, category: "instruction-text", text: "" }] }] }] }] } } })) : undefined);
    renderAt(`/shopfloor/pi-sheets/${SHEET}`, DESIGNER);
    expect(await screen.findByText("Before this sheet can be released")).toBeInTheDocument();
    expect(screen.getByText("U / O / F, step 1: write the text.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Release" })).toBeDisabled();
  });

  it("adds a step of a chosen type and numbers it", async () => {
    handler = (url) => (url.pathname.endsWith(`/pi-sheets/${SHEET}`) ? json(200, sheet()) : undefined);
    renderAt(`/shopfloor/pi-sheets/${SHEET}`, DESIGNER);
    await screen.findByText("PUMP-ASSY, version 1");
    fireEvent.change(screen.getByLabelText("Add a step"), { target: { value: "process-data-request" } });
    expect(await screen.findByText("Step 2: Enter a value")).toBeInTheDocument();
    expect(screen.getByLabelText("Characteristic")).toBeInTheDocument();
  });

  it("shows a released sheet read-only and makes a new version of it", async () => {
    handler = (url) => {
      if (url.pathname.endsWith(`/pi-sheets/${SHEET}:new-version`)) return json(200, sheet({ id: "next", version: 2 }));
      if (url.pathname.endsWith(`/pi-sheets/${SHEET}`)) return json(200, sheet({ status: "released" }));
      if (url.pathname.endsWith("/pi-sheets/next")) return json(200, sheet({ id: "next", version: 2 }));
      return undefined;
    };
    renderAt(`/shopfloor/pi-sheets/${SHEET}`, DESIGNER);
    await screen.findByText("PUMP-ASSY, version 1");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
    expect(screen.getByLabelText("Name")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "New version" }));
    expect(await screen.findByText("PUMP-ASSY, version 2")).toBeInTheDocument();
  });

  it("shows the service's refusal of a stale save", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(409, { code: "SF-0182", detail: "the PI sheet changed since you read it" });
      return url.pathname.endsWith(`/pi-sheets/${SHEET}`) ? json(200, sheet()) : undefined;
    };
    renderAt(`/shopfloor/pi-sheets/${SHEET}`, DESIGNER);
    await screen.findByText("PUMP-ASSY, version 1");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("That did not work")).toBeInTheDocument();
    expect(screen.getByText(/Someone else changed this PI sheet/)).toBeInTheDocument();
  });

  it("creates a draft from a code and a name", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/pi-sheets") && init.method === "POST") return json(201, sheet({ id: "made" }));
      if (url.pathname.endsWith("/pi-sheets/made")) return json(200, sheet({ id: "made" }));
      return undefined;
    };
    renderAt("/shopfloor/pi-sheets/new", DESIGNER);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "pump-assy" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump assembly" } });
    fireEvent.click(screen.getByRole("button", { name: "Create draft" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ code: "PUMP-ASSY", name: "Pump assembly" });
    expect(await screen.findByText("PUMP-ASSY, version 1")).toBeInTheDocument();
  });
});

const kioskRecipe = (status: ControlRecipeDetail["status"]): ControlRecipeDetail => ({
  id: RECIPE,
  orderId: ORDER,
  resourceId: RESOURCE,
  destination: "kiosk",
  edgeId: null,
  topic: null,
  status,
  recipe: {
    procedure: {
      name: "Pump",
      unitProcedures: [
        {
          name: "Cell",
          operations: [
            {
              name: "Assemble",
              phases: [
                {
                  name: "Fit",
                  instructions: [
                    { seq: 1, category: "instruction-text", text: "Check the casing." },
                    { seq: 2, category: "process-data-request", text: "Measure the end float.", characteristic: { name: "End float", type: "numeric", unit: "mm", min: 0.2, max: 0.5 } },
                    { seq: 3, category: "signature-request", text: "Sign to confirm.", meaning: "verified" },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  },
  sendCount: 1,
  lastSentAt: "2026-10-21T09:00:00Z",
  lastError: null,
  version: 1,
  createdBy: "maya",
  createdAt: "2026-10-21T09:00:00Z",
  updatedAt: "2026-10-21T09:00:00Z",
  history: [],
  writes: [],
});

describe("kiosk PI sheet", () => {
  it("starts the recipe, records each entered value as a process message, and completes the recipe", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(`/control-recipes/${RECIPE}:report`)) return json(200, kioskRecipe("in-execution"));
      if (url.pathname.endsWith(`/control-recipes/${RECIPE}`)) return json(200, kioskRecipe("sent"));
      if (url.pathname.endsWith("/process-messages") && init.method === "POST") return json(201, { message: { id: "m" }, duplicate: false });
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/kiosk/pi/${RECIPE}`, OPERATOR);
    fireEvent.click(await screen.findByRole("button", { name: "Start" }));
    expect(await screen.findByText("Step 1 of 3")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname.endsWith(":report")).map((c) => (c.body as { status: string }).status)).toEqual(["received", "in-execution"]);
    expect(screen.getByText("Cell / Assemble / Fit")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Next step" }));
    expect(await screen.findByText("Step 2 of 3")).toBeInTheDocument();

    // A value outside its limits needs a comment (AC-25.12).
    fireEvent.change(screen.getByLabelText(/End float/), { target: { value: "0.7" } });
    fireEvent.click(screen.getByRole("button", { name: "Next step" }));
    expect(await screen.findByText("The value is outside its limits. Say why in the comment to go on.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: "gauge was warm" } });
    fireEvent.click(screen.getByRole("button", { name: "Next step" }));
    expect(await screen.findByText("Step 3 of 3")).toBeInTheDocument();
    const message = calls.find((c) => c.url.pathname.endsWith("/process-messages"))!.body as { source: string; categoryCode: string; orderId: string; phasePath: string; sourceMessageId: string; values: { name: string; value: string; max?: string }[] };
    expect(message).toMatchObject({ source: "kiosk", categoryCode: "process-data", orderId: ORDER, phasePath: "Pump/Cell/Assemble/Fit", sourceMessageId: `${RECIPE}:Cell/Assemble/Fit/2` });
    expect(message.values).toEqual([
      { name: "End float", type: "numeric", value: "0.7", min: "0.2", max: "0.5", unit: "mm" },
      { name: "comment", type: "text", value: "gauge was warm" },
    ]);

    fireEvent.click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByText("Type your name to sign.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Verified by: your name"), { target: { value: "Ana Ruiz" } });
    fireEvent.click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByText('Confirm the statement "I verified this step." to sign.')).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("I verified this step."));
    fireEvent.click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByText("All steps are done")).toBeInTheDocument();
    const signed = calls.filter((c) => c.url.pathname.endsWith("/process-messages")).at(-1)?.body as { values: unknown[] };
    expect(signed.values).toEqual([
      { name: "signed-by", type: "text", value: "Ana Ruiz" },
      { name: "signature-meaning", type: "text", value: "verified" },
    ]);
    expect(calls.filter((c) => c.url.pathname.endsWith(":report")).map((c) => (c.body as { status: string }).status).at(-1)).toBe("completed");
  });

  it("keeps the step open and says so when the value could not be recorded, then retries without a second message", async () => {
    let fail = true;
    handler = (url, init) => {
      if (url.pathname.endsWith(`/control-recipes/${RECIPE}`)) return json(200, kioskRecipe("in-execution"));
      if (url.pathname.endsWith("/process-messages") && init.method === "POST") return fail ? json(503, { code: "COM-0503", detail: "down" }) : json(201, { message: { id: "m" }, duplicate: false });
      return undefined;
    };
    renderAt(`/shopfloor/kiosk/pi/${RECIPE}`, OPERATOR);
    fireEvent.click(await screen.findByRole("button", { name: "Next step" }));
    fireEvent.change(await screen.findByLabelText(/End float/), { target: { value: "0.3" } });
    fireEvent.click(screen.getByRole("button", { name: "Next step" }));
    expect(await screen.findByText("The step was not saved")).toBeInTheDocument();
    expect(screen.getByText("Step 2 of 3")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Next step" }));
    expect(await screen.findByText("Step 3 of 3")).toBeInTheDocument();
  });

  it("says when the recipe is finished or runs somewhere else", async () => {
    handler = (url) => (url.pathname.endsWith(`/control-recipes/${RECIPE}`) ? json(200, kioskRecipe("completed")) : undefined);
    const done = renderAt(`/shopfloor/kiosk/pi/${RECIPE}`, OPERATOR);
    expect(await screen.findByText("This recipe is completed")).toBeInTheDocument();
    done.unmount();
    handler = (url) => (url.pathname.endsWith(`/control-recipes/${RECIPE}`) ? json(200, { ...kioskRecipe("sent"), destination: "pcs-opcua" }) : undefined);
    renderAt(`/shopfloor/kiosk/pi/${RECIPE}`, OPERATOR);
    expect(await screen.findByText("This recipe runs on a process control system")).toBeInTheDocument();
  });
});
