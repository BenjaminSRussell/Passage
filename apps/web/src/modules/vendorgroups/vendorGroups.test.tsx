import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderRoutes, json, idempotencyKey, pageViolations, server, useFakeServer } from "../../shell/testing/fakeServer";
import vendorGroups from "./module";
import { validateVendorGroup } from "./VendorGroupsPage";

useFakeServer();

const GROUP = { id: "11111111-1111-4111-8111-111111111111", code: "DOM", name: "Domestic", version: 1 };
const renderPage = () => renderRoutes(vendorGroups.routes, "/vendor-groups");

describe("validateVendorGroup", () => {
  it("accepts a lower-case code (it is upper-cased) and refuses a bad one", () => {
    expect(validateVendorGroup({ code: "dom-1", name: "Domestic", id: "" })).toEqual({});
    expect(validateVendorGroup({ code: "DOM", name: "Domestic", id: GROUP.id })).toEqual({});
    expect(validateVendorGroup({ code: "TOOLONGCODE1", name: "x", id: "" }).code).toBeDefined();
    expect(validateVendorGroup({ code: "A B", name: "x", id: "" }).code).toBeDefined();
    expect(validateVendorGroup({ code: "A", name: " ", id: "" }).name).toBeDefined();
    expect(validateVendorGroup({ code: "A", name: "x", id: "not-a-uuid" }).id).toBeDefined();
  });
});

describe("VendorGroupsPage", () => {
  it("lists the vendor groups with their codes", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/party/vendor-groups" ? json(200, { vendorGroups: [GROUP] }) : undefined);
    const { container } = renderPage();
    expect(await screen.findByText("Domestic")).toBeInTheDocument();
    expect(screen.getByText("DOM")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty list", async () => {
    server.handler = () => json(200, { vendorGroups: [] });
    renderPage();
    expect(await screen.findByText("No vendor groups")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderPage();
    expect(await screen.findByText("Could not load the vendor groups")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("refuses a bad code without calling the service", async () => {
    server.handler = () => json(200, { vendorGroups: [] });
    renderPage();
    await screen.findByText("No vendor groups");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "bad code!" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Domestic" } });
    fireEvent.click(screen.getByRole("button", { name: "Add vendor group" }));
    expect(await screen.findByText(/Use 1 to 10 characters/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("creates a group with an upper-cased code and an Idempotency-Key, then reloads", async () => {
    let created = false;
    server.handler = (url, init) => {
      if (url.pathname !== "/api/v1/party/vendor-groups") return undefined;
      if (init.method === "POST") {
        created = true;
        return json(201, GROUP);
      }
      return json(200, { vendorGroups: created ? [GROUP] : [] });
    };
    renderPage();
    await screen.findByText("No vendor groups");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "dom" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Domestic " } });
    fireEvent.click(screen.getByRole("button", { name: "Add vendor group" }));
    expect(await screen.findByText("Domestic")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ code: "DOM", name: "Domestic" });
    expect(idempotencyKey(post!)).toBeTruthy();
  });

  it("registers a code for a group id vendors already carry", async () => {
    server.handler = (url, init) => {
      if (url.pathname !== "/api/v1/party/vendor-groups") return undefined;
      return init.method === "POST" ? json(201, GROUP) : json(200, { vendorGroups: [] });
    };
    renderPage();
    await screen.findByText("No vendor groups");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "DOM" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Domestic" } });
    fireEvent.change(screen.getByLabelText("Existing group ID"), { target: { value: ` ${GROUP.id} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Add vendor group" }));
    await waitFor(() => expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ id: GROUP.id, code: "DOM", name: "Domestic" }));
  });

  it("shows the service refusal of a taken code", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "PTY-VENDOR-GROUP-EXISTS", detail: "the vendor group code is already taken" });
      return url.pathname.endsWith("/vendor-groups") ? json(200, { vendorGroups: [GROUP] }) : undefined;
    };
    renderPage();
    await screen.findByText("Domestic");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "DOM" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Again" } });
    fireEvent.click(screen.getByRole("button", { name: "Add vendor group" }));
    await waitFor(() => expect(screen.getByText(/already taken/)).toBeInTheDocument());
  });
});
