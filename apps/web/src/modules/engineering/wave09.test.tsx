import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocumentPage, DocumentsPage, EbomImportPage, OrganizationsPage, ReleaseLinesPage } from "./wave09";

afterEach(() => vi.unstubAllGlobals());

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
  it("signs with re-authentication and records the meaning", async () => {
    const doc = { id: "d1", typeCode: "DRW", number: "DRW-1", version: "01", status: "in_review", lastEditor: "rita" };
    const fetchMock = stub((url, init) => {
      if (String(url).endsWith(":sign")) return ok({ ...doc, status: "released" });
      if (String(url).endsWith("/signatures")) {
        return ok({
          items: init ? [{ id: "s1", recordId: "d1", signerId: "carol", printedName: "Carol Kane", meaning: "Approved for release", signedAt: "2026-07-01T12:00:00Z" }] : [],
        });
      }
      return ok(doc);
    });
    render(<DocumentPage id="d1" canSign />);
    expect(await screen.findByText("DRW-1")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Printed name/), { target: { value: "Carol Kane" } });
    fireEvent.change(screen.getByLabelText(/Re-authentication assertion/), { target: { value: "ok" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign and release" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith(":sign"))).toBe(true));
    const signCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith(":sign"));
    expect(new Headers(signCall?.[1]?.headers).get("X-Passage-Reauth-Assertion")).toBe("ok");
    expect(JSON.parse(String(signCall?.[1]?.body))).toEqual({ printedName: "Carol Kane", method: "password" });
  });

  it("hides the signature form without the sign duty", async () => {
    stub((url) => (String(url).endsWith("/signatures") ? ok({ items: [] }) : ok({ id: "d1", typeCode: "DRW", number: "DRW-1", status: "in_review" })));
    render(<DocumentPage id="d1" canWrite />);
    await screen.findByText("DRW-1");
    expect(screen.queryByRole("button", { name: "Sign and release" })).toBeNull();
  });
});

describe("ReleaseLinesPage", () => {
  it("accepts a sent line when the accept duty is held", async () => {
    const line = { id: "l1", productId: "BIKE", engineeringVersion: "A", legalEntityId: "USMF", status: "sent" };
    const fetchMock = stub((url) => (String(url).endsWith(":accept") ? ok({ ...line, status: "accepted", acceptedBy: "bob" }) : ok({ items: [line] })));
    render(<ReleaseLinesPage canAccept />);
    await findTable({ name: "Release lines" });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith(":accept"))).toBe(true));
    expect(await screen.findByText("Accepted")).toBeInTheDocument();
  });

  it("hides accept without the duty", async () => {
    stub(() => ok({ items: [{ id: "l1", productId: "BIKE", engineeringVersion: "A", legalEntityId: "USMF", status: "sent" }] }));
    render(<ReleaseLinesPage />);
    await findTable({ name: "Release lines" });
    expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
  });
});

describe("EbomImportPage", () => {
  it("posts the CAD payload with an idempotency key", async () => {
    const fetchMock = stub(() => ok({ id: "i1", source: "cad", partNumber: "BIKE", revision: "A", lines: [], exceptions: [{ partNumber: "UNKNOWN-0", code: "ENG-EBOM-PART-UNKNOWN" }] }));
    render(<EbomImportPage />);
    fireEvent.change(screen.getByLabelText(/Part number/), { target: { value: "BIKE" } });
    fireEvent.change(screen.getByLabelText(/Revision/), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await screen.findByText(/unknown part/);
    const [, init] = fetchMock.mock.calls[0];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toMatchObject({ source: "cad", partNumber: "BIKE", revision: "A" });
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
