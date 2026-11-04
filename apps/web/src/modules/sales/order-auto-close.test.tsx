import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import sales from "./module";
import { autoCloseRequest, autoCloseText } from "./OrderAutoClosePage";

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

const run = (over: Record<string, unknown> = {}) => ({
  closed: 1,
  skipped: 0,
  more: false,
  items: [{ orderId: "77777777-0000-4000-8000-000000000007", orderNumber: "SO-0000007", legalEntityId: LE, lastChangedAt: "2027-03-01T09:00:00Z", version: 6, result: "CLOSED" }],
  ...over,
});

describe("close invoiced orders", () => {
  it("validates the form and builds the request", () => {
    expect(autoCloseRequest({ legalEntity: "", graceDays: "30", limit: "100" }, true)).toEqual({ request: { legalEntityId: undefined, graceDays: 30, limit: 100, dryRun: true } });
    const bad = autoCloseRequest({ legalEntity: "x", graceDays: "400", limit: "0" }, false);
    expect("errors" in bad && Object.keys(bad.errors).sort()).toEqual(["graceDays", "legalEntity", "limit"]);
    expect("errors" in autoCloseRequest({ legalEntity: "", graceDays: "-1", limit: "5" }, false)).toBe(true);
  });

  it("words the outcome", () => {
    expect(autoCloseText(run({ items: [], closed: 0 }) as never, true)).toBe("No order is ready to close.");
    expect(autoCloseText(run() as never, false)).toBe("1 order closed.");
    expect(autoCloseText(run({ closed: 2, skipped: 1, more: true }) as never, false)).toContain("1 changed in the meantime and was left open.");
    expect(autoCloseText(run({ closed: 0, items: [] }) as never, false)).toBe("No order was ready to close.");
  });

  it("previews without closing, then closes, and shows the orders", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/orders:auto-close")) {
        const dry = url.searchParams.get("dryRun") === "true";
        return { status: 200, body: dry ? run({ closed: 0, items: [{ ...run().items[0], result: "CANDIDATE", version: 5 }] }) : run() };
      }
      return { status: 404, body: {} };
    };
    const { container } = renderAt("/sales/order-auto-close", ["sales.order.read", "sales.order.write"]);
    expect(await screen.findByRole("heading", { name: "Close invoiced orders" })).toBeInTheDocument();
    expect(screen.getByText(/Preview lists the orders that would close/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("1 order is ready to close.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "SO-0000007" })).toHaveAttribute("href", "/sales/orders/77777777-0000-4000-8000-000000000007");
    let call = calls.at(-1)!;
    expect(call.method).toBe("POST");
    expect(call.url.searchParams.get("dryRun")).toBe("true");
    expect(call.url.searchParams.get("graceDays")).toBe("30");
    expect(call.url.searchParams.get("limit")).toBe("100");
    fireEvent.click(screen.getByRole("button", { name: "Close orders" }));
    expect(await screen.findByText("1 order closed.")).toBeInTheDocument();
    call = calls.at(-1)!;
    expect(call.url.searchParams.get("dryRun")).toBe("false");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state for a run that found nothing", async () => {
    handler = () => ({ status: 200, body: run({ closed: 0, items: [] }) });
    renderAt("/sales/order-auto-close", ["sales.order.read", "sales.order.write"]);
    fireEvent.click(await screen.findByRole("button", { name: "Preview" }));
    expect(await screen.findByText("No orders")).toBeInTheDocument();
    expect(screen.getByText("No order is ready to close.")).toBeInTheDocument();
  });

  it("stops on a bad field and sends nothing", async () => {
    handler = () => ({ status: 200, body: run() });
    renderAt("/sales/order-auto-close", ["sales.order.read", "sales.order.write"]);
    fireEvent.change(await screen.findByLabelText(/Unchanged for/), { target: { value: "999" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Enter whole days from 0 to 365.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("reports a failed run", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", title: "Forbidden", detail: "the legal entity is outside the caller's scope" } });
    renderAt("/sales/order-auto-close", ["sales.order.read", "sales.order.write"]);
    fireEvent.click(await screen.findByRole("button", { name: "Close orders" }));
    expect(await screen.findByText("The run could not finish")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument());
  });

  it("offers no run without the write duty, as the preview is the same POST", async () => {
    handler = () => ({ status: 200, body: run() });
    renderAt("/sales/order-auto-close", ["sales.order.read"]);
    expect(await screen.findByText(/needs the sales.order.write duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close orders" })).not.toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
