import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import {
  assignAsBuilt,
  detachAsBuilt,
  listOrderAsBuilt,
  listOrders,
  queryAsBuilt,
  type AsBuiltRecord,
  type PostingProfile,
} from "./api/client";
import { resetResourceCache } from "../mfg-shared/resources";
import { emptyAsBuilt, toAsBuiltInput } from "./AsBuiltPanel";
import { formKey, initialValues, toInput, validateOrder } from "./CreateOrderPage";
import { describeReference, varianceLabel } from "./labels";
import production, { reworkValues } from "./module";
import { blankProfile, validateProfile } from "./PostingProfilesPage";
import { json, order, orderId, problem, summary } from "./testData";

beforeEach(() => resetResourceCache());
afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderAt(path: string) {
  const at = { pathname: path, state: null as unknown };
  function Probe() {
    const location = useLocation();
    at.pathname = location.pathname;
    at.state = location.state;
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
const reworkId = uuid(77);

function record(overrides: Partial<AsBuiltRecord> = {}): AsBuiltRecord {
  return {
    id: uuid(501),
    orderId,
    orderNumber: "PO-000001",
    finishedItemId: uuid(900),
    finishedSerialOrBatch: "S-1",
    componentItemId: "RM-STEEL",
    tracking: "serial",
    componentSerialOrBatch: "M-1",
    qty: "1",
    assignedBy: "operator-1",
    assignedAt: "2026-10-26T14:00:00Z",
    ...overrides,
  };
}

describe("rework and as-built API client", () => {
  it("filters the order list by origin and by the original order", async () => {
    const fetchMock = stubFetch(() => json({ items: [summary({ originType: "Rework" })] }));
    const page = await listOrders({ originType: "Rework", reworkOf: orderId, limit: 10 });
    expect(page.items[0].originType).toBe("Rework");
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/orders?limit=10&originType=Rework&reworkOf=${orderId}`);
  });

  it("assigns with an Idempotency-Key, detaches with :detach and queries both directions", async () => {
    const fetchMock = stubFetch(() => json({ items: [record()] }));
    await assignAsBuilt(orderId, { finishedSerialOrBatch: "S-1", componentItemId: "c", tracking: "serial", componentSerialOrBatch: "M-1" }, "k-ab");
    await detachAsBuilt(uuid(501));
    expect(await queryAsBuilt({ componentSerialOrBatch: "M-1" })).toHaveLength(1);
    await queryAsBuilt({ finishedSerialOrBatch: "S 2/a" });
    const [assignUrl, assignInit] = calls(fetchMock)[0];
    expect(assignUrl).toBe(`/api/v1/production/orders/${orderId}/as-built`);
    expect(assignInit?.headers).toMatchObject({ "Idempotency-Key": "k-ab" });
    expect(calls(fetchMock)[1][0]).toBe(`/api/v1/production/as-built/${uuid(501)}:detach`);
    expect(calls(fetchMock)[1][1]?.method).toBe("POST");
    expect(calls(fetchMock)[2][0]).toBe("/api/v1/production/as-built?componentSerialOrBatch=M-1");
    expect(calls(fetchMock)[3][0]).toBe("/api/v1/production/as-built?finishedSerialOrBatch=S+2%2Fa");
  });

  it("reads an order's records and tolerates a body without items", async () => {
    stubFetch(() => json({ orderId }));
    expect(await listOrderAsBuilt(orderId)).toEqual([]);
  });
});

describe("rework helpers", () => {
  it("validates the as-built assignment like the service", () => {
    expect(toAsBuiltInput({ ...emptyAsBuilt("RM"), finishedSerialOrBatch: "S-1", componentSerialOrBatch: "M-1" })).toEqual({
      input: { finishedSerialOrBatch: "S-1", componentItemId: "RM", tracking: "serial", componentSerialOrBatch: "M-1" },
      errors: {},
    });
    const batch = toAsBuiltInput({ ...emptyAsBuilt("RM"), tracking: "batch", finishedSerialOrBatch: "S-1", componentSerialOrBatch: "B-1", qty: "2.5" });
    expect(batch.input?.qty).toBe("2.5");
    const bad = toAsBuiltInput({ ...emptyAsBuilt(""), tracking: "batch", finishedSerialOrBatch: " S-1", componentSerialOrBatch: "", qty: "0" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["componentItemId", "componentSerialOrBatch", "finishedSerialOrBatch", "qty"]);
  });

  it("maps a rework order to the CreateOrder body and the reference errors back to the form", () => {
    const v = {
      ...initialValues(),
      ...reworkValues(order({ productId: uuid(1), siteId: uuid(2), warehouseId: uuid(3) })),
      quantity: "5",
      operations: [{ operNumber: "10", resourceId: uuid(5), setupTimeH: "", runTimeHPer: "0.4", laborRate: "30", overheadRate: "" }],
    };
    expect(validateOrder(v)).toEqual({});
    const body = toInput(v);
    expect(body.originType).toBe("Rework");
    expect(body.reworkReference).toEqual({ type: "order", id: orderId });
    expect(body.bom).toEqual([{ lineNo: "10", itemId: uuid(1), qtyPer: "1", scrapPct: undefined, unitCost: undefined, flushing: undefined }]);
    expect(toInput({ ...v, referenceType: "", referenceId: "" }).reworkReference).toBeUndefined();
    expect(toInput({ ...v, rework: false })).not.toHaveProperty("originType");
    expect(validateOrder({ ...v, referenceId: "PO-000001" })).toHaveProperty("referenceId");
    expect(validateOrder({ ...v, referenceType: "serial", referenceId: "S-1 " })).toHaveProperty("referenceId");
    expect(validateOrder({ ...v, referenceType: "batch", referenceId: "B-2026-07" })).toEqual({});
    expect(formKey("reworkReference.id")).toBe("referenceId");
    expect(formKey("reworkReference.type")).toBe("referenceType");
    expect(describeReference({ type: "batch", id: "B-7" })).toBe("Batch B-7");
    expect(varianceLabel.rework).toBe("Rework");
  });

  it("keeps the rework variance account optional on posting profiles", () => {
    const filled = Object.fromEntries(Object.entries(blankProfile()).map(([k, v]) => [k, v === "" ? "5000" : v])) as unknown as PostingProfile;
    expect(validateProfile({ ...filled, reworkVarianceAccount: "" })).toEqual({});
    expect(validateProfile({ ...filled, reworkVarianceAccount: " 5650" })).toHaveProperty("reworkVarianceAccount");
    expect(validateProfile({ ...filled, reworkVarianceAccount: "9".repeat(65) })).toHaveProperty("reworkVarianceAccount");
  });
});

describe("Production orders list: rework", () => {
  it("lists rework orders on their own and marks them", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("originType=Rework") ? json({ items: [] }) : json({ items: [summary(), summary({ id: reworkId, orderNumber: "PO-000002", originType: "Rework" })] }),
    );
    renderAt("/production");
    const table = await findTable({ name: "Production orders" });
    await waitFor(() => expect(within(table).getByText("PO-000002")).toBeInTheDocument());
    expect(within(table).getByText("Rework")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Origin"), "Rework");
    await waitFor(() => expect(calls(fetchMock).at(-1)?.[0]).toBe("/api/v1/production/orders?limit=50&originType=Rework"));
    expect(await screen.findByRole("heading", { name: "No rework orders" })).toBeInTheDocument();
  });
});

describe("Production order detail: rework and as-built", () => {
  /** Answers a request the test cares about, or undefined to fall through to the defaults. */
  type Extra = (url: string, init?: RequestInit) => Response | undefined;

  function detailFetch(current: ReturnType<typeof order>, extra?: Extra) {
    return stubFetch((url, init) => {
      if (url.includes("/engineering/resources")) {
        return json({ items: [{ id: uuid(5), code: "CNC-1", name: "CNC mill" }] });
      }
      const handled = extra?.(url, init);
      if (handled) {
        return handled;
      }
      if (url.endsWith("/status-log") || url.endsWith("/journals") || url.endsWith("/as-built")) {
        return json({ orderId, items: [] });
      }
      if (url.includes("reworkOf=")) {
        return json({ items: [] });
      }
      if (url.endsWith("/costs")) {
        return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
      }
      return json(current);
    });
  }

  it("starts a rework order prefilled from the original and creates it with its reference", async () => {
    const fetchMock = detailFetch(order({ status: "Ended", qtyRaf: "100", itemGroup: "FG" }), (_url, init) =>
      init?.method === "POST" ? json(order({ id: reworkId, orderNumber: "PO-000002", originType: "Rework" }), 201) : undefined,
    );
    const { location } = renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Create rework order" }));
    await waitFor(() => expect(location.pathname).toBe("/production/new"));
    expect(await screen.findByRole("heading", { name: "New rework order" })).toBeInTheDocument();
    expect(screen.getByLabelText("Order type")).toHaveValue("rework");
    expect(screen.getByLabelText("Rework of")).toHaveValue("order");
    expect(screen.getByLabelText("Reference")).toHaveValue(orderId);
    expect(screen.getByLabelText("Product")).toHaveAttribute("data-value", order().productId);

    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    const component = screen.getByRole("group", { name: "Component 1" });
    expect(within(component).getByLabelText("Item")).toHaveAttribute("data-value", order().productId);
    // The test data's ids are not UUIDs; the service needs them to be.
    for (const [label, value] of [["Product", uuid(1)], ["Site", uuid(2)], ["Warehouse", uuid(3)]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.change(within(component).getByLabelText("Item"), { target: { value: uuid(1) } });
    const operation = screen.getByRole("group", { name: "Operation 1" });
    fireEvent.change(within(operation).getByLabelText("Resource"), { target: { value: uuid(5) } });
    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${reworkId}`));
    const post = calls(fetchMock).find(([, init]) => init?.method === "POST");
    const body = JSON.parse(String(post?.[1]?.body));
    expect(body.originType).toBe("Rework");
    expect(body.reworkReference).toEqual({ type: "order", id: orderId });
    expect(body.itemGroup).toBe("FG");
  });

  it("shows a refused rework order with its code and maps the reference error to the field", async () => {
    stubFetch((url) =>
      url.includes("/engineering/resources")
        ? json({ items: [{ id: uuid(5), code: "CNC-1", name: "CNC mill" }] })
        : problem(400, "COM-0001", "request failed validation", { "reworkReference.id": "must be the id of the original production order" }),
    );
    renderAt("/production/new");
    await userEvent.selectOptions(screen.getByLabelText("Order type"), "rework");
    await userEvent.selectOptions(screen.getByLabelText("Rework of"), "serial");
    for (const [label, value] of [["Product", uuid(1)], ["Site", uuid(2)], ["Warehouse", uuid(3)], ["Quantity", "5"], ["Reference", "S-0042"]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    await userEvent.click(screen.getByRole("button", { name: "Add component" }));
    const component = screen.getByRole("group", { name: "Component 1" });
    fireEvent.change(within(component).getByLabelText("Item"), { target: { value: uuid(4) } });
    fireEvent.change(within(component).getByLabelText("Quantity per unit"), { target: { value: "1" } });
    const operation = screen.getByRole("group", { name: "Operation 1" });
    fireEvent.change(within(operation).getByLabelText("Resource"), { target: { value: uuid(5) } });
    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    await waitFor(() => expect(screen.getByLabelText("Reference")).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByLabelText("Reference")).toHaveAccessibleDescription(/must be the id of the original production order/);
    const summaryAlert = screen.getAllByRole("alert").find((a) => a.textContent?.includes("The order was not created"));
    expect(summaryAlert).toHaveTextContent("COM-0001");
  });

  it("validates the reference before sending", async () => {
    const fetchMock = stubFetch(() => json(order()));
    renderAt("/production/new");
    await userEvent.selectOptions(screen.getByLabelText("Order type"), "rework");
    await userEvent.selectOptions(screen.getByLabelText("Rework of"), "order");
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PO-000001" } });
    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    expect(await screen.findByText("Enter the original production order's id (a UUID).")).toBeInTheDocument();
    expect(calls(fetchMock).filter(([, init]) => init?.method === "POST")).toEqual([]);
  });

  it("shows the reference of a rework order and the rework orders of an original", async () => {
    detailFetch(order({ status: "Started", originType: "Rework", reworkReference: { type: "order", id: uuid(9) } }), (url) =>
      url.includes("reworkOf=") ? json({ items: [summary({ id: reworkId, orderNumber: "PO-000002", originType: "Rework", status: "Released", quantity: "5" })] }) : undefined,
    );
    const { location, container } = renderAt(`/production/${orderId}`);
    const reworks = await findTable({ name: "Rework orders of this order" });
    await waitFor(() => expect(within(reworks).getByText("PO-000002")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Create rework order" })).not.toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Open the original order" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${uuid(9)}`));
  });

  it("opens a rework order from the original", async () => {
    detailFetch(order({ status: "Ended", qtyRaf: "100" }), (url) =>
      url.includes("reworkOf=") ? json({ items: [summary({ id: reworkId, orderNumber: "PO-000002", originType: "Rework" })] }) : undefined,
    );
    const { location } = renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "PO-000002" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${reworkId}`));
  });

  it("lists, assigns and detaches as-built records (AC-21.14)", async () => {
    let rows = [record()];
    const fetchMock = detailFetch(order({ status: "Started", version: 6 }), (url, init) => {
      if (url.endsWith("/as-built") && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        const created = record({ id: uuid(502), finishedSerialOrBatch: body.finishedSerialOrBatch, componentSerialOrBatch: body.componentSerialOrBatch });
        rows = [...rows, created];
        return json(created, 201);
      }
      if (url.endsWith(":detach")) {
        rows = rows.filter((r) => r.id !== uuid(501));
        return json(record({ detachedAt: "2026-10-26T15:00:00Z", detachedBy: "operator-1" }));
      }
      return url.endsWith("/as-built") ? json({ orderId, items: rows }) : undefined;
    });
    const { container } = renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "As-built records" });
    await waitFor(() => expect(within(table).getByText("M-1")).toBeInTheDocument());
    expect(checkAxeAA(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Assign as-built" });
    await userEvent.click(within(form).getByRole("button", { name: "Assign component" }));
    expect(await within(form).findByText(/Enter the finished serial or batch number/)).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "POST")).toBe(false);

    await userEvent.type(within(form).getByLabelText("Finished serial or batch"), "S-2");
    await userEvent.selectOptions(within(form).getByLabelText("Component"), "RM-BOLT");
    await userEvent.type(within(form).getByLabelText("Component serial or batch"), "M-2");
    await userEvent.click(within(form).getByRole("button", { name: "Assign component" }));
    expect(await screen.findByText("M-2 assigned to S-2.")).toBeInTheDocument();
    const assign = calls(fetchMock).find(([url, init]) => url.endsWith("/as-built") && init?.method === "POST");
    expect(JSON.parse(String(assign?.[1]?.body))).toEqual({
      finishedSerialOrBatch: "S-2",
      componentItemId: "RM-BOLT",
      tracking: "serial",
      componentSerialOrBatch: "M-2",
    });
    expect((assign?.[1]?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);

    await userEvent.click(screen.getByRole("button", { name: "Detach M-1" }));
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(calls(fetchMock).some(([url]) => url.endsWith(":detach"))).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Detach M-1" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, detach it" }));
    expect(await screen.findByText("M-1 detached from S-1.")).toBeInTheDocument();
    await waitFor(() => expect(within(screen.getByRole("table", { name: "As-built records" })).queryByText("M-1")).not.toBeInTheDocument());
  });

  it("shows a refused assignment with its code and offers no assignment on an ended order", async () => {
    detailFetch(order({ status: "ReportedAsFinished" }), (url, init) =>
      url.endsWith("/as-built") && init?.method === "POST"
        ? problem(422, "SERIAL_DUPLICATE", "component serial M-1 is already assigned to a finished serial")
        : undefined,
    );
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Assign as-built" });
    await userEvent.type(within(form).getByLabelText("Finished serial or batch"), "S-2");
    await userEvent.type(within(form).getByLabelText("Component serial or batch"), "M-1");
    await userEvent.click(within(form).getByRole("button", { name: "Assign component" }));
    const alert = await screen.findByText(/SERIAL_DUPLICATE/);
    expect(alert.closest("[role=alert]")).toHaveTextContent("The as-built change was refused");
  });

  it("keeps as-built read-only outside Started and Reported as finished", async () => {
    detailFetch(order({ status: "Ended" }), (url) => (url.endsWith("/as-built") ? json({ orderId, items: [record()] }) : undefined));
    renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "As-built records" });
    await waitFor(() => expect(within(table).getByText("M-1")).toBeInTheDocument());
    expect(screen.queryByRole("form", { name: "Assign as-built" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Detach M-1" })).not.toBeInTheDocument();
    expect(screen.getByText("Components are assigned to finished serials while the order is Started or Reported as finished.")).toBeInTheDocument();
  });
});

describe("As-built and where-used inquiry", () => {
  it("traces the as-built of a finished serial and the where-used of a component serial", async () => {
    const fetchMock = stubFetch((url) => {
      if (!url.includes("/as-built")) {
        return url.includes("?") || url.endsWith("/status-log") || url.endsWith("/journals")
          ? json({ items: [] })
          : url.endsWith("/costs")
            ? json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] })
            : json(order());
      }
      return url.includes("finishedSerialOrBatch=S-2")
        ? json({ items: [record({ finishedSerialOrBatch: "S-2", componentSerialOrBatch: "M-2" })] })
        : json({ items: [record()] });
    });
    const { container, location } = renderAt("/production/as-built");
    await userEvent.type(screen.getByLabelText("Serial or batch number"), "S-2");
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    const asBuilt = await findTable({ name: "As-built of S-2" });
    await waitFor(() => expect(within(asBuilt).getByText("M-2")).toBeInTheDocument());
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/as-built?finishedSerialOrBatch=S-2");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.selectOptions(screen.getByLabelText("Trace"), "where-used");
    await userEvent.clear(screen.getByLabelText("Serial or batch number"));
    await userEvent.type(screen.getByLabelText("Serial or batch number"), "M-1");
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    const whereUsed = await findTable({ name: "Where-used of M-1" });
    await waitFor(() => expect(within(whereUsed).getByText("S-1")).toBeInTheDocument());
    expect(calls(fetchMock)[1][0]).toBe("/api/v1/production/as-built?componentSerialOrBatch=M-1");

    await userEvent.click(within(whereUsed).getByRole("button", { name: "PO-000001" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${orderId}`));
  });

  it("validates the number, explains an empty result and shows a failure", async () => {
    let fail = false;
    const fetchMock = stubFetch(() => (fail ? problem(403, "AUTH-0003", "missing duty production.order.read") : json({ items: [] })));
    renderAt("/production/as-built");
    expect(screen.getByText("Enter a serial or batch number to trace it.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await screen.findByText(/Enter a serial or batch number \(up to 100/)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    await userEvent.type(screen.getByLabelText("Serial or batch number"), "X-9");
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await screen.findByRole("heading", { name: "Nothing recorded for X-9" })).toBeInTheDocument();

    fail = true;
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not trace the serial");
    expect(alert).toHaveTextContent("AUTH-0003");
  });

  it("shows a loading table while the trace runs", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    renderAt("/production/as-built");
    await userEvent.type(screen.getByLabelText("Serial or batch number"), "S-1");
    await userEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await screen.findByRole("table", { name: "As-built of S-1" })).toHaveAttribute("aria-busy", "true");
  });
});

describe("Posting profiles: rework variance account", () => {
  it("saves the optional rework variance account", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init?.method === "PUT" ? json({ ...JSON.parse(String(init.body)), legalEntityId: uuid(1), version: 1, updatedBy: "admin", updatedAt: "2026-10-26T00:00:00Z" }, 201) : json({ items: [] }),
    );
    renderAt("/production/posting-profiles");
    await screen.findByRole("heading", { name: "No posting profiles yet" });
    const form = screen.getByRole("form", { name: "Posting profile" });
    for (const input of within(form).getAllByRole("textbox")) {
      if ((input as HTMLInputElement).value === "") {
        fireEvent.change(input, { target: { value: "5000" } });
      }
    }
    fireEvent.change(within(form).getByLabelText("Rework variance"), { target: { value: "5650" } });
    await userEvent.click(within(form).getByRole("button", { name: "Save posting profile" }));
    await screen.findByText(/saved \(version 1\)/);
    const put = calls(fetchMock).find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(String(put?.[1]?.body)).reworkVarianceAccount).toBe("5650");
  });
});
