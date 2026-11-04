import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ProcessCategoryDef } from "./api/client";
import shopfloor from "./module";
import { emptyCategoryValues, validateCategory } from "./ProcessCategoryForm";

const WEIGH = "0195f0a0-0000-7000-8000-000000000061";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

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
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const weigh: ProcessCategoryDef = {
  id: WEIGH,
  code: "pi-weigh",
  name: "Weigh-in",
  destinations: ["ebr", "kafka"],
  rejectOutOfLimit: true,
  characteristics: [{ name: "weight", type: "numeric", unit: "kg", min: "9.5", max: "10.5", required: true }],
  active: true,
  builtIn: false,
  version: 2,
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};
const phaseStart: ProcessCategoryDef = { code: "phase-start", name: "phase-start", destinations: ["ebr", "kafka"], rejectOutOfLimit: false, characteristics: [], active: true, builtIn: true };

describe("Process categories list", () => {
  it("lists the master rows and the built-in defaults", async () => {
    handler = (url) => (url.pathname.endsWith("/process-categories") ? json(200, { items: [weigh, phaseStart] }) : undefined);
    const { container } = renderAt("/shopfloor/process-categories");
    const table = await findTable({ name: "Process categories" });
    await waitFor(() => expect(table).toHaveTextContent("Weigh-in"));
    expect(table).toHaveTextContent("Batch record (EBR), Kafka topic");
    expect(table).toHaveTextContent("Refused");
    expect(table).toHaveTextContent("Phase start");
    expect(table).toHaveTextContent("Built-in default");
    expect(within(table).getByRole("link", { name: "Edit category pi-weigh" })).toHaveAttribute("href", `/shopfloor/process-categories/${WEIGH}`);
    expect(screen.getByRole("link", { name: "New process category" })).toHaveAttribute("href", "/shopfloor/process-categories/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/process-categories");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No process categories")).toBeInTheDocument();
  });

  it("deactivates a row with the version it read as If-Match", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...weigh, active: false, version: 3 });
      return url.pathname.endsWith("/process-categories") ? json(200, { items: [weigh] }) : undefined;
    };
    renderAt("/shopfloor/process-categories");
    const table = await findTable({ name: "Process categories" });
    fireEvent.click(await within(table).findByRole("button", { name: "Deactivate category pi-weigh" }));
    await waitFor(() => expect(sent("PUT", `/process-categories/${WEIGH}`)).toHaveLength(1));
    const [put] = sent("PUT", `/process-categories/${WEIGH}`);
    expect(put.body).toMatchObject({ name: "Weigh-in", destinations: ["ebr", "kafka"], rejectOutOfLimit: true, active: false });
    expect(header(put, "If-Match")).toBe('"2"');
  });

  it("customizes a built-in default by creating its row and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...phaseStart, id: WEIGH, builtIn: false, version: 1, name: "Phase start" });
      if (url.pathname.endsWith(`/process-categories/${WEIGH}`)) return json(200, { ...phaseStart, id: WEIGH, builtIn: false, version: 1, name: "Phase start" });
      return url.pathname.endsWith("/process-categories") ? json(200, { items: [phaseStart] }) : undefined;
    };
    renderAt("/shopfloor/process-categories");
    const table = await findTable({ name: "Process categories" });
    fireEvent.click(await within(table).findByRole("button", { name: "Customize category phase-start" }));
    expect(await screen.findByRole("heading", { name: "Process category phase-start" })).toBeInTheDocument();
    const [create] = sent("POST", "/process-categories");
    expect(create.body).toMatchObject({ code: "phase-start", name: "Phase start", destinations: ["ebr", "kafka"], rejectOutOfLimit: false });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows a refused change", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(412, { code: "SF-0143", detail: "process category was changed by someone else" });
      return url.pathname.endsWith("/process-categories") ? json(200, { items: [weigh] }) : undefined;
    };
    renderAt("/shopfloor/process-categories");
    const table = await findTable({ name: "Process categories" });
    fireEvent.click(await within(table).findByRole("button", { name: "Deactivate category pi-weigh" }));
    expect(await screen.findByText(/changed by someone else \(SF-0143\)/)).toBeInTheDocument();
  });
});

describe("New process category", () => {
  it("validates like the service", () => {
    const base = { ...emptyCategoryValues(), code: " pi-weigh ", name: "Weigh" };
    expect(validateCategory(base, true).input).toEqual({ code: "pi-weigh", name: "Weigh", destinations: ["ebr"], rejectOutOfLimit: false, characteristics: [], active: true });
    expect(validateCategory({ ...base, code: "Bad Code" }, true).errors.code).toBeDefined();
    expect(validateCategory({ ...base, code: "" }, false).errors.code).toBeUndefined();
    expect(validateCategory({ ...base, destinations: [] }, true).errors.destinations).toBeDefined();
    const def = { name: "w", type: "numeric" as const, unit: "", min: "5", max: "1", required: false };
    expect(validateCategory({ ...base, defs: [def] }, true).errors["defs[0].min"]).toBeDefined();
    expect(validateCategory({ ...base, defs: [{ ...def, min: "x", max: "" }] }, true).errors["defs[0].min"]).toBeDefined();
    expect(validateCategory({ ...base, defs: [def, { ...def, min: "", max: "" }] }, true).errors["defs[1].name"]).toBeDefined();
  });

  it("creates the category with its characteristic and an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/process-categories")) return json(201, weigh);
      if (url.pathname.endsWith("/process-categories")) return json(200, { items: [weigh] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/process-categories/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "pi-weigh");
    fill("Name", "Weigh-in");
    fireEvent.click(screen.getByLabelText("Kafka topic"));
    fireEvent.click(screen.getByLabelText(/Refuse messages with a value outside/));
    fireEvent.click(screen.getByRole("button", { name: "Add characteristic" }));
    fill("Characteristic 1 name", "weight");
    fill("Characteristic 1 lower limit", "9.5");
    fill("Characteristic 1 upper limit", "10.5");
    fill("Characteristic 1 unit", "kg");
    fireEvent.click(screen.getByLabelText("Characteristic 1 is required in every message"));
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(await screen.findByRole("heading", { name: "Process categories" })).toBeInTheDocument();
    const [create] = sent("POST", "/process-categories");
    expect(create.body).toEqual({
      code: "pi-weigh",
      name: "Weigh-in",
      destinations: ["ebr", "kafka"],
      rejectOutOfLimit: true,
      active: true,
      characteristics: [{ name: "weight", type: "numeric", unit: "kg", min: "9.5", max: "10.5", required: true }],
    });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("does not send an invalid form and shows a used code on the code field", async () => {
    renderAt("/shopfloor/process-categories/new");
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(screen.getByText("Use up to 32 lowercase letters, digits or hyphens.")).toBeInTheDocument();
    expect(sent("POST", "/process-categories")).toHaveLength(0);
    handler = () => json(409, { code: "SF-0142", detail: "code is already used" });
    fill("Code", "pi-weigh");
    fill("Name", "Weigh-in");
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(await screen.findByText("This code is already used.")).toBeInTheDocument();
  });

  it("shows a service error on a characteristic on that characteristic's field, with the alert", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "validation failed", errors: { "characteristics[0].min": "must be a decimal number" } });
    renderAt("/shopfloor/process-categories/new");
    fill("Code", "pi-weigh");
    fill("Name", "Weigh-in");
    fireEvent.click(screen.getByRole("button", { name: "Add characteristic" }));
    fill("Characteristic 1 name", "weight");
    fill("Characteristic 1 lower limit", "1");
    fireEvent.click(screen.getByRole("button", { name: "Create category" }));
    expect(await screen.findByText("must be a decimal number")).toBeInTheDocument();
    expect(screen.getByText(/validation failed \(COM-0001\)/)).toBeInTheDocument();
  });
});

describe("Process category detail", () => {
  it("shows the stored category, saves a change with the version and shows the new one", async () => {
    let stored = weigh;
    handler = (url, init) => {
      if (init.method === "PUT") {
        stored = { ...stored, ...(JSON.parse(String(init.body)) as object), version: 3 };
        return json(200, stored);
      }
      return url.pathname.endsWith(`/process-categories/${WEIGH}`) ? json(200, stored) : undefined;
    };
    const { container } = renderAt(`/shopfloor/process-categories/${WEIGH}`);
    expect(await screen.findByRole("heading", { name: "Process category pi-weigh" })).toBeInTheDocument();
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Characteristic 1 name")).toHaveValue("weight");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByLabelText(/Refuse messages with a value outside/));
    fireEvent.click(screen.getByRole("button", { name: "Save category" }));
    expect(await screen.findByText("The category is now at version 3.")).toBeInTheDocument();
    const [put] = sent("PUT", `/process-categories/${WEIGH}`);
    expect(header(put, "If-Match")).toBe('"2"');
    expect(put.body).toMatchObject({ rejectOutOfLimit: false, destinations: ["ebr", "kafka"] });
    expect(put.body).not.toHaveProperty("code");
  });

  it("shows the load error and a refused save", async () => {
    handler = () => json(404, { code: "SF-0164", detail: "process category not found" });
    renderAt(`/shopfloor/process-categories/${WEIGH}`);
    expect(await screen.findByText(/process category not found \(SF-0164\)/)).toBeInTheDocument();
  });

  it("shows a stale version refusal", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(412, { code: "SF-0143", detail: "process category was changed by someone else" });
      return url.pathname.endsWith(`/process-categories/${WEIGH}`) ? json(200, weigh) : undefined;
    };
    renderAt(`/shopfloor/process-categories/${WEIGH}`);
    await screen.findByRole("heading", { name: "Process category pi-weigh" });
    fireEvent.click(screen.getByRole("button", { name: "Save category" }));
    expect(await screen.findByText(/changed by someone else \(SF-0143\)/)).toBeInTheDocument();
  });
});

describe("Process message forms use the category master", () => {
  it("offers a custom category on the manual entry form", async () => {
    handler = (url) => (url.pathname.endsWith("/process-categories") ? json(200, { items: [weigh, phaseStart] }) : undefined);
    renderAt("/shopfloor/process-messages/new");
    expect(await screen.findByRole("option", { name: "Weigh-in" })).toHaveValue("pi-weigh");
    expect(screen.getByRole("option", { name: "Phase start" })).toHaveValue("phase-start");
  });

  it("lets the monitor filter by an inactive category, whose stored messages remain", async () => {
    const inactive = { ...weigh, active: false };
    handler = (url) => {
      if (url.pathname.endsWith("/process-categories")) return url.searchParams.get("active") === "true" ? json(200, { items: [phaseStart] }) : json(200, { items: [inactive, phaseStart] });
      if (url.pathname.endsWith("/process-messages")) return json(200, { items: [], nextCursor: null });
      return undefined;
    };
    renderAt("/shopfloor/process-messages");
    expect(await screen.findByRole("option", { name: "Weigh-in" })).toHaveValue("pi-weigh");
    expect(calls.some((c) => c.url.pathname.endsWith("/process-categories") && c.url.searchParams.get("active") === "true")).toBe(false);
  });

  it("keeps the built-in categories when the master cannot be read", async () => {
    renderAt("/shopfloor/process-messages/new");
    expect(await screen.findByRole("option", { name: "Quality result" })).toHaveValue("quality-result");
  });
});
