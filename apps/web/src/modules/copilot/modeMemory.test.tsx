import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { CopilotPanel } from "./CopilotPanel";
import { modeMemoryKey, readRememberedMode, rememberMode, type ModeStore } from "./domain";

function memoryStore(seed: Record<string, string> = {}): ModeStore & { data: Record<string, string> } {
  const data = { ...seed };
  return {
    data,
    getItem: (k) => (k in data ? data[k] ?? null : null),
    setItem: (k, v) => {
      data[k] = v;
    },
  };
}

describe("copilot mode memory (9.47.6)", () => {
  it("keys the remembered mode by user and workspace", () => {
    expect(modeMemoryKey("u1", "ap")).toBe("passage.copilot.mode:u1:ap");
    const store = memoryStore();
    rememberMode(store, "u1", "ap", "explain");
    expect(readRememberedMode(store, "u1", "ap")).toBe("explain");
    expect(readRememberedMode(store, "u1", "ar")).toBeUndefined();
    expect(readRememberedMode(store, "u2", "ap")).toBeUndefined();
  });

  it("ignores a stored value that is not a copilot mode", () => {
    const store = memoryStore({ [modeMemoryKey("u1", "ap")]: "delete-everything" });
    expect(readRememberedMode(store, "u1", "ap")).toBeUndefined();
  });

  it("opens in the remembered mode instead of the default", () => {
    const store = memoryStore({ [modeMemoryKey("u1", "ap")]: "summarize" });
    render(<CopilotPanel memory={{ store, userId: "u1", workspace: "ap" }} />);
    expect(screen.getByRole("button", { name: "Summarize" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Ask the data" })).toHaveAttribute("aria-pressed", "false");
  });

  it("saves a mode the user picks, and the next panel reopens in it", async () => {
    const store = memoryStore();
    const user = userEvent.setup();
    const first = render(<CopilotPanel memory={{ store, userId: "u1", workspace: "ap" }} />);
    await user.click(screen.getByRole("button", { name: "Draft" }));
    expect(store.data[modeMemoryKey("u1", "ap")]).toBe("draft");
    first.unmount();

    render(<CopilotPanel memory={{ store, userId: "u1", workspace: "ap" }} />);
    expect(screen.getByRole("button", { name: "Draft" })).toHaveAttribute("aria-pressed", "true");
  });

  it("keeps working when the store throws (private window, blocked site data)", async () => {
    const broken: ModeStore = {
      getItem: () => {
        throw new Error("SecurityError");
      },
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
    };
    const user = userEvent.setup();
    render(<CopilotPanel initialMode="explain" memory={{ store: broken, userId: "u1", workspace: "ap" }} />);
    expect(screen.getByRole("button", { name: "Explain this record" })).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByRole("button", { name: "Draft" }));
    expect(screen.getByRole("button", { name: "Draft" })).toHaveAttribute("aria-pressed", "true");
  });

  it("escapes ids so one user's key cannot collide with another's", () => {
    expect(modeMemoryKey("a.b", "c")).not.toBe(modeMemoryKey("a", "b.c"));
  });
});
