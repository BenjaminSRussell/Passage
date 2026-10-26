import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

test.describe("Notes form", () => {
  test("empty required field shows an associated error and focuses the first invalid field", async ({ page }) => {
    await new ShellPage(page).goto("/notes");
    const title = page.getByLabel("Title");
    await page.getByRole("button", { name: "Save Note" }).click();

    const error = page.getByRole("alert").filter({ hasText: "Title is required" });
    await expect(error).toBeVisible();
    await expect(title).toHaveAttribute("aria-invalid", "true");
    const errorId = await error.getAttribute("id");
    expect(errorId).toBeTruthy();
    await expect(title).toHaveAttribute("aria-describedby", new RegExp(`\\b${errorId}\\b`));
    await expect(title).toBeFocused();
    await expect(page.getByRole("table", { name: "Note list" }).getByText("No rows")).toBeVisible();
  });

  test("a valid save adds a row and announces it", async ({ page }) => {
    await new ShellPage(page).goto("/notes");
    await page.getByLabel("Title").fill("Quarterly review");
    await page.getByRole("button", { name: "Save Note" }).click();

    const table = page.getByRole("table", { name: "Note list" });
    await expect(table.getByRole("button", { name: "Quarterly review" })).toBeVisible();
    await expect(table.getByText("No rows")).toHaveCount(0);
    await expect(page.getByRole("status").filter({ hasText: "Note saved" }).first()).toBeVisible();
    await expect(page.getByLabel("Title")).toHaveValue("");
  });
});
