import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { Shell } from "../../shell/Shell";
import { defaultRoutes, json, openType, serve } from "./hierarchyTestKit";
import org from "./module";
import { stubLocalStorage } from "./testStorage";

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

async function openHierarchyThroughShell(duties: string[]) {
  serve({ ...defaultRoutes, "GET /hierarchy-types/t1/versions": () => json({ items: [] }) });
  const router = createMemoryRouter([{ element: <Shell modules={[org]} duties={duties} />, children: org.routes }], { initialEntries: ["/org/hierarchy"] });
  const user = userEvent.setup();
  render(<RouterProvider router={router} />);
  await openType(user);
}

describe("hierarchy designer behind the shell", () => {
  it("offers editing when the shell principal holds org.hierarchy.write", async () => {
    await openHierarchyThroughShell(["org.read", "org.hierarchy.write", "tenant.administer"]);
    expect(await screen.findByRole("button", { name: "Create draft" })).toBeInTheDocument();
  });

  it("hides editing when the shell principal lacks the duty", async () => {
    await openHierarchyThroughShell(["org.read"]);
    expect(await screen.findByText(/needs the hierarchy write duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create draft" })).toBeNull();
  });
});
