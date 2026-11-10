import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const BASE = "/api/v1/procurement/purchasing-policies";

afterEach(() => vi.unstubAllGlobals());

const saved = { legalEntityId: LE, changeManagementDefault: true, poFourEyes: false, allowInvoiceBeforeReceipt: false, categoryAccessMode: "SOFT", version: 2 };
const defaults = { ...saved, version: 0 };

describe("purchasing policy list", () => {
  it("shows a loading table, then the saved policies, and opens one", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const { router } = renderAt("/purchasing/policies");
    expect(await screen.findByRole("table", { name: "Purchasing policies" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [{ ...saved, poFourEyes: true }] }));
    const row = await screen.findByText("11111111");
    expect(screen.getByText("Version")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/policies/${LE}`));
  });

  it("explains the empty state", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/policies");
    expect(await screen.findByText("No policies saved yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem code and retries on a load failure", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/policies");
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
  });

  it("opens a legal entity by id and refuses a malformed one", async () => {
    stubFetch(() => json({ items: [] }));
    const { router } = renderAt("/purchasing/policies");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Open policy" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Open policy" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/policies/${LE}`));
  });
});

describe("purchasing policy detail", () => {
  it("creates the policy of an unsaved legal entity under If-Match 0", async () => {
    const mock = stubFetch((_url, init) => {
      if (init.method === "PUT") return json({ ...defaults, poFourEyes: true, version: 1 }, 200, { ETag: '"1"' });
      return json(defaults, 200, { ETag: '"0"' });
    });
    const { container } = renderAt(`/purchasing/policies/${LE}`);
    expect(await screen.findByText("Not saved yet (defaults)")).toBeInTheDocument();
    expect(screen.getByLabelText("Change management on new purchase orders")).toBeChecked();
    fireEvent.click(screen.getByLabelText("Four-eyes on purchase orders"));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    expect(await screen.findByRole("button", { name: "Update policy" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Policy saved as version 1.");
    const put = mock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT")!;
    expect(put[0]).toBe(`${BASE}/${LE}`);
    expect((put[1] as RequestInit).headers).toMatchObject({ "If-Match": '"0"' });
    expect(JSON.parse((put[1] as RequestInit).body as string)).toEqual({
      changeManagementDefault: true,
      poFourEyes: true,
      allowInvoiceBeforeReceipt: false,
      categoryAccessMode: "SOFT",
    });
  });

  it("saves the category access mode chosen in the select", async () => {
    const mock = stubFetch((_url, init) => {
      if (init.method === "PUT") return json({ ...saved, categoryAccessMode: "HARD", version: 3 }, 200, { ETag: '"3"' });
      return json(saved, 200, { ETag: '"2"' });
    });
    renderAt(`/purchasing/policies/${LE}`);
    const select = await screen.findByLabelText("Category access");
    expect(select).toHaveValue("SOFT");
    fireEvent.change(select, { target: { value: "HARD" } });
    expect(screen.getByText(/refused/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update policy" }));
    expect(await screen.findByText("Policy saved as version 3.")).toBeInTheDocument();
    const put = mock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT")!;
    expect(JSON.parse((put[1] as RequestInit).body as string).categoryAccessMode).toBe("HARD");
  });

  it("explains a stale version, keeps the form and reloads the saved policy on request", async () => {
    let reads = 0;
    const mock = stubFetch((_url, init) => {
      if (init.method === "PUT") return problem(412, "PRC-POL-003", "policy is at version 3, not 2");
      reads += 1;
      return reads === 1 ? json(saved, 200, { ETag: '"2"' }) : json({ ...saved, poFourEyes: true, version: 3 }, 200, { ETag: '"3"' });
    });
    renderAt(`/purchasing/policies/${LE}`);
    fireEvent.click(await screen.findByLabelText("Allow invoicing before receipt"));
    fireEvent.click(screen.getByRole("button", { name: "Update policy" }));
    expect(await screen.findByText(/Someone else saved this policy/)).toBeInTheDocument();
    expect(screen.getByLabelText("Allow invoicing before receipt")).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Reload policy" }));
    expect(await screen.findByText("3")).toBeInTheDocument();
    expect(screen.getByLabelText("Four-eyes on purchase orders")).toBeChecked();
    expect(screen.getByLabelText("Allow invoicing before receipt")).not.toBeChecked();
    expect(screen.queryByText(/PRC-POL-003/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Update policy" }));
    await waitFor(() => {
      const puts = mock.mock.calls.filter(([, i]) => (i as RequestInit).method === "PUT");
      expect((puts[puts.length - 1][1] as RequestInit).headers).toMatchObject({ "If-Match": '"3"' });
    });
  });

  it("shows a load failure", async () => {
    stubFetch(() => problem(403, "AUTH-0003", "legal entity is outside the caller's scope"));
    renderAt(`/purchasing/policies/${LE}`);
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /policy$/ })).toBeNull();
  });
});
