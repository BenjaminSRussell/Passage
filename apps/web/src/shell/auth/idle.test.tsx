import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Auth } from "./auth";
import { AccountProvider } from "./account";
import { configuredIdleMinutes, IdleSignOut } from "./idle";
import type { Session } from "./oidc";

const session: Session = { accessToken: "t", refreshToken: undefined, idToken: undefined, expiresAt: 0, subject: "u-1", tenantId: "t-1", duties: [] };
const auth = (): Auth => ({ ensureSession: vi.fn(), accessToken: vi.fn(), login: vi.fn(), logout: vi.fn() });

describe("idle sign-out (#606)", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    window.localStorage.clear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("is off by default in a normal build", () => {
    expect(configuredIdleMinutes()).toBe(0);
    const a = auth();
    render(<AccountProvider auth={a} session={session}><p>page</p></AccountProvider>);
    act(() => void vi.advanceTimersByTime(60 * 60_000));
    expect(a.logout).not.toHaveBeenCalled();
  });

  it("warns one minute before and signs out when nothing happens", () => {
    const a = auth();
    render(<AccountProvider auth={a} session={session} idleMinutes={15}><p>page</p></AccountProvider>);
    act(() => void vi.advanceTimersByTime(13 * 60_000));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => void vi.advanceTimersByTime(90_000));
    expect(screen.getByRole("alert")).toHaveTextContent(/signed out in \d+ seconds/);
    expect(a.logout).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(60_000));
    expect(a.logout).toHaveBeenCalledOnce();
  });

  it("activity keeps the session, and the warning's button does too", async () => {
    const a = auth();
    render(<AccountProvider auth={a} session={session} idleMinutes={15}><p>page</p></AccountProvider>);
    act(() => void vi.advanceTimersByTime(14 * 60_000));
    act(() => void window.dispatchEvent(new Event("keydown")));
    act(() => void vi.advanceTimersByTime(13 * 60_000));
    expect(a.logout).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(90_000));
    const user = userEvent.setup({ advanceTimers: (ms) => void vi.advanceTimersByTime(ms) });
    await user.click(screen.getByRole("button", { name: "Stay signed in" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => void vi.advanceTimersByTime(14 * 60_000));
    expect(a.logout).not.toHaveBeenCalled();
  });

  it("another tab's activity counts", () => {
    const onTimeout = vi.fn();
    render(<IdleSignOut minutes={2} onTimeout={onTimeout} />);
    act(() => void vi.advanceTimersByTime(100_000));
    act(() => void window.dispatchEvent(new StorageEvent("storage", { key: "passage.lastActivity", newValue: String(Date.now()) })));
    act(() => void vi.advanceTimersByTime(100_000));
    expect(onTimeout).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(30_000));
    expect(onTimeout).toHaveBeenCalledOnce();
  });
});
