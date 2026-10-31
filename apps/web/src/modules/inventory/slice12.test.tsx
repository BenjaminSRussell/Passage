import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findAlert, findTable } from "../../test-utils";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateJournalName } from "./NewJournalNamePage";
import { validateJournal } from "./NewJournalPage";

const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const NAME_ID = "7b7b7b7b-0000-4000-8000-000000000041";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; body: unknown; headers: Headers }[];
const json = (status: number, body: unknown) => ({ status, body });

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined, headers: new Headers(init.headers) });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const name = (o: Record<string, unknown> = {}) => ({
  id: NAME_ID,
  legalEntityId: ENTITY_ID,
  code: "ADJ-APPROVED",
  description: "Adjustments with approval",
  journalType: "ADJUSTMENT",
  requiresApproval: true,
  active: true,
  version: 2,
  createdAt: "2026-10-01T08:00:00Z",
  updatedAt: "2026-10-01T08:00:00Z",
  ...o,
});

describe("Journal names", () => {
  it("lists names with their approval rule", async () => {
    handler = () => json(200, { items: [name(), name({ id: "x2", code: "MOVE", journalType: "MOVEMENT", requiresApproval: false, offsetAccountId: "500100", description: "" })] });
    const { container } = renderAt("/inventory/journal-names");
    const table = await findTable({ name: "Journal names" });
    await waitFor(() => expect(table).toHaveTextContent("ADJ-APPROVED"));
    expect(table).toHaveTextContent("Required before posting");
    expect(table).toHaveTextContent("500100");
    expect(calls[0].url.searchParams.get("active")).toBe("true");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/journal-names");
    expect(await screen.findByText("No journal names")).toBeInTheDocument();
  });

  it("shows a failed load with a retry", async () => {
    handler = () => json(500, { code: "SYS-0001", detail: "database down" });
    renderAt("/inventory/journal-names");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load journal names");
    expect(alert).toHaveTextContent("SYS-0001");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("deactivates a name at its version and reloads", async () => {
    let active = true;
    handler = (_url, init) => {
      if (init.method === "PATCH") {
        active = false;
        return json(200, name({ active: false, version: 3 }));
      }
      return json(200, { items: [name({ active })] });
    };
    renderAt("/inventory/journal-names");
    await userEvent.click(await screen.findByRole("button", { name: "Deactivate ADJ-APPROVED" }));
    await waitFor(() => expect(screen.getByText("ADJ-APPROVED is inactive.")).toBeInTheDocument());
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.url.pathname).toBe(`/api/v1/inventory/journal-names/${NAME_ID}`);
    expect(patch?.headers.get("If-Match")).toBe('"2"');
    expect(patch?.body).toEqual({ active: false });
  });

  it("reports a version conflict from a row action", async () => {
    handler = (_url, init) => (init.method === "PATCH" ? json(412, { code: "conflict_version", detail: "journal name changed since it was read" }) : json(200, { items: [name()] }));
    renderAt("/inventory/journal-names");
    await userEvent.click(await screen.findByRole("button", { name: "Drop approval ADJ-APPROVED" }));
    expect(await findAlert()).toHaveTextContent("conflict_version");
  });
});

describe("New journal name", () => {
  it("validates before sending", () => {
    const ok = { legalEntityId: ENTITY_ID, code: "adj", description: "", journalType: "ADJUSTMENT", requiresApproval: "no", offsetAccountId: "" } as const;
    expect(validateJournalName(ok)).toEqual({});
    expect(validateJournalName({ ...ok, legalEntityId: "x", code: "bad code" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      code: "Use 1 to 20 letters, digits, _ or -.",
    });
    expect(validateJournalName({ ...ok, journalType: "MOVEMENT", offsetAccountId: "bad account" }).offsetAccountId).toBe("Enter a ledger account id.");
    // The offset field is hidden for other types, so a value left from MOVEMENT raises no error there.
    expect(validateJournalName({ ...ok, offsetAccountId: "bad account" })).toEqual({});
  });

  it("drops an offset account typed for a movement name when the type changes", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, name()) : json(200, { items: [] }));
    renderAt("/inventory/journal-names/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "adj" } });
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "MOVEMENT" } });
    fireEvent.change(screen.getByLabelText(/Default offset account/), { target: { value: "500100" } });
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "ADJUSTMENT" } });
    await userEvent.click(screen.getByRole("button", { name: "Create journal name" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ legalEntityId: ENTITY_ID, code: "ADJ", journalType: "ADJUSTMENT", requiresApproval: false });
  });

  it("creates a name with one idempotency key and returns to the list", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, name()) : json(200, { items: [] }));
    const { container } = renderAt("/inventory/journal-names/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "adj-approved" } });
    fireEvent.change(screen.getByLabelText("Approval before posting"), { target: { value: "yes" } });
    await userEvent.click(screen.getByRole("button", { name: "Create journal name" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ legalEntityId: ENTITY_ID, code: "ADJ-APPROVED", journalType: "ADJUSTMENT", requiresApproval: true });
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "Journal names" })).toBeInTheDocument();
  });

  it("shows a duplicate code on the form", async () => {
    handler = () => json(409, { code: "INV-JNLN-001", detail: "journal name ADJ already exists in this legal entity" });
    renderAt("/inventory/journal-names/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "adj" } });
    await userEvent.click(screen.getByRole("button", { name: "Create journal name" }));
    expect(await findAlert()).toHaveTextContent("INV-JNLN-001");
  });
});

describe("New journal under a name", () => {
  it("offers the names of the chosen type and legal entity and sends the choice", async () => {
    const created = { id: "j1", legalEntityId: ENTITY_ID, journalType: "ADJUSTMENT", status: "DRAFT", postingDate: "2026-10-01", currency: "USD", version: 1, lineCount: 0, createdAt: "2026-10-01T00:00:00Z" };
    handler = (url, init) => {
      if (init.method !== "POST" && url.pathname.endsWith("/journals/j1")) return json(200, { ...created, lines: [] });
      if (init.method === "POST") return json(201, { ...created, lines: [] });
      if (url.pathname.endsWith("/journal-names")) return json(200, { items: [name(), name({ id: "other", code: "OTHER", legalEntityId: "9e8d7c6b-0000-4000-8000-0000000000ff" })] });
      return json(200, {});
    };
    renderAt("/inventory/journals/new");
    fireEvent.change(screen.getByLabelText("Journal type"), { target: { value: "ADJUSTMENT" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "usd" } });
    const select = await screen.findByLabelText(/Journal name/);
    expect(screen.queryByRole("option", { name: "OTHER" })).toBeNull();
    fireEvent.change(select, { target: { value: NAME_ID } });
    expect(screen.getByText("This name requires the journal to be approved before it posts.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create journal" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ journalType: "ADJUSTMENT", journalNameId: NAME_ID });
  });

  it("does not need an offset account when the name defaults one", () => {
    const values = { journalType: "MOVEMENT", legalEntityId: ENTITY_ID, postingDate: "2026-10-01", currency: "USD", offsetAccountId: "", journalNameId: NAME_ID } as const;
    expect(validateJournal(values).offsetAccountId).toBeDefined();
    expect(validateJournal(values, name({ journalType: "MOVEMENT", offsetAccountId: "500100" }) as never)).toEqual({});
  });
});
