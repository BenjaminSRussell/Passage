import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ExceptionsPanel, overrideAction } from "./ExceptionsPanel";

function setup(overrides: { verifyPin?: (s: string, p: string) => Promise<boolean>; clock?: { t: number } } = {}) {
  const clock = overrides.clock ?? { t: 1_000_000 };
  let n = 0;
  render(
    <ExceptionsPanel deviceId="DEV-1" workerId="W-1" verifyPin={overrides.verifyPin} now={() => clock.t} newId={() => `id-${++n}`} />,
  );
  return clock;
}

function fillWorkLine(line = "L-42", location = "A-01") {
  fireEvent.change(screen.getByLabelText("Location"), { target: { value: location } });
  fireEvent.change(screen.getByLabelText("Work line"), { target: { value: line } });
}

function fillSkip() {
  fireEvent.change(screen.getByLabelText("Exception"), { target: { value: "SKIP" } });
  fillWorkLine();
}

function fillOverride(supervisor: string, reason = "Customer hold") {
  fireEvent.change(screen.getByLabelText("Supervisor ID"), { target: { value: supervisor } });
  fireEvent.change(screen.getByLabelText("Supervisor PIN"), { target: { value: "4321" } });
  fireEvent.change(screen.getByLabelText("Override reason"), { target: { value: reason } });
}

function requestOverride() {
  fireEvent.click(screen.getByRole("button", { name: "Request override" }));
}

const exceptionLog = () => screen.getByRole("list", { name: "Exception log" });

describe("ExceptionsPanel worker exceptions (FR-32.8)", () => {
  it("starts with an empty log", () => {
    setup();
    expect(screen.getByText("No exceptions logged on this device.")).toBeInTheDocument();
  });

  it("logs a short pick with its work line, location and quantity", () => {
    setup();
    fillWorkLine();
    fireEvent.change(screen.getByLabelText("Units short"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Log exception" }));

    expect(within(exceptionLog()).getByText(/Short pick · L-42 · A-01 · 3 short/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("logged");
    expect(screen.getByLabelText("Units short")).toHaveValue("");
  });

  it.each(["1.5", "0", ""])("refuses a short pick of %j units", (qty) => {
    setup();
    fillWorkLine();
    fireEvent.change(screen.getByLabelText("Units short"), { target: { value: qty } });
    fireEvent.click(screen.getByRole("button", { name: "Log exception" }));
    expect(screen.getByRole("alert")).toHaveTextContent("whole quantity above zero");
    expect(screen.getByText("No exceptions logged on this device.")).toBeInTheDocument();
  });

  it("requires a reason for damage and logs it when given", () => {
    setup();
    fireEvent.change(screen.getByLabelText("Exception"), { target: { value: "DAMAGED" } });
    fillWorkLine();
    fireEvent.click(screen.getByRole("button", { name: "Log exception" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Damage needs a reason.");

    fireEvent.change(screen.getByLabelText("Damage reason"), { target: { value: "crushed corner" } });
    fireEvent.click(screen.getByRole("button", { name: "Log exception" }));
    expect(exceptionLog()).toHaveTextContent("Report damage · L-42 · A-01 · crushed corner");
  });

  it("requires a work line and location", () => {
    setup();
    fireEvent.change(screen.getByLabelText("Exception"), { target: { value: "SKIP" } });
    fireEvent.click(screen.getByRole("button", { name: "Log exception" }));
    expect(screen.getByRole("alert")).toHaveTextContent("User, device, location and work line are required.");
  });
});

describe("ExceptionsPanel supervisor override (BR-32.7)", () => {
  it("names one action per exception and work line", () => {
    expect(overrideAction({ kind: "SKIP", workLineId: "L-42" })).toBe("SKIP:L-42");
  });

  it("cannot issue an override when no PIN check is connected", () => {
    setup();
    fillSkip();
    fillOverride("SUP-7");
    expect(screen.getByText(/PIN check is not connected/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request override" })).toBeDisabled();
  });

  it("asks for the exception before the PIN is checked", async () => {
    const verifyPin = vi.fn().mockResolvedValue(true);
    setup({ verifyPin });
    fillOverride("SUP-7");
    requestOverride();
    expect(await findAlert()).toHaveTextContent("Fill in the exception to authorize first.");
    expect(verifyPin).not.toHaveBeenCalled();
  });

  it("needs a configured override reason", async () => {
    const verifyPin = vi.fn().mockResolvedValue(true);
    setup({ verifyPin });
    fillSkip();
    fillOverride("SUP-7", "");
    requestOverride();
    expect(await findAlert()).toHaveTextContent("Override needs a configured reason.");
    expect(verifyPin).not.toHaveBeenCalled();
  });

  it("rejects a PIN the server does not accept and clears the PIN field", async () => {
    const verifyPin = vi.fn().mockResolvedValue(false);
    setup({ verifyPin });
    fillSkip();
    fillOverride("SUP-7");
    requestOverride();
    expect(await screen.findByText("Supervisor PIN was not accepted.")).toBeInTheDocument();
    expect(verifyPin).toHaveBeenCalledWith("SUP-7", "4321");
    expect(screen.getByLabelText("Supervisor PIN")).toHaveValue("");
    expect(screen.queryByRole("button", { name: /Use override/ })).toBeNull();
  });

  it("treats a failed PIN check as not accepted", async () => {
    setup({ verifyPin: vi.fn().mockRejectedValue(new Error("offline")) });
    fillSkip();
    fillOverride("SUP-7");
    requestOverride();
    expect(await screen.findByText("Supervisor PIN was not accepted.")).toBeInTheDocument();
  });

  it("refuses an override a supervisor would issue for their own work, without sending the PIN", async () => {
    const verifyPin = vi.fn().mockResolvedValue(true);
    setup({ verifyPin });
    fillSkip();
    fillOverride("W-1");
    requestOverride();
    expect(await screen.findByText("A supervisor cannot override their own work.")).toBeInTheDocument();
    expect(verifyPin).not.toHaveBeenCalled();
  });

  it("logs the supervisor when granted, logs the authorized exception when used, and is used once", async () => {
    setup({ verifyPin: vi.fn().mockResolvedValue(true) });
    fillSkip();
    fillOverride("SUP-7");
    requestOverride();
    const use = await screen.findByRole("button", { name: "Use override: Skip · L-42 · A-01" });
    expect(exceptionLog()).toHaveTextContent("Supervisor override · L-42 · A-01 · Customer hold · by SUP-7");

    // Editing the form after the grant does not change what the override authorizes.
    fillWorkLine("L-99", "B-02");
    fireEvent.click(use);
    expect(screen.getByRole("status")).toHaveTextContent("Skip · L-42 · A-01 logged with the override by SUP-7.");
    const items = within(exceptionLog()).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[1]).toHaveTextContent(/^Skip · L-42 · A-01 ·/);
    expect(screen.queryByRole("button", { name: /Use override/ })).toBeNull();
  });

  it("expires after 60 seconds with MOB-0110 and logs nothing", async () => {
    const clock = setup({ verifyPin: vi.fn().mockResolvedValue(true) });
    fillSkip();
    fillOverride("SUP-7");
    requestOverride();
    const use = await screen.findByRole("button", { name: /Use override/ });

    clock.t += 60_001;
    fireEvent.click(use);
    expect(screen.getByRole("alert")).toHaveTextContent("MOB-0110 Supervisor PIN expired or already used.");
    expect(within(exceptionLog()).getAllByRole("listitem")).toHaveLength(1);
    expect(screen.queryByRole("button", { name: /Use override/ })).toBeNull();
  });
});
