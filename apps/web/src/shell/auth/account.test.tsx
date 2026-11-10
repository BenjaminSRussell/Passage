import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SESSION_EXPIRED_EVENT } from "../api/http";
import type { Auth } from "./auth";
import { AccountProvider, UserMenu } from "./account";
import type { Session } from "./oidc";

const session: Session = { accessToken: "t", refreshToken: undefined, idToken: undefined, expiresAt: 0, subject: "u-1", tenantId: "t-1", duties: [], name: "Ada Lovelace" };

function setup() {
  const auth: Auth = { ensureSession: vi.fn(), accessToken: vi.fn(), login: vi.fn().mockResolvedValue(undefined), logout: vi.fn() };
  render(
    <AccountProvider auth={auth} session={session}>
      <UserMenu />
    </AccountProvider>,
  );
  return auth;
}

describe("account basics", () => {
  it("shows who is signed in and signs out from the menu", async () => {
    const auth = setup();
    await userEvent.click(screen.getByRole("button", { name: /Account: Ada Lovelace/ }));
    expect(screen.getByText("Ada Lovelace", { selector: "strong" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(auth.logout).toHaveBeenCalledOnce();
  });

  it("asks the user to sign in again when the API says the session ended", async () => {
    const auth = setup();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => {
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    });
    expect(screen.getByRole("alert")).toHaveTextContent("Your session has ended");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(auth.login).toHaveBeenCalledOnce();
  });

  it("renders nothing without a sign-in session", () => {
    render(<UserMenu />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
