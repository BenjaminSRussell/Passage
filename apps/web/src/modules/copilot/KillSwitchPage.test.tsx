import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pageViolations } from "./a11yTest";
import { getKillSwitch, KILL_SWITCH_PATH, setKillSwitch, type KillSwitchResult, type KillSwitchStatus } from "./api/agents";
import { KillSwitchPage } from "./KillSwitchPage";
import copilot from "./module";

const T1 = "00000000-0000-7000-8000-000000000001";
const ADMIN = "00000000-0000-7000-8000-0000000000a1";

const off: KillSwitchStatus = {
  tenantId: T1,
  halted: false,
  stale: false,
  tenant: { active: false, version: null, changedAt: null, byUser: null },
  global: { active: false, version: null, changedAt: null },
};
const halted: KillSwitchStatus = {
  ...off,
  halted: true,
  tenant: { active: true, version: 3, changedAt: "2026-10-22T10:00:00Z", byUser: ADMIN },
};
const result = (active: boolean, scope: "TENANT" | "GLOBAL" = "TENANT"): KillSwitchResult => ({
  scope,
  tenantId: scope === "TENANT" ? T1 : null,
  active,
  version: 4,
  changedAt: "2026-10-22T10:05:00Z",
  byUser: ADMIN,
});

const json = (body: unknown, status = 200, type = "application/json") => new Response(JSON.stringify(body), { status, headers: { "content-type": type } });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("kill switch client", () => {
  it("reads the status and validates it", async () => {
    const fetchFn = vi.fn(async (_url: string, _init?: RequestInit) => json(halted));
    expect(await getKillSwitch(undefined, fetchFn)).toEqual(halted);
    expect(fetchFn.mock.calls[0]?.[0]).toBe(KILL_SWITCH_PATH);
    await expect(getKillSwitch(undefined, async () => json({ halted: "yes" }))).rejects.toThrow("couldn't read");
  });

  it("posts the command and raises the problem detail on refusal", async () => {
    const fetchFn = vi.fn(async (_url: string, _init?: RequestInit) => json(result(true)));
    await setKillSwitch({ scope: "TENANT", tenantId: T1, active: true }, fetchFn);
    const init = fetchFn.mock.calls[0]?.[1];
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ scope: "TENANT", tenantId: T1, active: true });
    const refused = async () =>
      json({ type: "https://passage.dev/problems/AGT-0203", status: 403, code: "AGT-0203", detail: "you do not hold duty agents.admin" }, 403, "application/problem+json");
    await expect(setKillSwitch({ scope: "GLOBAL", active: true }, refused)).rejects.toMatchObject({ status: 403, code: "AGT-0203", message: "you do not hold duty agents.admin" });
  });
});

describe("KillSwitchPage", () => {
  it("shows a loading state first", () => {
    render(<KillSwitchPage load={() => new Promise(() => {})} duties={[]} />);
    expect(screen.getByText("Loading kill switch…")).toBeInTheDocument();
  });

  it("shows a running tenant with no history and no global control for a tenant admin", async () => {
    render(<KillSwitchPage load={async () => off} duties={["agents.admin"]} />);
    // The card is the heading's grandparent (Card: head > heading).
    const tenant = (await screen.findByRole("heading", { name: "Your tenant" })).parentElement?.parentElement as HTMLElement;
    expect(within(tenant).getByText("Running")).toBeInTheDocument();
    expect(within(tenant).getByText("Nobody yet")).toBeInTheDocument();
    expect(within(tenant).getByText("Never")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Halt agent runs" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Halt all tenants" })).toBeNull();
    expect(screen.getByText("Only a platform operator can change the global switch.")).toBeInTheDocument();
    expect(pageViolations()).toEqual([]);
  });

  it("halts the tenant only after confirmation, then shows the new state", async () => {
    const load = vi.fn().mockResolvedValueOnce(off).mockResolvedValueOnce(halted);
    const toggle = vi.fn(async () => result(true));
    render(<KillSwitchPage load={load} toggle={toggle} duties={["agents.admin"]} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Halt agent runs" }));
    const dialog = screen.getByRole("alertdialog", { name: "Halt all agent runs?" });
    expect(toggle).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: "Halt agent runs" }));
    expect(toggle).toHaveBeenCalledWith({ scope: "TENANT", tenantId: T1, active: true });
    expect(await screen.findByRole("status")).toHaveTextContent("Agent runs are halted for your tenant.");
    expect(await screen.findByRole("button", { name: "Resume agent runs" })).toBeInTheDocument();
    expect(screen.getByText(ADMIN)).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("cancelling the confirmation changes nothing", async () => {
    const toggle = vi.fn(async () => result(true));
    render(<KillSwitchPage load={async () => off} toggle={toggle} duties={["agents.admin"]} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Halt agent runs" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(toggle).not.toHaveBeenCalled();
  });

  it("shows a refused toggle as an alert and keeps the page", async () => {
    const toggle = vi.fn(async () => {
      throw new Error("I cannot do that: you do not hold duty agents.admin required by the tenant kill switch.");
    });
    render(<KillSwitchPage load={async () => off} toggle={toggle} duties={[]} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Halt agent runs" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Halt agent runs" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("you do not hold duty agents.admin"));
    expect(screen.getByRole("button", { name: "Halt agent runs" })).toBeInTheDocument();
  });

  it("lets a platform operator use the global switch, and explains a tenant halted by it", async () => {
    const globalOn: KillSwitchStatus = { ...off, halted: true, global: { active: true, version: 5, changedAt: "2026-10-22T10:00:00Z" } };
    const toggle = vi.fn(async () => result(false, "GLOBAL"));
    render(<KillSwitchPage load={async () => globalOn} toggle={toggle} duties={["platform.global.admin"]} />);
    const user = userEvent.setup();
    expect(await screen.findByText("Halted by the global switch")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Resume all tenants" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Resume all tenants" }));
    expect(toggle).toHaveBeenCalledWith({ scope: "GLOBAL", active: false });
    expect(await screen.findByRole("status")).toHaveTextContent("Agent runs are resumed for every tenant.");
  });

  it("warns that a runtime out of contact with the stream halts every run", async () => {
    render(<KillSwitchPage load={async () => ({ ...off, halted: true, stale: true })} duties={["agents.admin"]} />);
    expect(await screen.findByText("Runs are halted while the runtime reconnects")).toBeInTheDocument();
  });

  it("shows a load failure with a working Try again", async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error("request failed with status 503")).mockResolvedValueOnce(off);
    render(<KillSwitchPage load={load} duties={["agents.admin"]} />);
    const user = userEvent.setup();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("status 503"));
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "Halt agent runs" })).toBeInTheDocument();
  });
});

describe("kill switch route", () => {
  it("is served by module.ts and loads the status from the agents API", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => json(halted));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <MemoryRouter initialEntries={["/copilot/kill-switch"]}>
        <Routes>
          {copilot.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("button", { name: "Resume agent runs" })).toBeInTheDocument();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(KILL_SWITCH_PATH);
  });
});
