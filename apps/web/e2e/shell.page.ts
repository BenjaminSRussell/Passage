import type { Locator, Page } from "@playwright/test";

/** Page Object for the Passage shell: landmarks, search palette and mobile drawer. */
export class ShellPage {
  readonly skipLink: Locator;
  readonly main: Locator;
  readonly title: Locator;
  readonly workspaceNav: Locator;
  readonly status: Locator;
  readonly searchButton: Locator;
  readonly menuButton: Locator;
  readonly closeNavButton: Locator;
  readonly drawer: Locator;
  readonly palette: Locator;
  readonly paletteInput: Locator;
  readonly paletteOptions: Locator;

  constructor(readonly page: Page) {
    this.skipLink = page.getByRole("link", { name: "Skip to content" });
    this.main = page.getByRole("main");
    this.title = page.getByRole("heading", { level: 1 });
    this.workspaceNav = page.getByRole("navigation", { name: "Workspaces" });
    this.status = page.getByRole("status");
    this.searchButton = page.getByRole("button", { name: "Search", exact: true });
    this.menuButton = page.getByRole("button", { name: "Open navigation" });
    this.closeNavButton = page.getByRole("button", { name: "Close navigation" });
    this.drawer = page.getByRole("dialog", { name: "Navigation" });
    this.palette = page.getByRole("dialog", { name: "Search" });
    this.paletteInput = page.getByRole("combobox", { name: "Find a page" });
    this.paletteOptions = page.getByRole("option");
  }

  async goto(path: string): Promise<void> {
    await this.page.goto(path);
    await this.title.first().waitFor();
  }

  async openPaletteWithShortcut(): Promise<void> {
    await this.page.keyboard.press("ControlOrMeta+k");
    await this.paletteInput.waitFor();
  }

  async openDrawer(): Promise<void> {
    await this.menuButton.click();
    await this.drawer.waitFor();
  }
}
