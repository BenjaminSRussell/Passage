import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { XrefEntry } from "./api/client";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply | undefined;
let calls: URL[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderPage() {
  return render(
    <PrincipalProvider duties={["migration.run"]}>
      <MemoryRouter initialEntries={["/migration/xref"]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const entry = (key: string, extra: Partial<XrefEntry> = {}): XrefEntry => ({
  sourceSystem: "ax2012",
  entity: "customer",
  sourceKey: key,
  targetId: "0195f0a0-0000-7000-8000-0000000000d1",
  updatedAt: "2026-10-30T09:00:00Z",
  ...extra,
});
const summary = { items: [{ sourceSystem: "ax2012", entity: "customer", count: 3 }] };

describe("legacy key cross-reference", () => {
  it("shows counts and rows, filters by key prefix and pages with the cursor", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/xref/summary")) return json(200, summary);
      if (url.searchParams.get("cursor") === "c1") return json(200, { items: [entry("C3")] });
      return json(200, { items: [entry("C1"), entry("C2", { mergedIntoTargetId: "0195f0a0-0000-7000-8000-0000000000e2" })], nextCursor: "c1" });
    };
    const { container } = renderPage();
    const table = await findTable({ name: "Legacy key cross-reference" });
    expect(table).toHaveTextContent("C2");
    expect(table).toHaveTextContent("0195f0a0-0000-7000-8000-0000000000e2");
    expect(screen.getByLabelText("Mapped keys per source system and entity")).toHaveTextContent("ax2012 customer");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(table).toHaveTextContent("C3"));
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Legacy key starts with"), { target: { value: "C1" } });
    await waitFor(() => expect(calls.some((u) => u.searchParams.get("keyPrefix") === "C1" && !u.searchParams.has("cursor"))).toBe(true));
  });

  it("sends a Passage id only once it is a full UUID", async () => {
    handler = (url) => json(200, url.pathname.endsWith("/summary") ? summary : { items: [entry("C1")] });
    renderPage();
    await findTable({ name: "Legacy key cross-reference" });
    const field = screen.getByLabelText("Passage id");
    fireEvent.change(field, { target: { value: "0195f0a0-0000" } });
    expect(await screen.findByText("Enter a full Passage id (UUID).")).toBeInTheDocument();
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((u) => u.searchParams.has("targetId"))).toBe(false);
    expect(screen.getByRole("table")).toHaveTextContent("C1");

    fireEvent.change(field, { target: { value: " 0195f0a0-0000-7000-8000-0000000000d1 " } });
    await waitFor(() => expect(calls.some((u) => u.searchParams.get("targetId") === "0195f0a0-0000-7000-8000-0000000000d1")).toBe(true));
    expect(screen.queryByText("Enter a full Passage id (UUID).")).not.toBeInTheDocument();
  });

  it("drops a load-more page that returns after the filter changed", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        const url = new URL(input, "http://localhost");
        calls.push(url);
        let body: unknown = { items: [entry("C1")], nextCursor: "c1" };
        if (url.pathname.endsWith("/summary")) body = summary;
        else if (url.searchParams.has("cursor")) {
          await gate;
          body = { items: [entry("STALE")] };
        } else if (url.searchParams.get("keyPrefix") === "N") body = { items: [entry("N1")] };
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    renderPage();
    await findTable({ name: "Legacy key cross-reference" });
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    fireEvent.change(screen.getByLabelText("Legacy key starts with"), { target: { value: "N" } });
    const table = await findTable({ name: "Legacy key cross-reference" });
    await waitFor(() => expect(table).toHaveTextContent("N1"));
    release();
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(screen.getByRole("table")).toHaveTextContent("N1");
    expect(screen.getByRole("table")).not.toHaveTextContent("STALE");
  });

  it("shows the empty state", async () => {
    handler = (url) => json(200, url.pathname.endsWith("/summary") ? { items: [] } : { items: [] });
    renderPage();
    expect(await screen.findByText("No mapped keys")).toBeInTheDocument();
  });

  it("shows the error state and retries", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderPage();
    expect(await screen.findByText(/missing duty \(AUTH-0003\)/)).toBeInTheDocument();
    handler = (url) => json(200, url.pathname.endsWith("/summary") ? summary : { items: [entry("C1")] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Legacy key cross-reference" })).toHaveTextContent("C1");
  });

  it("reports a failed load-more without dropping the rows", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/summary")) return json(200, summary);
      if (url.searchParams.has("cursor")) return json(500, { code: "COM-0500", detail: "boom" });
      return json(200, { items: [entry("C1")], nextCursor: "c1" });
    };
    renderPage();
    await findTable({ name: "Legacy key cross-reference" });
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText(/boom \(COM-0500\)/)).toBeInTheDocument();
    expect(screen.getByRole("table")).toHaveTextContent("C1");
  });
});
