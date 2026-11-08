import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CleanoutCounterDetail } from "./api/client";
import scheduling from "./module";

const FILL = "3f1c0000-0000-4000-8000-000000000001";
const CAMPAIGN = "c1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: (url: URL, init?: RequestInit) => Reply | undefined;
let calls: { url: URL; method: string; body: string; key: string | null }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = new URL(input, "http://localhost");
      const headers = new Headers(init?.headers);
      calls.push({ url, method: init?.method ?? "GET", body: String(init?.body ?? ""), key: headers.get("Idempotency-Key") });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const counter: CleanoutCounterDetail = {
  resourceId: FILL,
  resourceCode: "FILL-1",
  resourceName: "Filling line 1",
  batchesSince: 3,
  minutesSince: 180,
  lastCampaignId: CAMPAIGN,
  updatedAt: "2027-01-05T09:00:00Z",
  lastResetAt: null,
  resets: [],
};

describe("Cleanout counters list", () => {
  it("lists the counters and links to the detail", async () => {
    handler = (url) => (url.pathname.endsWith("/cleanout-counters") ? { status: 200, body: { items: [counter] } } : undefined);
    const { container } = renderAt("/scheduling/cleanout-counters");
    const table = await findTable({ name: "Cleanout counters" });
    await waitFor(() => expect(table).toHaveTextContent("FILL-1"));
    expect(table).toHaveTextContent("Filling line 1");
    expect(table).toHaveTextContent("180");
    expect(screen.getByRole("link", { name: "Cleanout counter of FILL-1" })).toHaveAttribute("href", `/scheduling/cleanout-counters/${FILL}`);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows an empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/scheduling/cleanout-counters");
    expect(await screen.findByText("No resources")).toBeInTheDocument();
  });

  it("reports a failed load with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "SCHED-9999", detail: "database down" } });
    renderAt("/scheduling/cleanout-counters");
    expect(await findAlert()).toHaveTextContent("SCHED-9999");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Cleanout counter detail", () => {
  it("resets with the counts it saw and the reason, then lists the campaigns to sequence again", async () => {
    let after: CleanoutCounterDetail = counter;
    handler = (url, init) => {
      if (init?.method === "POST" && url.pathname.endsWith("/cleanout-counter:reset")) {
        after = {
          ...counter,
          batchesSince: 0,
          minutesSince: 0,
          lastCampaignId: null,
          lastResetAt: "2027-01-06T10:00:00Z",
          resets: [{ id: "r1", resourceId: FILL, previousBatches: 3, previousMinutes: 180, reason: "unplanned wash-down", resetBy: "u1", createdAt: "2027-01-06T10:00:00Z" }],
        };
        return { status: 201, body: { ...after.resets[0], counter: after, staleCampaignIds: [CAMPAIGN] } };
      }
      return url.pathname.endsWith(`/resources/${FILL}/cleanout-counter`) ? { status: 200, body: after } : undefined;
    };
    const { container } = renderAt(`/scheduling/cleanout-counters/${FILL}`);
    expect(await screen.findByText("Cleanout counter of FILL-1")).toBeInTheDocument();
    expect(await findTable({ name: "Resets" })).toHaveTextContent("No resets");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Reset counter" }));
    expect(await screen.findByText("Say why the resource was cleaned out.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: " unplanned wash-down " } });
    fireEvent.click(screen.getByRole("button", { name: "Reset counter" }));
    expect(await screen.findByText("1 planned campaign(s) must be sequenced again")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(JSON.parse(post?.body ?? "{}")).toEqual({ batchesSince: 3, minutesSince: 180, reason: "unplanned wash-down" });
    expect(post?.key).toBeTruthy();
    expect(screen.getByRole("link", { name: CAMPAIGN.slice(0, 8) })).toHaveAttribute("href", `/scheduling/campaigns/${CAMPAIGN}`);
    const table = await findTable({ name: "Resets" });
    await waitFor(() => expect(table).toHaveTextContent("unplanned wash-down"));
  });

  it("shows the refusal when the counter moved and reloads it", async () => {
    handler = (url, init) =>
      init?.method === "POST"
        ? { status: 409, body: { code: "SCHED-0148", detail: "the resource has run 4 batches and 240 minutes, not the 3 and 180 you saw" } }
        : url.pathname.endsWith("/cleanout-counter")
          ? { status: 200, body: counter }
          : undefined;
    renderAt(`/scheduling/cleanout-counters/${FILL}`);
    await screen.findByText("Cleanout counter of FILL-1");
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "wash" } });
    fireEvent.click(screen.getByRole("button", { name: "Reset counter" }));
    const alert = await screen.findByText(/SCHED-0148/);
    expect(alert).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBe(2));
  });

  it("says when the resource does not exist, and reports other failures with a retry", async () => {
    handler = () => ({ status: 404, body: { code: "SCHED-0102", detail: "resource not found" } });
    const first = renderAt(`/scheduling/cleanout-counters/${FILL}`);
    expect(await screen.findByText("Resource not found")).toBeInTheDocument();
    first.unmount();
    handler = () => ({ status: 500, body: { code: "SCHED-9999", detail: "database down" } });
    renderAt(`/scheduling/cleanout-counters/${FILL}`);
    expect(await findAlert()).toHaveTextContent("SCHED-9999");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
