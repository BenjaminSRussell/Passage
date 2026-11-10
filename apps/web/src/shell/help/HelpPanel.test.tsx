import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { checkAxeAA } from "../a11y";
import { HelpButton, guideUrl } from "./HelpPanel";
import { helpForRoute } from "./routeHelp";
import { parseWhatsNew } from "./whatsNew";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <HelpButton />
    </MemoryRouter>,
  );
}

describe("help panel", () => {
  it("opens with the page's purpose, tasks and guide link, and has no axe violations", async () => {
    const user = userEvent.setup();
    const view = renderAt("/purchasing/requisitions/new");
    const help = helpForRoute("/purchasing/requisitions/new");
    expect(help).toBeDefined();
    await user.click(screen.getByRole("button", { name: "Help for this page" }));
    const dialog = screen.getByRole("dialog", { name: help?.title });
    expect(dialog).toHaveTextContent(help?.purpose ?? "");
    for (const task of help?.howTo ?? []) expect(dialog).toHaveTextContent(task);
    expect(screen.getByRole("link", { name: /Read the full guide/ })).toHaveAttribute("href", guideUrl(help!.guide));
    expect(checkAxeAA(view.container).filter((v) => !["region", "bypass", "page-has-heading-one", "landmark-unique"].includes(v.id))).toEqual([]);
  });

  it("closes on Escape and returns focus to the button", async () => {
    const user = userEvent.setup();
    renderAt("/purchasing/requisitions/new");
    const button = screen.getByRole("button", { name: "Help for this page" });
    await user.click(button);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(button).toHaveFocus();
  });

  it("says so when a page has no help", async () => {
    const user = userEvent.setup();
    renderAt("/no-such-page");
    await user.click(screen.getByRole("button", { name: "Help for this page" }));
    expect(screen.getByText("There is no help for this page yet.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "What's new" })).toHaveAttribute("href", "/whats-new");
  });
});

describe("what's new", () => {
  it("parses releases and their items, newest first as written", () => {
    expect(parseWhatsNew("# Title\n\nintro\n\n## 2026-02-01\n\n- B\n- C\n\n## 2026-01-01\n\n- A\n\n## empty\n")).toEqual([
      { date: "2026-02-01", items: ["B", "C"] },
      { date: "2026-01-01", items: ["A"] },
    ]);
  });
});
