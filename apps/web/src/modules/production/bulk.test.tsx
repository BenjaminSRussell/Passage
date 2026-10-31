import { findAlert, findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { bulkStatus, canMerge, canSplit, mergeOrders, splitOrder, STATUSES } from "./api/client";
import { loggedCommandLabel } from "./labels";
import production from "./module";
import { validateSplit } from "./SplitPanel";
import { json, logEntry, order, orderId, problem, summary } from "./testData";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;
const posts = (mock: ReturnType<typeof vi.fn>) => calls(mock).filter(([, init]) => init?.method === "POST");
const header = (init: RequestInit | undefined, name: string) => (init?.headers as Record<string, string>)[name];

function renderAt(path: string) {
  const at = { pathname: path };
  function Probe() {
    at.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Probe />
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
  return { location: at, ...view };
}

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;
const first = summary({ status: "Scheduled", version: 3 });
const second = summary({ id: uuid(2), orderNumber: "PO-000002", status: "Estimated", version: 2 });
const third = summary({ id: uuid(3), orderNumber: "PO-000003", status: "Started", version: 5 });

describe("Bulk status update on the order list (FR-21.12)", () => {
  it("applies the command to the selected orders and lists every result", async () => {
    const fetchMock = stubFetch((url) =>
      url.endsWith(":bulk-status")
        ? json({
            command: "release",
            succeeded: 1,
            failed: 1,
            code: "PRODUCTION_BULK_PARTIAL",
            results: [
              { orderId: first.id, ok: true, status: "Released", version: 4 },
              { orderId: second.id, ok: false, code: "PRODUCTION_ILLEGAL_TRANSITION", detail: "transition Estimated via release is not allowed" },
            ],
          })
        : json({ items: [first, second, third] }),
    );
    const { container, location } = renderAt("/production");
    const table = await findTable({ name: "Production orders" });
    await waitFor(() => expect(within(table).getByText("PO-000003")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Apply to selected" })).toBeDisabled();

    await userEvent.click(screen.getByRole("checkbox", { name: "Select PO-000001" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select PO-000002" }));
    // Ticking a box selects the order without opening it.
    expect(location.pathname).toBe("/production");
    expect(screen.getByText(/2 selected/)).toBeInTheDocument();
    expect(screen.getByLabelText("Bulk command")).toHaveValue("release");
    await userEvent.click(screen.getByRole("button", { name: "Apply to selected" }));

    const results = await findTable({ name: "Bulk update results" });
    expect(screen.getByText("Release: 1 succeeded, 1 failed")).toBeInTheDocument();
    expect(within(results).getByText("PO-000001")).toBeInTheDocument();
    expect(within(results).getByText("Done")).toBeInTheDocument();
    expect(within(results).getByText("Failed")).toBeInTheDocument();
    expect(within(results).getByText("transition Estimated via release is not allowed (PRODUCTION_ILLEGAL_TRANSITION)")).toBeInTheDocument();

    const [[url, init]] = posts(fetchMock);
    expect(url).toBe("/api/v1/production/orders:bulk-status");
    expect(JSON.parse(String(init?.body))).toEqual({ command: "release", orderIds: [first.id, second.id] });
    expect(header(init, "Idempotency-Key")).toBeTruthy();
    // The list is read again and the selection cleared.
    await waitFor(() => expect(calls(fetchMock).filter(([u]) => u.includes("?limit=50"))).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "Apply to selected" })).toBeDisabled());
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("asks before cancelling orders in bulk", async () => {
    const fetchMock = stubFetch((url) =>
      url.endsWith(":bulk-status")
        ? json({ command: "cancel", succeeded: 1, failed: 0, results: [{ orderId: first.id, ok: true, status: "Cancelled", version: 4 }] })
        : json({ items: [first] }),
    );
    renderAt("/production");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select PO-000001" }));
    await userEvent.selectOptions(screen.getByLabelText("Bulk command"), "cancel");
    await userEvent.click(screen.getByRole("button", { name: "Apply to selected" }));
    expect(screen.getByText("Cancel 1 orders?")).toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Yes, cancel them" }));
    expect(await screen.findByText("Cancel: 1 succeeded, 0 failed")).toBeInTheDocument();
    expect(JSON.parse(String(posts(fetchMock)[0][1]?.body)).command).toBe("cancel");
  });

  it("shows a refused batch and keeps the selection for a retry with the same key", async () => {
    const fetchMock = stubFetch((url) =>
      url.endsWith(":bulk-status") ? problem(403, "AUTH-0003", "missing duty production.order.release") : json({ items: [first] }),
    );
    renderAt("/production");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select PO-000001" }));
    await userEvent.click(screen.getByRole("button", { name: "Apply to selected" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("missing duty production.order.release (AUTH-0003)");
    expect(screen.getByRole("checkbox", { name: "Select PO-000001" })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Apply to selected" }));
    const [a, b] = posts(fetchMock);
    expect(header(a[1], "Idempotency-Key")).toBe(header(b[1], "Idempotency-Key"));
  });
});

describe("Merge on the order list (FR-21.15)", () => {
  it("merges the selected orders into the first one selected", async () => {
    const fetchMock = stubFetch((url) =>
      url.endsWith(":merge")
        ? json({ survivor: order({ id: second.id, orderNumber: "PO-000002", quantity: "200", version: 3 }), merged: [first.id] })
        : json({ items: [first, second, third] }),
    );
    renderAt("/production");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select PO-000002" }));
    expect(screen.getByRole("button", { name: "Merge selected" })).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select PO-000001" }));
    expect(screen.getByText(/A merge keeps PO-000002/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Merge selected" }));
    expect(screen.getByText("Merge 2 orders into PO-000002?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Yes, merge them" }));
    expect(await screen.findByText("Merged 1 order into PO-000002; quantity 200.")).toBeInTheDocument();
    const [[url, init]] = posts(fetchMock);
    expect(url).toBe("/api/v1/production/orders:merge");
    expect(JSON.parse(String(init?.body))).toEqual({
      orders: [
        { orderId: second.id, version: 2 },
        { orderId: first.id, version: 3 },
      ],
    });
  });

  it("does not offer a merge that includes a started order", async () => {
    stubFetch(() => json({ items: [first, third] }));
    renderAt("/production");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select PO-000001" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select PO-000003" }));
    expect(screen.getByRole("button", { name: "Merge selected" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Apply to selected" })).toBeEnabled();
  });

  it("shows why the service refused the merge", async () => {
    stubFetch((url) =>
      url.endsWith(":merge")
        ? problem(422, "PRODUCTION_MERGE_REFUSED", "merge refused: order PO-000001 has posted journals")
        : json({ items: [first, second] }),
    );
    renderAt("/production");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select PO-000001" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select PO-000002" }));
    await userEvent.click(screen.getByRole("button", { name: "Merge selected" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, merge them" }));
    expect(await findAlert()).toHaveTextContent("merge refused: order PO-000001 has posted journals (PRODUCTION_MERGE_REFUSED)");
  });
});

/** Answers the detail page's reads; `split` answers the :split call. */
function detailFetch(current = order({ status: "Estimated", version: 2 }), split?: Handler) {
  return stubFetch((url, init) => {
    if (url.endsWith(":split") && split) {
      return split(url, init);
    }
    if (url.includes("?")) {
      return json({ items: [] });
    }
    if (url.endsWith("/status-log")) {
      return json({ items: [logEntry({ command: "split", from: "Estimated", to: "Estimated" })] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    if (url.endsWith("/journals") || url.endsWith("/as-built")) {
      return json({ items: [] });
    }
    return json(current);
  });
}

describe("Split on the order detail (FR-21.15)", () => {
  it("splits a quantity off into a new order and links to it", async () => {
    const created = order({ id: uuid(7), orderNumber: "PO-000007", quantity: "40", originType: "Split", splitFromOrderId: orderId });
    const fetchMock = detailFetch(undefined, () =>
      json({ original: order({ status: "Estimated", quantity: "60", version: 3 }), new: created }, 201),
    );
    const { container, location } = renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Split order" });
    await userEvent.type(within(form).getByLabelText("Split quantity"), "40");
    await userEvent.type(within(form).getByLabelText(/Order multiple/), "10");
    await userEvent.click(within(form).getByRole("button", { name: "Split off" }));

    expect(await screen.findByText("Split off PO-000007 for 40.")).toBeInTheDocument();
    expect(screen.getByText("Version 3 · created 2026-10-26")).toBeInTheDocument();
    const [[url, init]] = posts(fetchMock);
    expect(url).toBe(`/api/v1/production/orders/${orderId}:split`);
    expect(header(init, "If-Match")).toBe('"2"');
    expect(header(init, "Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toEqual({ splitQty: "40", multiple: "10" });
    // The status log names the split.
    expect(await screen.findByRole("cell", { name: "Split" })).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: "Open PO-000007" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${uuid(7)}`));
  });

  it("checks the quantity before calling the service", async () => {
    const fetchMock = detailFetch();
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Split order" });
    await userEvent.type(within(form).getByLabelText("Split quantity"), "100");
    await userEvent.click(within(form).getByRole("button", { name: "Split off" }));
    expect(await within(form).findByText("Enter less than the order quantity (100); the original keeps the rest.")).toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(0);
  });

  it("shows the service's refusal", async () => {
    detailFetch(undefined, () => problem(422, "PRODUCTION_SPLIT_QUANTITY", "split quantity 15 is not a multiple of 10"));
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Split order" });
    await userEvent.type(within(form).getByLabelText("Split quantity"), "15");
    await userEvent.click(within(form).getByRole("button", { name: "Split off" }));
    const message = await screen.findByText("split quantity 15 is not a multiple of 10 (PRODUCTION_SPLIT_QUANTITY)");
    expect(message.closest('[role="alert"]')).toHaveTextContent("The split was refused");
  });

  it("splits a Started order with the allocation of its posted consumption and WIP (BR-21.11)", async () => {
    const created = order({ id: uuid(8), orderNumber: "PO-000008", quantity: "40", status: "Started", originType: "Split", splitFromOrderId: orderId });
    const fetchMock = detailFetch(order({ status: "Started", version: 5, quantity: "100" }), () => json({ original: order({ status: "Started", version: 6, quantity: "60" }), new: created }, 201));
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Split order" });
    expect(within(form).getByLabelText("Posted consumption and WIP")).toHaveValue("proportional");
    await userEvent.type(within(form).getByLabelText("Split quantity"), "40");
    await userEvent.click(within(form).getByRole("button", { name: "Split off" }));
    expect(await screen.findByText("Split off PO-000008 for 40.")).toBeInTheDocument();
    const [[, init]] = posts(fetchMock);
    expect(JSON.parse(String(init?.body))).toEqual({ splitQty: "40", allocation: "proportional" });
  });

  it("sends keep-on-original when the user keeps the posted figures on the order", async () => {
    const fetchMock = detailFetch(order({ status: "Started", version: 5, quantity: "100" }), () => problem(409, "PRODUCTION_SPLIT_REFUSED", "the order has 1 draft journals; post or delete them before splitting"));
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Split order" });
    await userEvent.selectOptions(within(form).getByLabelText("Posted consumption and WIP"), "keep-on-original");
    expect(screen.getByText(/stay on this order; the new order starts with nothing posted/)).toBeInTheDocument();
    await userEvent.type(within(form).getByLabelText("Split quantity"), "10");
    await userEvent.click(within(form).getByRole("button", { name: "Split off" }));
    expect(await screen.findByText(/post or delete them before splitting \(PRODUCTION_SPLIT_REFUSED\)/)).toBeInTheDocument();
    const [[, init]] = posts(fetchMock);
    expect(JSON.parse(String(init?.body))).toEqual({ splitQty: "10", allocation: "keep-on-original" });
  });

  it("is not offered once the order has ended, and links a split or merged order to its origin", async () => {
    detailFetch(order({ status: "Cancelled", splitFromOrderId: uuid(4), mergedIntoOrderId: uuid(5) }));
    const { location } = renderAt(`/production/${orderId}`);
    await screen.findByRole("heading", { name: /Production order PO-000001/ });
    expect(screen.queryByRole("form", { name: "Split order" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open the order it was split from" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open the order it was merged into" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${uuid(5)}`));
  });
});

describe("split, merge and bulk client", () => {
  it("validates split input without floats", () => {
    expect(validateSplit({ splitQty: "40", minimumQty: "", multiple: "" }, "100")).toEqual({});
    expect(validateSplit({ splitQty: "99.999999", minimumQty: "", multiple: "" }, "100")).toEqual({});
    expect(validateSplit({ splitQty: "0", minimumQty: "", multiple: "" }, "100").splitQty).toMatch(/positive/);
    expect(validateSplit({ splitQty: "100.000001", minimumQty: "", multiple: "" }, "100").splitQty).toMatch(/less than/);
    expect(validateSplit({ splitQty: "1.1234567", minimumQty: "", multiple: "" }, "100").splitQty).toMatch(/6 decimals/);
    expect(validateSplit({ splitQty: "5", minimumQty: "-1", multiple: "x" }, "100")).toEqual({
      minimumQty: "Enter a quantity with up to 6 decimals, or leave it empty.",
      multiple: "Enter a quantity with up to 6 decimals, or leave it empty.",
    });
  });

  it("offers merge before Started and split up to Started", () => {
    expect(STATUSES.filter(canMerge)).toEqual(["Created", "Estimated", "Scheduled", "Released"]);
    expect(STATUSES.filter(canSplit)).toEqual(["Created", "Estimated", "Scheduled", "Released", "Started"]);
    expect(loggedCommandLabel.merge).toBe("Merge");
  });

  it("sends the documented requests", async () => {
    const fetchMock = stubFetch((url) =>
      url.endsWith(":bulk-status") ? json({}) : url.endsWith(":merge") ? json({ survivor: order(), merged: [] }) : json({ original: order(), new: order() }, 201),
    );
    const result = await bulkStatus([orderId], "start", "k1");
    expect(result).toEqual({ command: "start", succeeded: 0, failed: 0, code: undefined, detail: undefined, results: [] });
    await mergeOrders([{ orderId, version: 1 }, { orderId: uuid(2), version: 4 }], "k2");
    await splitOrder(orderId, 3, { splitQty: "5" }, "k3");
    const sent = calls(fetchMock);
    expect(sent.map(([u]) => u)).toEqual([
      "/api/v1/production/orders:bulk-status",
      "/api/v1/production/orders:merge",
      `/api/v1/production/orders/${orderId}:split`,
    ]);
    expect(sent.map(([, init]) => header(init, "Idempotency-Key"))).toEqual(["k1", "k2", "k3"]);
    expect(header(sent[2][1], "If-Match")).toBe('"3"');
  });
});
