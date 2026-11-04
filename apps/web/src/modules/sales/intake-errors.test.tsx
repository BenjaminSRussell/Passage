import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { IntakeError } from "./api/client";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["sales.order.read", "sales.order.approve"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const ERROR_ID = "55555555-0000-4000-8000-000000000005";

function intakeError(overrides: Partial<IntakeError> = {}): IntakeError {
  return {
    id: ERROR_ID,
    legalEntityId: LE,
    channel: "EDI",
    externalRef: "PO-7001",
    requestHash: "abc123",
    errorCode: "SLS-0011",
    message: "product is not listed for the customer",
    status: "OPEN",
    attempts: 3,
    firstSeenAt: "2027-03-01T09:00:00Z",
    lastSeenAt: "2027-03-01T11:00:00Z",
    replayable: true,
    replayCount: 0,
    version: 3,
    ...overrides,
  };
}

describe("Intake errors list", () => {
  it("loads the open errors and filters by channel", async () => {
    handler = () => ({ status: 200, body: { items: [intakeError()] } });
    const { container } = renderAt("/sales/intake-errors");
    expect(screen.getByText("Loading…")).toBeTruthy();
    expect(await screen.findByRole("link", { name: "EDI" })).toHaveAttribute("href", `/sales/intake-errors/${ERROR_ID}`);
    expect(screen.getByText("PO-7001")).toBeInTheDocument();
    expect(screen.getByText("product is not listed for the customer")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("status")).toBe("OPEN");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Channel/), { target: { value: "PORTAL" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].url.searchParams.get("channel")).toBe("PORTAL");
    expect(calls[1].url.searchParams.get("status")).toBeNull();
  });

  it("shows the empty state and the error with a retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const { unmount } = renderAt("/sales/intake-errors");
    expect(await screen.findByText("No intake errors")).toBeInTheDocument();
    unmount();
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "down" } });
    renderAt("/sales/intake-errors");
    expect(await screen.findByText(/down \(COM-0500\)/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("refuses a malformed legal entity without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/intake-errors");
    await screen.findByText("No intake errors");
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByText("Enter a legal entity UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });
});

describe("Intake error detail", () => {
  it("needs a note, then closes the error at its ETag", async () => {
    handler = (_u, init) =>
      init.method === "POST" ? { status: 200, body: intakeError({ status: "RESOLVED", version: 4 }), etag: '"4"' } : { status: 200, body: intakeError(), etag: '"3"' };
    const { container } = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    expect(await screen.findByText("Intake error from EDI")).toBeInTheDocument();
    expect(screen.getByText("abc123")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Close error" }));
    expect(await screen.findByText("Give a note of 1 to 500 characters.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText(/Note/), { target: { value: "mapping corrected" } });
    fireEvent.click(screen.getByRole("button", { name: "Close error" }));
    expect(await screen.findByText(/Closed\./)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/intake-errors/${ERROR_ID}:resolve`);
    expect(post?.headers.get("If-Match")).toBe('"3"');
    expect(post?.body).toEqual({ note: "mapping corrected" });
  });

  it("explains a stale version and hides the form without the duty", async () => {
    handler = (_u, init) =>
      init.method === "POST" ? { status: 412, body: { code: "SLS-0006", detail: "changed by another user" } } : { status: 200, body: intakeError(), etag: '"3"' };
    const { unmount } = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Intake error from EDI");
    fireEvent.change(screen.getByLabelText(/Note/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Close error" }));
    expect(await screen.findByText("The intake error was changed by someone else")).toBeInTheDocument();
    unmount();
    renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read"]);
    expect(await screen.findByText(/Closing an intake error needs the sales.order.write duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close error" })).toBeNull();
  });

  it("shows a closed error without the form, and the load error", async () => {
    handler = () => ({ status: 200, body: intakeError({ status: "RESOLVED", resolvedAt: "2027-03-02T08:00:00Z", resolvedBy: "ops", resolutionNote: "mapping corrected" }), etag: '"4"' });
    const { unmount } = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    expect(await screen.findByText("mapping corrected")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close error" })).toBeNull();
    unmount();
    handler = () => ({ status: 404, body: { code: "SLS-0115", detail: "intake error not found" } });
    renderAt(`/sales/intake-errors/${ERROR_ID}`);
    expect(await screen.findByText(/intake error not found \(SLS-0115\)/)).toBeInTheDocument();
  });
});

describe("Replaying refused orders", () => {
  const ORDER_ID = "66666666-0000-4000-8000-000000000006";

  it("replays one error and shows the order it became", async () => {
    handler = (_u, init) =>
      init.method === "POST"
        ? { status: 200, body: { intakeError: intakeError({ status: "RESOLVED", replayedOrderId: ORDER_ID, replayable: false, version: 4 }), replayed: true, orderId: ORDER_ID, orderNumber: "SO-0042" }, etag: '"4"' }
        : { status: 200, body: intakeError(), etag: '"3"' };
    const { container } = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Intake error from EDI");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Replay order" }));
    expect(await screen.findByText("Replayed as order SO-0042. The error is closed.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/intake-errors/${ERROR_ID}:replay`);
  });

  it("says why intake refused the order again", async () => {
    handler = (_u, init) =>
      init.method === "POST"
        ? { status: 200, body: { intakeError: intakeError({ attempts: 4, replayCount: 1, version: 4 }), replayed: false, failure: { code: "SLS-0011", message: "product is still not listed" } } }
        : { status: 200, body: intakeError(), etag: '"3"' };
    renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Intake error from EDI");
    fireEvent.click(screen.getByRole("button", { name: "Replay order" }));
    expect(await screen.findByText("Intake refused the order again")).toBeInTheDocument();
    expect(screen.getByText(/product is still not listed \(SLS-0011\)/)).toBeInTheDocument();
  });

  it("explains a replay the service rejects, hides the button without the duty or a kept request", async () => {
    handler = (_u, init) =>
      init.method === "POST"
        ? { status: 409, body: { code: "SLS-0118", detail: "the refused request was not kept" } }
        : { status: 200, body: intakeError(), etag: '"3"' };
    const { unmount } = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Intake error from EDI");
    fireEvent.click(screen.getByRole("button", { name: "Replay order" }));
    expect(await screen.findByText("This error cannot be replayed")).toBeInTheDocument();
    unmount();
    const second = renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read"]);
    expect(await screen.findByText(/Replaying an order needs the sales.order.write duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replay order" })).toBeNull();
    second.unmount();
    handler = () => ({ status: 200, body: intakeError({ replayable: false }), etag: '"3"' });
    renderAt(`/sales/intake-errors/${ERROR_ID}`, ["sales.order.read", "sales.order.write"]);
    expect(await screen.findByText(/was not kept for this error/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replay order" })).toBeNull();
  });

  it("replays the open errors of the list filter and reports the counts", async () => {
    handler = (_u, init) =>
      init.method === "POST"
        ? { status: 200, body: { attempted: 3, replayed: 2, failed: 1, skipped: 4, items: [] } }
        : { status: 200, body: { items: [intakeError()] } };
    renderAt("/sales/intake-errors", ["sales.order.read", "sales.order.write"]);
    await screen.findByRole("link", { name: "EDI" });
    fireEvent.change(screen.getByLabelText(/Channel/), { target: { value: "EDI" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    fireEvent.click(screen.getByRole("button", { name: "Replay open errors" }));
    expect(await screen.findByText(/Replayed 2 of 3 orders; 1 refused again; 4 dead letters/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/intake-errors:replay");
    expect(post?.url.searchParams.get("channel")).toBe("EDI");
  });

  it("shows a failed bulk replay and hides the button without the duty", async () => {
    handler = (_u, init) =>
      init.method === "POST" ? { status: 500, body: { code: "COM-0500", detail: "down" } } : { status: 200, body: { items: [intakeError()] } };
    const { unmount } = renderAt("/sales/intake-errors", ["sales.order.read", "sales.order.write"]);
    await screen.findByRole("link", { name: "EDI" });
    fireEvent.click(screen.getByRole("button", { name: "Replay open errors" }));
    expect(await screen.findByText("The replay could not run")).toBeInTheDocument();
    unmount();
    renderAt("/sales/intake-errors", ["sales.order.read"]);
    await screen.findByRole("link", { name: "EDI" });
    expect(screen.queryByRole("button", { name: "Replay open errors" })).toBeNull();
  });
});
