import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { canCommandOperations, runOperationCommand, STATUSES, type Operation } from "./api/client";
import { resetResourceCache } from "../mfg-shared/resources";
import { initialValues, toInput, validateOrder } from "./CreateOrderPage";
import production from "./module";
import { json, order, orderId, problem } from "./testData";

beforeEach(() => resetResourceCache());
afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
const header = (init: RequestInit | undefined, name: string) => (init?.headers as Record<string, string> | undefined)?.[name];

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

const op = (overrides: Partial<Operation>): Operation => ({ ...order().operations[0], ...overrides });

/** A Started order with operation 10 finished by a route card, 20 open and 30 subcontracted. */
const started = (overrides: Parameters<typeof order>[0] = {}) =>
  order({
    status: "Started",
    version: 6,
    operations: [
      op({ operNumber: 10, status: "finished", hoursReported: "1.5" }),
      op({ operNumber: 20, status: "open" }),
      op({ operNumber: 30, status: "open", subcontract: true, subcontractPrice: "2" }),
    ],
    ...overrides,
  });

type Extra = (url: string, init?: RequestInit) => Response | undefined;

/** Answers the detail page's reads; extra handles the request a test cares about. */
function detailFetch(current: ReturnType<typeof order>, extra?: Extra) {
  return stubFetch((url, init) => {
    const handled = extra?.(url, init);
    if (handled) {
      return handled;
    }
    if (url.endsWith("/status-log") || url.endsWith("/journals") || url.endsWith("/as-built") || url.endsWith("/trigger-points")) {
      return json({ orderId, items: [] });
    }
    if (url.includes("reworkOf=") || url.includes("/bom-edits")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    return json(current);
  });
}

describe("operation command client", () => {
  it("posts the command on the operation with the order version and the key, and no body", async () => {
    const fetchMock = stubFetch(() => json(started()));
    await runOperationCommand(orderId, 20, "finish", 6, "key-op");
    const [url, init] = calls(fetchMock)[0];
    expect(url).toBe(`/api/v1/production/orders/${orderId}/operations/20:finish`);
    expect(init?.method).toBe("POST");
    expect(header(init, "If-Match")).toBe('"6"');
    expect(header(init, "Idempotency-Key")).toBe("key-op");
    expect(init?.body).toBeUndefined();
  });

  it("allows operation commands only while the order takes feedback", () => {
    expect(STATUSES.filter(canCommandOperations)).toEqual(["Started", "ReportedAsFinished"]);
  });
});

describe("Route operations on the order page", () => {
  it("finishes an open operation and reopens a finished one", async () => {
    let current = started();
    const fetchMock = detailFetch(current, (url, init) => {
      if (init?.method !== "POST" || !url.includes("/operations/")) {
        return undefined;
      }
      const [, n, command] = /operations\/(\d+):(\w+)$/.exec(url) ?? [];
      const next = command === "finish" ? "finished" : Number(n) === 10 ? "started" : "open";
      current = { ...current, version: current.version + 1, operations: current.operations.map((o) => (o.operNumber === Number(n) ? { ...o, status: next } : o)) };
      return json(current);
    });
    const { container } = renderAt(`/production/${orderId}`);
    const route = await findTable({ name: "Order operations" });

    await userEvent.click(within(route).getByRole("button", { name: "Finish operation 20" }));
    expect(await screen.findByText("Operation 20 finished.")).toBeInTheDocument();
    const finish = calls(fetchMock).find(([url, init]) => init?.method === "POST" && url.endsWith("/operations/20:finish"));
    expect(header(finish?.[1], "If-Match")).toBe('"6"');
    expect(header(finish?.[1], "Idempotency-Key")).toBeTruthy();
    const table = screen.getByRole("table", { name: "Order operations" });
    expect(within(table).getByRole("button", { name: "Reopen operation 20" })).toBeInTheDocument();

    // The next command carries the version the first one returned.
    await userEvent.click(within(table).getByRole("button", { name: "Reopen operation 10" }));
    expect(await screen.findByText("Operation 10 reopened.")).toBeInTheDocument();
    const reopen = calls(fetchMock).find(([url, init]) => init?.method === "POST" && url.endsWith("/operations/10:reopen"));
    expect(header(reopen?.[1], "If-Match")).toBe('"7"');
    expect(within(screen.getByRole("table", { name: "Order operations" })).getByRole("button", { name: "Finish operation 10" })).toBeInTheDocument();

    expect(await checkAxeAA(container)).toEqual([]);
  });

  it("offers no action on a subcontracted operation", async () => {
    detailFetch(started());
    renderAt(`/production/${orderId}`);
    const route = await findTable({ name: "Order operations" });
    expect(within(route).queryByRole("button", { name: /operation 30/ })).not.toBeInTheDocument();
    expect(within(route).getAllByRole("button")).toHaveLength(2);
  });

  it("shows the service's refusal and keeps the operation as it was", async () => {
    detailFetch(started(), (url, init) =>
      init?.method === "POST" && url.includes("/operations/") ? problem(412, "conflict_version", "order was changed by another user; current version is 7") : undefined,
    );
    renderAt(`/production/${orderId}`);
    const route = await findTable({ name: "Order operations" });
    await userEvent.click(within(route).getByRole("button", { name: "Finish operation 20" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The command was refused");
    expect(alert).toHaveTextContent("conflict_version");
    expect(within(screen.getByRole("table", { name: "Order operations" })).getByRole("button", { name: "Finish operation 20" })).toBeInTheDocument();
  });

  it("has no operation actions before the order is started", async () => {
    detailFetch(order({ status: "Released" }));
    renderAt(`/production/${orderId}`);
    const route = await findTable({ name: "Order operations" });
    expect(within(route).queryByRole("button")).not.toBeInTheDocument();
    expect(within(route).queryByText("Feedback action")).not.toBeInTheDocument();
  });
});

describe("Order quantity rules on the create form", () => {
  it("sends the rules given and leaves out the empty ones", () => {
    const input = toInput({ ...initialValues(), quantity: "48", minimumQty: "24", multiple: "12" });
    expect(input.minimumQty).toBe("24");
    expect(input.multiple).toBe("12");
    expect(input.maximumQty).toBeUndefined();
  });

  it("checks the format of the rules", () => {
    const errors = validateOrder({ ...initialValues(), minimumQty: "-1", maximumQty: "1.1234567", multiple: "12" });
    expect(errors.minimumQty).toMatch(/up to 6 decimals/);
    expect(errors.maximumQty).toMatch(/up to 6 decimals/);
    expect(errors.multiple).toBeUndefined();
  });

  it("puts the service's quantity refusal on the quantity field", async () => {
    const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;
    const fetchMock = stubFetch((url) =>
      url.includes("/engineering/resources") ? json({ items: [{ id: uuid(5), code: "CNC-1", name: "CNC mill" }] }) : problem(422, "PRODUCTION_ORDER_QUANTITY", "quantity 121 is not a multiple of 12"),
    );
    renderAt("/production/new");
    const form = await screen.findByRole("form", { name: "New production order" });
    fireEvent.change(within(form).getByLabelText("Product"), { target: { value: uuid(1) } });
    await userEvent.type(within(form).getByLabelText("Quantity"), "121");
    fireEvent.change(within(form).getByLabelText("Site"), { target: { value: uuid(2) } });
    fireEvent.change(within(form).getByLabelText("Warehouse"), { target: { value: uuid(3) } });
    await userEvent.type(within(form).getByLabelText(/Order multiple/), "12");
    await userEvent.click(within(form).getByRole("button", { name: "Add component" }));
    fireEvent.change(within(form).getByLabelText("Item"), { target: { value: uuid(4) } });
    await userEvent.type(within(form).getByLabelText("Quantity per unit"), "1");
    fireEvent.change(within(form).getByLabelText("Resource"), { target: { value: uuid(5) } });
    await userEvent.click(within(form).getByRole("button", { name: "Create production order" }));

    await waitFor(() => expect(calls(fetchMock).some(([, init]) => init?.method === "POST")).toBe(true));
    const body = JSON.parse(String(calls(fetchMock).find(([, init]) => init?.method === "POST")![1]?.body)) as Record<string, unknown>;
    expect(body.multiple).toBe("12");
    expect(body).not.toHaveProperty("minimumQty");
    await waitFor(() => expect(screen.getByText(/The order was not created/)).toBeInTheDocument());
    expect(screen.getByText(/PRODUCTION_ORDER_QUANTITY/)).toBeInTheDocument();
    const quantity = within(form).getByLabelText("Quantity");
    expect(quantity).toHaveAttribute("aria-invalid", "true");
    expect(within(form).getByText("quantity 121 is not a multiple of 12")).toBeInTheDocument();
  }, 60_000);
});
