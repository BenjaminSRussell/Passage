import { findTable } from "../../test-utils";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { Andon } from "./api/client";
import shopfloor from "./module";

const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";
const JOB = "0195f0a0-0000-7000-8000-000000000050";
const QUALITY = "0195f0a0-0000-7000-8000-000000000031";
const SCRAP = "0195f0a0-0000-7000-8000-000000000032";

type Reply = { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const json = (status: number, body: unknown): Reply => ({ status, body });

let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

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
  cleanup();
  vi.unstubAllGlobals();
});

function renderAndon(duties: readonly string[]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={["/shopfloor/andon"]}>
        <Routes>
          {shopfloor.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const base: Andon = {
  id: QUALITY,
  resourceId: RESOURCE,
  siteId: null,
  kind: "quality-fail",
  severity: "warning",
  downtimeId: null,
  jobId: JOB,
  detail: "job MO-1 OP-10: failed visual",
  startedAt: "2026-10-21T08:10:00Z",
};
const scrap: Andon = { ...base, id: SCRAP, kind: "scrap-rate", jobId: null, detail: "12 of 100 pieces scrapped in the 15-minute bucket" };

const SUPERVISOR = ["shopfloor.view", "shopfloor.check.override"];

describe("Andon board conditions", () => {
  it("names each condition, links the job and offers a clear only to a holder of the check override duty", async () => {
    handler = (url) => (url.pathname.endsWith("/andon") ? json(200, { items: [base, scrap] }) : undefined);
    const { container, unmount } = renderAndon(["shopfloor.view"]);
    const table = await findTable({ name: "Open andon alerts" });
    await waitFor(() => expect(table).toHaveTextContent("Quality check failed"));
    expect(table).toHaveTextContent("Scrap over 5 % in a bucket");
    expect(table).toHaveTextContent("job MO-1 OP-10: failed visual");
    expect(within(table).getByRole("link", { name: "Open job" })).toHaveAttribute("href", `/shopfloor/jobs/${JOB}`);
    expect(within(table).queryByRole("button", { name: "Clear" })).toBeNull();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    unmount();
  });
});

describe("Andon sweep", () => {
  it("is the scheduler's job: the board offers no sweep, even to a supervisor", async () => {
    handler = (url) => (url.pathname.endsWith("/andon") ? json(200, { items: [scrap] }) : undefined);
    renderAndon(SUPERVISOR);
    const table = await findTable({ name: "Open andon alerts" });
    await waitFor(() => expect(table).toHaveTextContent("Scrap over 5 % in a bucket"));
    expect(screen.queryByRole("button", { name: /sweep/i })).toBeNull();
    expect(within(table).queryByRole("button", { name: "Clear" })).toBeNull();
    expect(calls.every((c) => c.method === "GET")).toBe(true);
  });
});

describe("Clear a quality-fail alert", () => {
  it("needs a reason, sends one key and reloads without the alert", async () => {
    let cleared = false;
    handler = (url, init) => {
      if (url.pathname.endsWith(`/andon/${QUALITY}:clear`) && init.method === "POST") {
        cleared = true;
        return json(200, base);
      }
      if (url.pathname.endsWith("/andon")) return json(200, { items: cleared ? [] : [base] });
      return undefined;
    };
    renderAndon(SUPERVISOR);
    const table = await findTable({ name: "Open andon alerts" });
    await waitFor(() => expect(table).toHaveTextContent("Quality check failed"));
    fireEvent.click(within(table).getByRole("button", { name: "Clear" }));
    fireEvent.click(await screen.findByRole("button", { name: "Clear alert" }));
    expect(await screen.findByText("Say why the alert is cleared, up to 500 characters.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: " reworked and re-checked " } });
    fireEvent.click(screen.getByRole("button", { name: "Clear alert" }));
    expect(await screen.findByText("No open alerts")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ reason: "reworked and re-checked" });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(screen.getByText(/quality-fail alert on resource .* is cleared/)).toBeInTheDocument();
  });

  it("shows a refusal and keeps the form", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(`/andon/${QUALITY}:clear`) && init.method === "POST") return json(404, { code: "SF-0162", detail: "no open andon alert with that id" });
      if (url.pathname.endsWith("/andon")) return json(200, { items: [base] });
      return undefined;
    };
    renderAndon(SUPERVISOR);
    const table = await findTable({ name: "Open andon alerts" });
    await waitFor(() => expect(table).toHaveTextContent("Quality check failed"));
    fireEvent.click(within(table).getByRole("button", { name: "Clear" }));
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "ok" } });
    fireEvent.click(screen.getByRole("button", { name: "Clear alert" }));
    expect(await screen.findByText(/no open andon alert with that id/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear alert" })).toBeEnabled();
  });
});
