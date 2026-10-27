import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ProcessMessage } from "./api/client";
import shopfloor from "./module";
import { validateProcessMessage, type Values } from "./NewProcessMessagePage";

const ORDER = "0195f0a0-0000-7000-8000-000000000040";
const MSG = "0195f0a0-0000-7000-8000-000000000041";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

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
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const message = (extra: Partial<ProcessMessage> = {}): ProcessMessage => ({
  id: MSG,
  source: "kiosk",
  sourceMessageId: "pi-7",
  categoryCode: "quality-result",
  orderId: ORDER,
  phasePath: "/mix/unit1/heat/hold",
  occurredAt: "2026-10-21T08:00:00Z",
  receivedAt: "2026-10-21T08:00:02Z",
  status: "partially-delivered",
  outOfLimit: true,
  values: [{ name: "ph", type: "numeric", value: "7.9", min: "6.5", max: "7.5", unit: "pH", outOfLimit: true }],
  deliveries: [
    { destination: "ebr", status: "delivered", attempts: 0, lastError: null, nextAttemptAt: null, deliveredAt: "2026-10-21T08:00:03Z" },
    { destination: "kafka", status: "delivered", attempts: 1, lastError: null, nextAttemptAt: null, deliveredAt: "2026-10-21T08:00:02Z" },
    { destination: "quality", status: "error", attempts: 5, lastError: "QMS unavailable", nextAttemptAt: null, deliveredAt: null },
  ],
  deletedAt: null,
  deletedBy: null,
  deleteReason: null,
  ...extra,
});

describe("Process message monitor", () => {
  it("lists messages with their status, out-of-limit flag and per-destination state", async () => {
    handler = (url) => (url.pathname.endsWith("/process-messages") ? json(200, { items: [message()] }) : undefined);
    const { container } = renderAt("/shopfloor/process-messages");
    const table = await screen.findByRole("table", { name: "Process messages" });
    await waitFor(() => expect(table).toHaveTextContent("Quality result"));
    expect(table).toHaveTextContent("Partially delivered");
    expect(table).toHaveTextContent("Out of limit");
    expect(table).toHaveTextContent("quality");
    expect(within(table).getByRole("link", { name: "Open message from pi-7" })).toHaveAttribute("href", `/shopfloor/process-messages/${MSG}`);
    expect(screen.getByRole("link", { name: "Record message" })).toHaveAttribute("href", "/shopfloor/process-messages/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by status, category and destination through the query", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/process-messages?status=error");
    expect(await screen.findByText("No messages match these filters")).toBeInTheDocument();
    expect(calls[0].url.search).toBe("?status=error");
    fill("Destination", "quality");
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=error&destination=quality"));
    fill("Status", "");
    fill("Destination", "");
    expect(await screen.findByText("No process messages yet")).toBeInTheDocument();
    expect(calls.at(-1)?.url.search).toBe("");
  });

  it("loads further pages with the cursor and the same filters, until the last page", async () => {
    const second = message({ id: "0195f0a0-0000-7000-8000-000000000042", sourceMessageId: "pi-6", receivedAt: "2026-10-21T07:00:00Z" });
    handler = (url) => {
      if (!url.pathname.endsWith("/process-messages")) return undefined;
      return url.searchParams.get("cursor") === "c-1" ? json(200, { items: [second], nextCursor: null }) : json(200, { items: [message()], nextCursor: "c-1" });
    };
    renderAt("/shopfloor/process-messages?status=partially-delivered");
    const table = await screen.findByRole("table", { name: "Process messages" });
    await waitFor(() => expect(within(table).getAllByRole("link")).toHaveLength(1));
    expect(screen.getByText("Showing the newest 1 message.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more messages" }));
    await waitFor(() => expect(within(table).getAllByRole("link")).toHaveLength(2));
    expect(within(table).getByRole("link", { name: "Open message from pi-6" })).toBeInTheDocument();
    expect(calls.at(-1)?.url.search).toBe("?status=partially-delivered&cursor=c-1");
    expect(screen.queryByRole("button", { name: "Load more messages" })).toBeNull();
  });

  it("keeps the loaded rows when a further page fails, and starts over when a filter changes", async () => {
    handler = (url) => {
      if (!url.pathname.endsWith("/process-messages")) return undefined;
      if (url.searchParams.get("cursor")) return json(503, { code: "COM-0503", detail: "Service unavailable" });
      return json(200, { items: [message()], nextCursor: "c-1" });
    };
    renderAt("/shopfloor/process-messages");
    fireEvent.click(await screen.findByRole("button", { name: "Load more messages" }));
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Process messages" })).getAllByRole("link")).toHaveLength(1);
    fill("Status", "error");
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=error"));
    await waitFor(() => expect(screen.queryByText(/Service unavailable/)).toBeNull());
    expect(await screen.findByRole("button", { name: "Load more messages" })).toBeInTheDocument();
  });

  it("shows the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/process-messages");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No process messages yet")).toBeInTheDocument();
  });
});

describe("Process message detail", () => {
  it("shows values with limits and deliveries, and reprocesses a destination in error with a reason and an Idempotency-Key", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/process-messages/${MSG}:reprocess`)) {
        const m = message();
        return json(200, {
          ...m,
          deliveries: m.deliveries.map((d) => (d.destination === "quality" ? { ...d, status: "pending", attempts: 0 } : d)),
        });
      }
      if (url.pathname.endsWith(`/process-messages/${MSG}`)) return json(200, message());
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/process-messages/${MSG}`);
    const values = await screen.findByRole("table", { name: "Values" });
    expect(values).toHaveTextContent("7.9 pH");
    expect(values).toHaveTextContent("6.5 to 7.5");
    const deliveries = screen.getByRole("table", { name: "Deliveries" });
    expect(deliveries).toHaveTextContent("QMS unavailable");
    expect(within(deliveries).getAllByRole("button")).toHaveLength(1);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(deliveries).getByRole("button", { name: "Reprocess Quality results" }));
    fireEvent.click(screen.getByRole("button", { name: "Send again" }));
    expect(await screen.findByText("Say why the destination should be sent again, up to 500 characters.")).toBeInTheDocument();
    expect(posts(":reprocess")).toHaveLength(0);

    fill("Reason", " QMS back online ");
    fireEvent.click(screen.getByRole("button", { name: "Send again" }));
    expect(await screen.findByText(/Quality results is pending again/)).toBeInTheDocument();
    const [sent] = posts(":reprocess");
    expect(sent.body).toEqual({ destination: "quality", reason: "QMS back online" });
    expect(header(sent, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(within(screen.getByRole("table", { name: "Deliveries" })).queryAllByRole("button")).toHaveLength(0);
  });

  it("shows a refused reprocess next to the form", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "SF-0116", detail: "only a destination in error can be reprocessed" }) : json(200, message());
    renderAt(`/shopfloor/process-messages/${MSG}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reprocess Quality results" }));
    fill("Reason", "retry");
    fireEvent.click(screen.getByRole("button", { name: "Send again" }));
    expect(await screen.findByText(/only a destination in error can be reprocessed \(SF-0116\)/)).toBeInTheDocument();
  });

  it("deletes a message with a destination in error, with a reason and an Idempotency-Key", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/process-messages/${MSG}:delete`)) {
        return json(200, message({ deletedAt: "2026-10-21T09:00:00Z", deletedBy: "supervisor-1", deleteReason: "sample lost" }));
      }
      if (url.pathname.endsWith(`/process-messages/${MSG}`)) return json(200, message());
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/process-messages/${MSG}`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete message" }));
    expect(screen.getByRole("heading", { name: "Delete message" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Delete message" }));
    expect(await screen.findByText("Say why the message is deleted, up to 500 characters.")).toBeInTheDocument();
    expect(posts(":delete")).toHaveLength(0);

    fill("Reason for deleting", "  sample lost ");
    fireEvent.click(screen.getByRole("button", { name: "Delete message" }));
    expect(await screen.findByText(/its remaining retries have stopped/)).toBeInTheDocument();
    const [sent] = posts(":delete");
    expect(sent.body).toEqual({ reason: "sample lost" });
    expect(header(sent, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(screen.getByText(/by supervisor-1: sample lost/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete message" })).toBeNull();
    expect(within(screen.getByRole("table", { name: "Deliveries" })).queryAllByRole("button")).toHaveLength(0);
  });

  it("shows a refused delete next to the form and keeps the message", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "SF-0119", detail: "only a message with a destination in error can be deleted" }) : json(200, message());
    renderAt(`/shopfloor/process-messages/${MSG}`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete message" }));
    fill("Reason for deleting", "duplicate");
    fireEvent.click(screen.getByRole("button", { name: "Delete message" }));
    expect(await screen.findByText(/only a message with a destination in error can be deleted \(SF-0119\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Delete message" })).toBeInTheDocument();
  });

  it("offers no delete while every destination can still be retried", async () => {
    const m = message();
    handler = () => json(200, { ...m, deliveries: m.deliveries.map((d) => (d.status === "error" ? { ...d, status: "pending", attempts: 2 } : d)) });
    renderAt(`/shopfloor/process-messages/${MSG}`);
    await screen.findByRole("table", { name: "Deliveries" });
    expect(screen.queryByRole("button", { name: "Delete message" })).toBeNull();
  });

  it("shows who deleted a message and why, and offers no reprocess or delete", async () => {
    handler = () => json(200, message({ deletedAt: "2026-10-21T09:00:00Z", deletedBy: "supervisor-1", deleteReason: "duplicate of pi-6" }));
    const { container } = renderAt(`/shopfloor/process-messages/${MSG}`);
    expect(await screen.findByText(/by supervisor-1: duplicate of pi-6/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete message" })).toBeNull();
    expect(within(screen.getByRole("table", { name: "Deliveries" })).queryAllByRole("button")).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a missing message as an error", async () => {
    handler = () => json(404, { code: "SF-0117", detail: "process message not found" });
    renderAt(`/shopfloor/process-messages/${MSG}`);
    expect(await screen.findByText(/process message not found \(SF-0117\)/)).toBeInTheDocument();
  });
});

describe("Record process message", () => {
  const base: Values = {
    categoryCode: "quality-result",
    orderId: ` ${ORDER} `,
    phasePath: "/mix/unit1",
    occurredAt: "2026-10-21T08:00",
    values: [{ name: " ph ", type: "numeric", value: "7.9", min: "6.5", max: "7.5", unit: "pH" }],
  };

  it("validates the way the service does and builds a manual message", () => {
    const { input } = validateProcessMessage(base, "src-1");
    expect(input).toMatchObject({
      source: "manual",
      sourceMessageId: "src-1",
      categoryCode: "quality-result",
      orderId: ORDER,
      phasePath: "/mix/unit1",
      values: [{ name: "ph", type: "numeric", value: "7.9", min: "6.5", max: "7.5", unit: "pH" }],
    });
    expect(input?.occurredAt).toBe(new Date("2026-10-21T08:00").toISOString());
    const bad = validateProcessMessage(
      {
        ...base,
        orderId: "PO-7",
        occurredAt: "",
        values: [
          { name: "a", type: "numeric", value: "hot", min: "x", max: "", unit: "" },
          { name: "a", type: "boolean", value: "maybe", min: "", max: "", unit: "" },
        ],
      },
      "src-2",
    );
    expect(Object.keys(bad.errors).sort()).toEqual(["occurredAt", "orderId", "values[0].min", "values[0].value", "values[1].name", "values[1].value"]);
    expect(bad.input).toBeUndefined();
  });

  it("records a message without an Idempotency-Key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/process-messages")) return json(201, { message: message({ source: "manual" }), duplicate: false });
      if (url.pathname.endsWith(`/process-messages/${MSG}`)) return json(200, message({ source: "manual" }));
      return undefined;
    };
    const { container } = renderAt("/shopfloor/process-messages/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Record message" }));
    expect(await screen.findByText("Enter the production or batch order id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Category", "quality-result");
    fill("Order id", ORDER);
    fill("Value 1 name", "ph");
    fill("Value 1", "7.9");
    fill("Value 1 upper limit", "7.5");
    fireEvent.click(screen.getByRole("button", { name: "Record message" }));
    expect(await screen.findByRole("table", { name: "Deliveries" })).toBeInTheDocument();
    const [create] = posts("/process-messages");
    expect(create.body).toMatchObject({ source: "manual", categoryCode: "quality-result", orderId: ORDER, values: [{ name: "ph", type: "numeric", value: "7.9", max: "7.5" }] });
    expect((create.body as { sourceMessageId: string }).sourceMessageId).toMatch(/^[0-9a-f-]{36}$/);
    expect(header(create, "Idempotency-Key")).toBeNull();
  });

  it("adds and removes values, and puts server field errors on their fields", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "values[1].name": "must be unique within the message" } });
    renderAt("/shopfloor/process-messages/new");
    fireEvent.click(screen.getByRole("button", { name: "Add value" }));
    fill("Order id", ORDER);
    fill("Value 1 name", "note");
    fill("Value 1 type", "text");
    fill("Value 1", "foam on surface");
    fill("Value 2 name", "done");
    fill("Value 2 type", "boolean");
    fill("Value 2", "true");
    fireEvent.click(screen.getByRole("button", { name: "Record message" }));
    expect(await screen.findByText("must be unique within the message")).toBeInTheDocument();
    expect(posts("/process-messages")[0].body).toMatchObject({
      values: [
        { name: "note", type: "text", value: "foam on surface" },
        { name: "done", type: "boolean", value: "true" },
      ],
    });
    fireEvent.click(screen.getByRole("button", { name: "Remove value 2" }));
    expect(screen.queryByLabelText("Value 2 name")).toBeNull();
  });
});
