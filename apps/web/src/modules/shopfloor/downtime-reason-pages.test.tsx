import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Downtime, DowntimeReason } from "./api/client";
import shopfloor from "./module";
import { validateDowntimeReason } from "./NewDowntimeReasonPage";

const DOWN = "0195f0a0-0000-7000-8000-000000000012";
const JAM = "0195f0a0-0000-7000-8000-000000000051";

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

const jam: DowntimeReason = {
  id: JAM,
  code: "JAM",
  name: "Material jam",
  category: "unplanned",
  active: true,
  version: 2,
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};
const changeover: DowntimeReason = { ...jam, id: "0195f0a0-0000-7000-8000-000000000052", code: "CHG", name: "Changeover", category: "planned", version: 1 };

describe("Downtime reasons list", () => {
  it("lists the codes with their category and status", async () => {
    handler = (url) => (url.pathname.endsWith("/downtime-reasons") ? json(200, { items: [jam, { ...changeover, active: false }] }) : undefined);
    const { container } = renderAt("/shopfloor/downtime-reasons");
    expect(screen.getByRole("heading", { name: "Downtime reasons" })).toBeInTheDocument();
    const table = await findTable({ name: "Downtime reasons" });
    await waitFor(() => expect(table).toHaveTextContent("Material jam"));
    expect(table).toHaveTextContent("Unplanned");
    expect(table).toHaveTextContent("Planned");
    expect(table).toHaveTextContent("Inactive");
    expect(screen.getByRole("link", { name: "New downtime reason" })).toHaveAttribute("href", "/shopfloor/downtime-reasons/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/downtime-reasons");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No downtime reasons yet")).toBeInTheDocument();
  });

  it("deactivates a reason with the version it read as If-Match", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...jam, active: false, version: 3 });
      return url.pathname.endsWith("/downtime-reasons") ? json(200, { items: [jam] }) : undefined;
    };
    renderAt("/shopfloor/downtime-reasons");
    const table = await findTable({ name: "Downtime reasons" });
    fireEvent.click(await within(table).findByRole("button", { name: "Deactivate downtime reason JAM" }));
    await waitFor(() => expect(sent("PUT", `/downtime-reasons/${JAM}`)).toHaveLength(1));
    const [put] = sent("PUT", `/downtime-reasons/${JAM}`);
    expect(put.body).toEqual({ name: "Material jam", category: "unplanned", active: false });
    expect(header(put, "If-Match")).toBe('"2"');
  });

  it("shows a refused change", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(412, { code: "SF-0143", detail: "downtime reason was changed by someone else" });
      return url.pathname.endsWith("/downtime-reasons") ? json(200, { items: [jam] }) : undefined;
    };
    renderAt("/shopfloor/downtime-reasons");
    const table = await findTable({ name: "Downtime reasons" });
    fireEvent.click(await within(table).findByRole("button", { name: "Deactivate downtime reason JAM" }));
    expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  });
});

describe("New downtime reason", () => {
  it("validates like the service", () => {
    expect(validateDowntimeReason({ code: " JAM ", name: "Jam", category: "unplanned" }).input).toEqual({ code: "JAM", name: "Jam", category: "unplanned" });
    const { errors } = validateDowntimeReason({ code: "", name: "", category: "other" });
    expect(Object.keys(errors).sort()).toEqual(["category", "code", "name"]);
    expect(validateDowntimeReason({ code: "X".repeat(33), name: "n", category: "planned" }).errors.code).toBeDefined();
  });

  it("creates the reason with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/downtime-reasons")) return json(201, jam);
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [jam] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/downtime-reasons/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "JAM");
    fill("Name", "Material jam");
    fireEvent.click(screen.getByRole("button", { name: "Create downtime reason" }));
    expect(await screen.findByRole("heading", { name: "Downtime reasons" })).toBeInTheDocument();
    const [create] = sent("POST", "/downtime-reasons");
    expect(create.body).toEqual({ code: "JAM", name: "Material jam", category: "unplanned" });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("does not send an invalid form and shows a used code on the code field", async () => {
    renderAt("/shopfloor/downtime-reasons/new");
    fireEvent.click(screen.getByRole("button", { name: "Create downtime reason" }));
    expect(screen.getByText("Enter a code of up to 32 characters.")).toBeInTheDocument();
    expect(sent("POST", "/downtime-reasons")).toHaveLength(0);
    handler = () => json(409, { code: "SF-0142", detail: "code is already used" });
    fill("Code", "JAM");
    fill("Name", "Material jam");
    fireEvent.click(screen.getByRole("button", { name: "Create downtime reason" }));
    expect(await screen.findByText("This code is already used.")).toBeInTheDocument();
  });
});

describe("Coding a downtime with the reason master", () => {
  const open: Downtime = {
    id: DOWN,
    machineId: "0195f0a0-0000-7000-8000-000000000020",
    resourceId: "0195f0a0-0000-7000-8000-000000000010",
    startedAt: "2026-10-21T08:10:00Z",
    endedAt: null,
    reasonCode: null,
    category: "unplanned",
    cause: "down",
    andonRaised: false,
  };

  it("offers the active reasons, sends only the code and takes the category from the reason", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/downtimes/${DOWN}:code`)) return json(200, { ...open, reasonCode: "CHG", category: "planned" });
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [jam, changeover] });
      if (url.pathname.endsWith("/downtimes")) return json(200, { items: [open] });
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/downtimes?downtime=${DOWN}`);
    const reason = await screen.findByLabelText("Reason code");
    expect(reason.tagName).toBe("SELECT");
    expect(screen.queryByLabelText("Category")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText("Choose a reason code.")).toBeInTheDocument();
    fill("Reason code", "CHG");
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/is coded CHG/)).toBeInTheDocument();
    const [code] = sent("POST", ":code");
    expect(code.body).toEqual({ reasonCode: "CHG" });
  });

  it("shows a reason the service refuses", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "SF-0159", detail: "downtime reason JAM is inactive" });
      if (url.pathname.endsWith("/downtime-reasons")) return json(200, { items: [jam] });
      return json(200, { items: [open] });
    };
    renderAt(`/shopfloor/downtimes?downtime=${DOWN}`);
    await screen.findByLabelText("Reason code");
    fill("Reason code", "JAM");
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/inactive/)).toBeInTheDocument();
  });
});
