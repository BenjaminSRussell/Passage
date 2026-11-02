import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ADMIN = ["pricing.view", "pricing.admin"];

function load() {
  fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
  fireEvent.click(screen.getByRole("button", { name: "Load parameters" }));
}

describe("Pricing parameters", () => {
  it("starts empty and validates the legal entity id", async () => {
    const { container } = renderAt("/pricing/parameters");
    expect(screen.getByText("No legal entity loaded")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load parameters" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("loads the defaults and saves four eyes at the version read", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith("/parameters")) return undefined;
      if (init.method === "PUT") return json(200, { legalEntityId: LE, journalFourEyes: true, version: 1, updatedBy: "ana" });
      return json(200, { legalEntityId: LE, journalFourEyes: false, version: 0 });
    };
    const { container } = renderAt("/pricing/parameters", ADMIN);
    load();
    const box = await screen.findByLabelText("Four eyes on journals");
    expect(screen.getByText("Defaults, nothing saved yet.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(box).not.toBeChecked();
    fireEvent.click(box);
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ legalEntityId: LE, journalFourEyes: true, version: 0 });
    expect(idempotencyKey(put)).toBeTruthy();
    expect(screen.getByText("Version 1, saved by ana")).toBeInTheDocument();
  });

  it("reads without pricing.admin but offers no save", async () => {
    server.handler = () => json(200, { legalEntityId: LE, journalFourEyes: true, version: 3, updatedBy: "ana" });
    renderAt("/pricing/parameters", ["pricing.view"]);
    load();
    expect(await screen.findByLabelText("Four eyes on journals")).toBeChecked();
    expect(screen.getByLabelText("Four eyes on journals")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save parameters" })).toBeNull();
    expect(screen.getByText("Changing them needs the pricing.admin duty.")).toBeInTheDocument();
  });

  it("retries a failed save with the same key, and a changed value with a new one", async () => {
    let fail = true;
    server.handler = (_url, init) => {
      if (init.method === "PUT") return fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { legalEntityId: LE, journalFourEyes: false, version: 2 });
      return json(200, { legalEntityId: LE, journalFourEyes: true, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    fireEvent.click(await screen.findByRole("button", { name: "Save parameters" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    fail = false;
    fireEvent.click(screen.getByLabelText("Four eyes on journals"));
    fireEvent.click(screen.getByRole("button", { name: "Save parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const keys = server.calls.filter((c) => c.method === "PUT").map(idempotencyKey);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[1]);
  });

  it("shows the service error, such as a stale version", async () => {
    server.handler = (_url, init) => {
      if (init.method === "PUT") return json(412, { code: "conflict_version", detail: "parameters version does not match" });
      return json(200, { legalEntityId: LE, journalFourEyes: false, version: 1 });
    };
    renderAt("/pricing/parameters", ADMIN);
    load();
    fireEvent.click(await screen.findByRole("button", { name: "Save parameters" }));
    await waitFor(async () => expect(await findAlert()).toHaveTextContent("parameters version does not match (conflict_version)"));
  });

  it("shows a read failure", async () => {
    server.handler = () => json(503, { code: "COM-0500", detail: "internal error" });
    renderAt("/pricing/parameters");
    load();
    expect(await findAlert()).toHaveTextContent("internal error (COM-0500)");
  });
});
