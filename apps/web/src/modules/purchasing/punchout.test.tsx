import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cartTotal, nextStep, type PunchoutSession } from "./api/punchout";
import { validatePunchout } from "./PunchoutPage";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

afterEach(() => vi.unstubAllGlobals());

const SUPPLIER = "33333333-3333-4333-8333-333333333333";
const REQ = "44444444-4444-4444-8444-444444444444";
const supplier = { partnerId: SUPPLIER, name: "Keystone Seals & Gaskets", legalEntityId: "11111111-1111-4111-8111-111111111111", currencies: ["USD"] };
const line = (n: number, needsApproval = false) => ({
  line: n, supplierPartId: `KS-${n}`, description: `Seal kit ${n}`, quantity: "2", unitCode: "EA", unitPrice: "10.00", currency: "USD", unspsc: "", leadTimeDays: 3, catalog: true, needsApproval,
});
const session = (id: string, patch: Partial<PunchoutSession>): PunchoutSession => ({
  id, partnerId: SUPPLIER, legalEntityId: supplier.legalEntityId, requesterId: "u1", format: "CXML", status: "OPEN", expiresAt: "2027-03-01T10:00:00Z", createdAt: "2027-03-01T09:00:00Z", ...patch,
});
const returned = session("55555555-5555-4555-8555-555555555555", {
  status: "RETURNED", cart: { lines: [line(1), line(2, true)], issues: [{ code: "EDI-0133", detail: "Line 3 has no unit mapping.", reject: false }], cartRejected: false, computedTotal: "40.00" },
});

describe("punchout catalogs", () => {
  it("opens a supplier catalog and turns a returned cart into a requisition", async () => {
    let sessions: PunchoutSession[] = [returned];
    const fetchMock = stubFetch((url, init) => {
      if (url === "/api/v1/edi/punchout/suppliers") return json({ items: [supplier] });
      if (url === "/api/v1/edi/punchout/sessions" && (init.method ?? "GET") === "GET") return json({ items: sessions });
      if (url === "/api/v1/edi/punchout/sessions") {
        const opened = session("66666666-6666-4666-8666-666666666666", { startUrl: "https://catalog.example/start" });
        sessions = [opened, ...sessions];
        return json(opened, 201);
      }
      if (url.endsWith(":create-requisition")) {
        sessions = sessions.map((s) => (s.id === returned.id ? { ...s, requisitionId: REQ } : s));
        return json({ sessionId: returned.id, requisitionId: REQ }, 201);
      }
      return problem(404, "COM-0404", "not found");
    });
    const { container } = renderAt("/purchasing/punchout");
    const table = await findTable({ name: "Your punchout sessions" });
    await waitFor(() => expect(within(table).getByText("Keystone Seals & Gaskets")).toBeInTheDocument());
    expect(within(table).getByText("40.00 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "View cart" }));
    const cart = await findTable({ name: "Cart lines" });
    expect(within(cart).getByText("Above catalog price")).toBeInTheDocument();
    expect(screen.getByText("Some cart lines were left out")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open catalog" }));
    expect(await screen.findByText("Choose the supplier whose catalog you want to shop.")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: /Supplier/ }), { target: { value: SUPPLIER } });
    fireEvent.click(screen.getByRole("button", { name: "Open catalog" }));
    expect(await screen.findByRole("link", { name: "Shop the catalog in a new tab" })).toHaveAttribute("href", "https://catalog.example/start");
    expect(JSON.parse(String(fetchMock.mock.calls.find((c) => c[1]?.method === "POST")?.[1]?.body))).toEqual({ partnerId: SUPPLIER, format: "CXML" });

    fireEvent.click(within(table).getByRole("button", { name: "Create requisition" }));
    await waitFor(() => expect(within(table).getByRole("link", { name: "Open requisition" })).toHaveAttribute("href", `/purchasing/requisitions/${REQ}`));
    expect(container.textContent).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-/);
  });

  it("shows the service's refusal in plain words", async () => {
    stubFetch((url) => {
      if (url === "/api/v1/edi/punchout/suppliers") return json({ items: [supplier] });
      if (url === "/api/v1/edi/punchout/sessions") return url && json({ items: [] });
      return json({});
    });
    renderAt("/purchasing/punchout");
    await screen.findByRole("heading", { name: "No punchout sessions yet" });
    stubFetch((url, init) => (init.method === "POST" ? problem(502, "EDI-0033", "the supplier could not be reached") : json({ items: [supplier] })));
    fireEvent.change(screen.getByRole("combobox", { name: /Supplier/ }), { target: { value: SUPPLIER } });
    fireEvent.click(screen.getByRole("button", { name: "Open catalog" }));
    expect(await screen.findByText(/The supplier's catalog couldn't be opened\./)).toBeInTheDocument();
  });

  it("works out the next step and the cart total", () => {
    expect(validatePunchout("")).toHaveProperty("partnerId");
    expect(nextStep(session("a", {}))).toBe("shop");
    expect(nextStep(returned)).toBe("create");
    expect(nextStep({ ...returned, requisitionId: REQ })).toBe("open");
    expect(nextStep(session("b", { status: "CANCELLED" }))).toBe("none");
    expect(cartTotal(returned.cart)).toBe("40.00 USD");
    expect(cartTotal(undefined)).toBe("—");
  });
});
