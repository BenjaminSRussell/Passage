import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Ledger } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const WRITE = ["ledger.ledger.write"];

function ledger(patch: Partial<Ledger> = {}): Ledger {
  return { id: LEDGER, code: "GL", name: "General ledger", chartCode: "STD", currency: "USD", dimensionAttributes: ["DEPT"], allowReinstate: true, version: 3, ...patch };
}

describe("ledger settings", () => {
  it("asks for a ledger first and checks the id", async () => {
    const { container } = renderAt("/ledger/ledger-settings");
    expect(screen.getByText("Choose a ledger to see its settings.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("turns reinstatement off with PATCH and If-Match, then sends the new version", async () => {
    let current = ledger();
    server.handler = (url, init) => {
      if (url.pathname !== `/api/v1/ledger/ledgers/${LEDGER}`) return undefined;
      if (init.method === "PATCH") {
        const body = JSON.parse(String(init.body)) as { allowReinstate: boolean };
        current = ledger({ allowReinstate: body.allowReinstate, version: (current.version ?? 0) + 1 });
      }
      return json(200, current);
    };
    const { container } = renderAt(`/ledger/ledger-settings?ledger=${LEDGER}`, WRITE);
    const box = await screen.findByLabelText("Allow reinstatement");
    expect(box).toBeChecked();
    const save = screen.getByRole("button", { name: "Save ledger settings" });
    expect(save).toBeDisabled();
    fireEvent.click(box);
    expect(save).toBeEnabled();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(save);
    expect(await screen.findByText("Ledger settings saved")).toBeInTheDocument();
    const patch = server.calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toEqual({ allowReinstate: false });
    expect(new Headers(patch?.init.headers).get("If-Match")).toBe('"3"');
    // The confirmation stays and the form reflects the saved value.
    expect(box).not.toBeChecked();
    expect(save).toBeDisabled();
    expect(screen.getByText("Reinstatement is now refused on GL.")).toBeInTheDocument();
    fireEvent.click(box);
    expect(screen.queryByText("Ledger settings saved")).not.toBeInTheDocument();
    fireEvent.click(save);
    expect(await screen.findByText("Reinstatement is now allowed on GL.")).toBeInTheDocument();
    const patches = server.calls.filter((c) => c.method === "PATCH");
    expect(patches).toHaveLength(2);
    expect(new Headers(patches[1]?.init.headers).get("If-Match")).toBe('"4"');
  });

  it("shows a version conflict from the service", async () => {
    server.handler = (url, init) => {
      if (url.pathname !== `/api/v1/ledger/ledgers/${LEDGER}`) return undefined;
      return init.method === "PATCH" ? json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the ledger changed since it was read" }) : json(200, ledger({ allowReinstate: false }));
    };
    renderAt(`/ledger/ledger-settings?ledger=${LEDGER}`, WRITE);
    fireEvent.click(await screen.findByLabelText("Allow reinstatement"));
    fireEvent.click(screen.getByRole("button", { name: "Save ledger settings" }));
    await waitFor(() => expect(screen.getByText(/LEDGER_VERSION_CONFLICT/)).toBeInTheDocument());
  });

  it("is read-only without the write duty and shows load errors with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, ledger()));
    renderAt(`/ledger/ledger-settings?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the ledger")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByLabelText("Allow reinstatement")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save ledger settings" })).not.toBeInTheDocument();
  });
});
