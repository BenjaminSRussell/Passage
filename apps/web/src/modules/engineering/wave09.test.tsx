import { findAlert, findTable } from "../../test-utils";
import { checkAxeAA } from "../../shell/a11y";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocumentCreatePage, DocumentPage, DocumentsPage, EbomImportPage, OrganizationsPage, ReleaseLinesPage } from "./wave09";

afterEach(() => vi.unstubAllGlobals());

const PAGE_LEVEL_RULES = ["page-has-heading-one", "landmark-unique", "region", "bypass"];

function componentViolations(root: ParentNode) {
  return checkAxeAA(root).filter((v) => !PAGE_LEVEL_RULES.includes(v.id));
}

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, title: string) => ({ ok: false, status, json: async () => ({ code, title }) });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("OrganizationsPage", () => {
  it("lists organizations and offers create", async () => {
    stub(() => ok({ items: [{ id: "o1", legalEntityId: "ENG1", code: "ENG", name: "Engineering" }] }));
    const created = vi.fn();
    render(<OrganizationsPage onCreate={created} />);
    expect(await findTable({ name: "Engineering organizations" })).toHaveTextContent("ENG");
    fireEvent.click(screen.getByRole("button", { name: "New organization" }));
    expect(created).toHaveBeenCalled();
  });
});

describe("DocumentsPage", () => {
  it("lists documents and opens a row", async () => {
    stub(() => ok({ items: [{ id: "d1", typeCode: "DRW", number: "DRW-1", version: "01", status: "in_review" }] }));
    const opened: string[] = [];
    render(<DocumentsPage onOpen={(id) => opened.push(id)} />);
    const table = await findTable({ name: "Engineering documents" });
    expect(table).toHaveTextContent("DRW-1");
    fireEvent.click(screen.getByRole("button", { name: "DRW-1" }));
    expect(opened).toEqual(["d1"]);
  });
});

describe("DocumentPage", () => {
  const doc = { id: "d1", typeCode: "DRW", number: "DRW-1", version: "01", status: "in_review", lastEditor: "rita" };
  const signatures = [{ id: "s1", recordId: "d1", signerId: "carol", printedName: "Carol Kane", meaning: "Approved for release", signedAt: "2026-07-01T12:00:00Z" }];

  it("signs without asking for an assertion and has no axe violations", async () => {
    let signed = false;
    const fetchMock = stub((url) => {
      if (String(url).endsWith(":sign")) {
        signed = true;
        return ok({ ...doc, status: "released" });
      }
      if (String(url).endsWith("/signatures")) return ok({ items: signed ? signatures : [] });
      return ok(doc);
    });
    const { container } = render(<DocumentPage id="d1" canSign />);
    expect(await screen.findByText("DRW-1")).toBeInTheDocument();
    expect(screen.queryByLabelText(/assertion/i)).toBeNull();
    expect(componentViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Printed name/), { target: { value: "Carol Kane" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign and release" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith(":sign"))).toBe(true));
    const signCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith(":sign"));
    expect(new Headers(signCall?.[1]?.headers).get("X-Passage-Reauth-Assertion")).toBeNull();
    expect(JSON.parse(String(signCall?.[1]?.body))).toEqual({ printedName: "Carol Kane", method: "password" });
    expect(await screen.findByText("Carol Kane")).toBeInTheDocument();
    expect(componentViolations(container)).toEqual([]);
  });

  it("says so when signing is unavailable", async () => {
    stub((url) => {
      if (String(url).endsWith(":sign")) return problem(503, "ENG-DOC-REAUTH-UNAVAILABLE", "signing is unavailable");
      return String(url).endsWith("/signatures") ? ok({ items: [] }) : ok(doc);
    });
    render(<DocumentPage id="d1" canSign />);
    await screen.findByText("DRW-1");
    fireEvent.change(screen.getByLabelText(/Printed name/), { target: { value: "Carol Kane" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign and release" }));
    expect(await findAlert()).toHaveTextContent("signing is unavailable");
  });

  it("hides the signature form without the sign duty or once signed", async () => {
    stub((url) => (String(url).endsWith("/signatures") ? ok({ items: [] }) : ok(doc)));
    const { unmount } = render(<DocumentPage id="d1" canWrite />);
    await screen.findByText("DRW-1");
    expect(screen.queryByRole("button", { name: "Sign and release" })).toBeNull();
    unmount();
    stub((url) => (String(url).endsWith("/signatures") ? ok({ items: signatures }) : ok({ ...doc, signedAt: "2026-07-01T12:00:00Z" })));
    render(<DocumentPage id="d1" canSign />);
    await screen.findByText("DRW-1");
    expect(screen.queryByRole("button", { name: "Sign and release" })).toBeNull();
  });
});

describe("ReleaseLinesPage", () => {
  const line = { id: "l1", productId: "BIKE", engineeringVersion: "A", legalEntityId: "USMF", status: "sent" };

  it("accepts a sent line when the accept duty is held", async () => {
    const fetchMock = stub((url) => (String(url).endsWith(":accept") ? ok({ ...line, status: "accepted", acceptedBy: "bob" }) : ok({ items: [line] })));
    const { container } = render(<ReleaseLinesPage canAccept />);
    await findTable({ name: "Release lines" });
    expect(componentViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith(":accept"))).toBe(true));
    expect(await screen.findByText("Accepted")).toBeInTheDocument();
  });

  it("hides accept without the duty", async () => {
    stub(() => ok({ items: [line] }));
    render(<ReleaseLinesPage />);
    await findTable({ name: "Release lines" });
    expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
  });

  it("offers no decision on a line that is not sent", async () => {
    stub(() => ok({ items: [{ ...line, id: "l2", status: "pending" }, { ...line, id: "l3", status: "accepted" }] }));
    render(<ReleaseLinesPage canAccept />);
    await findTable({ name: "Release lines" });
    expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
  });
});

describe("EbomImportPage", () => {
  const imported = {
    id: "i1", source: "cad", partNumber: "BIKE", revision: "A", added: 1, removed: 0, changed: 0, lines: [],
    exceptions: [{ partNumber: "UNKNOWN-0", code: "ENG-EBOM-PART-UNKNOWN" }, { partNumber: "UNKNOWN-1", code: "ENG-EBOM-PART-UNKNOWN" }],
  };

  it("posts the CAD payload with an idempotency key and shows the unknown parts", async () => {
    const fetchMock = stub(() => ok(imported));
    const created = vi.fn();
    const { container } = render(<EbomImportPage onCreated={created} />);
    fireEvent.change(screen.getByLabelText(/Part number/), { target: { value: "BIKE" } });
    fireEvent.change(screen.getByLabelText(/Revision/), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText(/2 unknown parts left out/)).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "Unknown parts" });
    expect(list).toHaveTextContent("UNKNOWN-0");
    expect(list).toHaveTextContent("UNKNOWN-1");
    expect(created).not.toHaveBeenCalled();
    expect(componentViolations(container)).toEqual([]);
    const [, init] = fetchMock.mock.calls[0];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toMatchObject({ source: "cad", partNumber: "BIKE", revision: "A" });
    fireEvent.click(screen.getByRole("button", { name: "View imports" }));
    expect(created).toHaveBeenCalledWith("i1");
  });

  it("shows the service problem on a refused import", async () => {
    stub(() => problem(409, "ENG-EBOM-REVISION-RELEASED", "revision already released"));
    render(<EbomImportPage />);
    fireEvent.change(screen.getByLabelText(/Part number/), { target: { value: "BIKE" } });
    fireEvent.change(screen.getByLabelText(/Revision/), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await findAlert()).toHaveTextContent("revision already released");
  });
});

describe("DocumentCreatePage", () => {
  it("has no axe violations and shows the storage key error from the service", async () => {
    stub(() => ({ ok: false, status: 400, json: async () => ({ code: "COM-0001", title: "invalid", errors: { "originals[0]": "storageKey must start with the tenant id" } }) }));
    const { container } = render(<DocumentCreatePage />);
    expect(componentViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Type code/), { target: { value: "DRW" } });
    fireEvent.change(screen.getByLabelText(/^Number/), { target: { value: "DRW-1" } });
    fireEvent.change(screen.getByLabelText(/Original storage key/), { target: { value: "k" } });
    fireEvent.change(screen.getByLabelText(/Original SHA-256/), { target: { value: "a".repeat(64) } });
    fireEvent.click(screen.getByRole("button", { name: "Create document" }));
    expect(await screen.findByText(/must start with the tenant id/)).toBeInTheDocument();
  });
});
