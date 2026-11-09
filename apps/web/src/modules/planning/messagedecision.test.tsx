import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "./api/client";
import {
  applyEffectOf,
  isApplicable,
  noteError,
  parentSupplyOf,
} from "./MessageDetail";
import planning from "./module";
import { messageDelay } from "./pages";

const ID = "00000000-0000-0000-0000-00000000f201";
const cancel: Message = {
  id: ID,
  status: "open",
  version: 1,
  action: "cancel",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  supplyRef: "PO-2",
  demandRef: "",
  suggestedDate: "2026-12-21",
  delayDays: 0,
  qty: "80",
};
const info: Message = {
  ...cancel,
  id: "00000000-0000-0000-0000-00000000f202",
  action: "sales-on-top",
  supplyRef: "",
  demandRef: "SO-1",
  code: "PLAN-0015",
  text: "Sales exceed the forecast",
};

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) =>
  json({ type: "t", title: "x", status, code, detail }, status);

type Handler = (url: string, init?: RequestInit) => Response;

function stubApi(...routes: [string, Handler][]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (fetchMock: ReturnType<typeof stubApi>) =>
  fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
const header = (init: RequestInit | undefined, name: string) =>
  (init?.headers as Record<string, string>)[name];

afterEach(() => vi.unstubAllGlobals());

describe("message decision rules", () => {
  it("applies only to messages about existing supply", () => {
    for (const action of [
      "postpone",
      "advance",
      "cancel",
      "decrease",
      "increase",
      "derived",
    ] as const)
      expect(isApplicable({ action })).toBe(true);
    for (const action of [
      "delay",
      "shortage",
      "red-zone",
      "sales-on-top",
      "no-vendor",
      "no-quota-source",
      "lot-split",
      "bom-cycle",
    ] as const)
      expect(isApplicable({ action })).toBe(false);
  });
  it("never shows the order count of a lot-split warning as a delay", () => {
    expect(messageDelay({ action: "lot-split", delayDays: 3 })).toBe(0);
    expect(messageDelay({ action: "delay", delayDays: 3 })).toBe(3);
  });
  it("limits the note to 500 characters", () => {
    expect(noteError("x".repeat(500))).toBeNull();
    expect(noteError("x".repeat(501))).toMatch(/500/);
  });
});

describe("Action messages list with decisions", () => {
  it("shows the status, links each message to its decision and lists other statuses on request", async () => {
    const fetchMock = stubApi(["/messages", () => json({ items: [cancel] })]);
    renderAt("/planning/messages?plan=MAIN");
    const link = await screen.findByRole("link", {
      name: /Review Cancel message/,
    });
    expect(link.getAttribute("href")).toBe(
      `/planning/messages/${ID}?plan=MAIN`,
    );
    expect(screen.getAllByText("Open").length).toBeGreaterThan(1); // the filter option and the row status
    fireEvent.change(screen.getByLabelText("Status"), {
      target: { value: "all" },
    });
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) =>
          String(url).includes("status=all"),
        ),
      ).toBe(true),
    );
  });
  it("says when no message of a status exists", async () => {
    stubApi(["/messages", () => json({ items: [] })]);
    renderAt("/planning/messages?plan=MAIN&status=dismissed");
    expect(
      await screen.findByText(
        "No message of this status in the current plan version.",
      ),
    ).toBeTruthy();
  });
});

describe("Derived messages (FR-22.7)", () => {
  const derived: Message = {
    ...cancel,
    id: "00000000-0000-0000-0000-00000000f203",
    action: "derived",
    parentAction: "decrease",
    supplyRef: "PO-9",
    demandRef: "MO-2/10/00000000-0000-0000-0000-0000000000c1",
    suggestedDate: "2026-12-02",
    qty: "120",
  };
  it("says what applying does by the parent's action", () => {
    expect(
      applyEffectOf({ action: "derived", parentAction: "cancel" }),
    ).toMatch(/reduce .* cancelled/);
    expect(
      applyEffectOf({ action: "derived", parentAction: "postpone" }),
    ).toMatch(/move .* to the suggested date/);
    expect(
      applyEffectOf({ action: "derived", parentAction: "advance" }),
    ).toMatch(/forward to the suggested date/);
    expect(applyEffectOf({ action: "cancel" })).toBe("cancel the receipt");
  });
  it("names the parent supply from the dependent demand, which may hold a slash", () => {
    expect(parentSupplyOf("MO-2/10/c1")).toBe("MO-2/10");
    expect(parentSupplyOf("SO-1")).toBe("");
    expect(parentSupplyOf(null)).toBe("");
  });
  it("shows the parent supply and its action and can be applied", async () => {
    stubApi([`/messages/${derived.id}`, () => json(derived)]);
    renderAt(`/planning/messages/${derived.id}`);
    expect(await screen.findByText("MO-2/10 (decrease)")).toBeTruthy();
    expect(
      screen.getByText(/reduce the component receipt by the quantity shown/),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Apply message" })).toBeTruthy();
  });
});

describe("Message detail page", () => {
  it("shows a loading state, then the message", async () => {
    stubApi([`/messages/${ID}`, () => json(cancel)]);
    renderAt(`/planning/messages/${ID}`);
    expect(screen.getAllByText("Loading…").length).toBeGreaterThan(0);
    expect(await screen.findByText("PO-2")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Apply message" })).toBeTruthy();
  });

  it("applies under If-Match with the note and an Idempotency-Key", async () => {
    const fetchMock = stubApi([
      `/messages/${ID}`,
      (_url, init) =>
        init?.method
          ? json({
              ...cancel,
              status: "applied",
              version: 2,
              decidedBy: "user-1",
              decidedAt: "2026-11-03T10:00:00Z",
              decisionNote: "vendor agreed",
            })
          : json(cancel),
    ]);
    renderAt(`/planning/messages/${ID}`);
    await screen.findByText("PO-2");
    fireEvent.change(screen.getByLabelText(/Note/), {
      target: { value: " vendor agreed " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply message" }));
    expect(await screen.findByText("This message was applied")).toBeTruthy();
    const [[url, init]] = posts(fetchMock);
    expect(String(url)).toContain(`/messages/${ID}:apply`);
    expect(header(init, "If-Match")).toBe('"1"');
    expect(header(init, "Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toEqual({ note: "vendor agreed" });
    expect(screen.queryByRole("button", { name: "Apply message" })).toBeNull();
  });

  it("offers only dismissal for an information message", async () => {
    const fetchMock = stubApi([
      `/messages/${info.id}`,
      (_url, init) =>
        init?.method
          ? json({ ...info, status: "dismissed", version: 2 })
          : json(info),
    ]);
    renderAt(`/planning/messages/${info.id}`);
    await screen.findByText("PLAN-0015 Sales exceed the forecast");
    expect(screen.queryByRole("button", { name: "Apply message" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss message" }));
    expect(await screen.findByText("This message was dismissed")).toBeTruthy();
    expect(String(posts(fetchMock)[0][0])).toContain(":dismiss");
    expect(JSON.parse(String(posts(fetchMock)[0][1]?.body))).toEqual({});
  });

  it("refuses a note over 500 characters without calling the service", async () => {
    const fetchMock = stubApi([`/messages/${ID}`, () => json(cancel)]);
    renderAt(`/planning/messages/${ID}`);
    await screen.findByText("PO-2");
    fireEvent.change(screen.getByLabelText(/Note/), {
      target: { value: "x".repeat(501) },
    });
    fireEvent.click(screen.getByRole("button", { name: "Dismiss message" }));
    expect(
      await screen.findByText("Keep the note to 500 characters."),
    ).toBeTruthy();
    expect(posts(fetchMock)).toHaveLength(0);
  });

  it("shows a decided message without the form", async () => {
    stubApi([
      `/messages/${ID}`,
      () =>
        json({
          ...cancel,
          status: "dismissed",
          version: 2,
          decidedBy: "user-1",
          decidedAt: "2026-11-03T10:00:00Z",
          decisionNote: "not needed",
        }),
    ]);
    renderAt(`/planning/messages/${ID}`);
    expect(await screen.findByText("This message was dismissed")).toBeTruthy();
    expect(screen.getByText(/By user-1 on 2026-11-03/)).toBeTruthy();
    expect(screen.queryByText(/sent to the owner/)).toBeNull();
    expect(screen.queryByText(/earlier plan version/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Apply message" })).toBeNull();
  });

  it("says an applied decision went to the order's owner and when a run carried it", async () => {
    stubApi([
      `/messages/${ID}`,
      () =>
        json({
          ...cancel,
          status: "applied",
          version: 1,
          decidedBy: "user-1",
          decidedAt: "2026-11-03T10:00:00Z",
          decisionNote: "vendor agreed",
          carriedFrom: "00000000-0000-0000-0000-00000000f1ff",
        }),
    ]);
    renderAt(`/planning/messages/${ID}`);
    expect(await screen.findByText("This message was applied")).toBeTruthy();
    expect(
      screen.getByText(
        /sent to the owner of PO-2; the order is changed there, not in planning/,
      ),
    ).toBeTruthy();
    expect(screen.getByText(/Decided on an earlier plan version/)).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Dismiss message" }),
    ).toBeNull();
  });

  it("offers a reload after a stale version and shows the new state", async () => {
    let reads = 0;
    stubApi([
      `/messages/${ID}`,
      (_url, init) => {
        if (init?.method)
          return problem(
            412,
            "conflict_version",
            "the message was changed by another user; current version is 2",
          );
        reads += 1;
        return json(
          reads === 1
            ? cancel
            : { ...cancel, status: "applied", version: 2, decidedBy: "other" },
        );
      },
    ]);
    renderAt(`/planning/messages/${ID}`);
    await screen.findByText("PO-2");
    fireEvent.click(screen.getByRole("button", { name: "Apply message" }));
    expect(
      await screen.findByText(
        "This message was changed or decided by someone else",
      ),
    ).toBeTruthy();
    expect(screen.getByText(/conflict_version/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reload message" }));
    expect(await screen.findByText("This message was applied")).toBeTruthy();
  });

  it("shows the service's refusal of an information message", async () => {
    stubApi([
      `/messages/${ID}`,
      (_url, init) =>
        init?.method
          ? problem(
              422,
              "PLAN-MESSAGE-NOT-ACTIONABLE",
              "a cancel message changes no source order",
            )
          : json(cancel),
    ]);
    renderAt(`/planning/messages/${ID}`);
    await screen.findByText("PO-2");
    fireEvent.click(screen.getByRole("button", { name: "Apply message" }));
    expect(await screen.findByText("The decision was not saved")).toBeTruthy();
    expect(screen.getByText(/PLAN-MESSAGE-NOT-ACTIONABLE/)).toBeTruthy();
  });

  it("explains a message that is not found and a load failure", async () => {
    stubApi([
      `/messages/${ID}`,
      () => problem(404, "PLAN-MESSAGE-NOT-FOUND", "message not found"),
    ]);
    renderAt(`/planning/messages/${ID}`);
    expect(await screen.findByText("Message not found")).toBeTruthy();
    vi.unstubAllGlobals();
    stubApi([
      `/messages/${info.id}`,
      () => problem(500, "PLAN-UNKNOWN", "boom"),
    ]);
    renderAt(`/planning/messages/${info.id}`);
    expect(await screen.findByText("Could not load the message")).toBeTruthy();
  });
});
