/** In-repo WCAG 2.1 AA checks used because apps/web cannot take new npm deps (Playwright/axe). */
export interface AxeViolation {
  id: string;
  nodes: string[];
}

function hasName(el: Element, root: ParentNode): boolean {
  if (el.getAttribute("aria-label")) {
    return true;
  }
  const labelledBy = el.getAttribute("aria-labelledby");
  return Boolean(labelledBy && labelledBy.split(/\s+/).some((id) => root.querySelector(`[id="${id}"]`)));
}

export function checkAxeAA(root: ParentNode): AxeViolation[] {
  const violations: AxeViolation[] = [];
  const h1 = root.querySelectorAll("h1");
  if (h1.length !== 1) {
    violations.push({ id: "page-has-heading-one", nodes: [`count=${h1.length}`] });
  }
  if (!root.querySelector("nav[aria-label], nav[aria-labelledby]")) {
    violations.push({ id: "landmark-unique", nodes: ["nav"] });
  }
  if (!root.querySelector("main")) {
    violations.push({ id: "region", nodes: ["main"] });
  }
  if (!root.querySelector("a[href='#main'], a[href='#content']")) {
    violations.push({ id: "bypass", nodes: ["skip-link"] });
  }
  const unlabeled: string[] = [];
  root.querySelectorAll("input, select, textarea").forEach((el) => {
    const id = el.getAttribute("id");
    const labelled = el.getAttribute("aria-label") || el.getAttribute("aria-labelledby") || (id && root.querySelector(`label[for="${id}"]`));
    if (!labelled) {
      unlabeled.push(el.tagName.toLowerCase());
    }
  });
  if (unlabeled.length) {
    violations.push({ id: "label", nodes: unlabeled });
  }
  const nameless: string[] = [];
  root.querySelectorAll("button, a").forEach((el) => {
    const name = (el.getAttribute("aria-label") || el.textContent || "").trim();
    if (!name) {
      nameless.push(el.tagName.toLowerCase());
    }
  });
  if (nameless.length) {
    violations.push({ id: "button-name", nodes: nameless });
  }
  const unnamedDialogs = Array.from(root.querySelectorAll("[role=dialog]")).filter((el) => !hasName(el, root));
  if (unnamedDialogs.length) {
    violations.push({ id: "dialog-name", nodes: unnamedDialogs.map((el) => el.tagName.toLowerCase()) });
  }
  const incompleteComboboxes = Array.from(root.querySelectorAll("[role=combobox]")).filter(
    (el) => !el.hasAttribute("aria-controls") || !el.hasAttribute("aria-expanded"),
  );
  if (incompleteComboboxes.length) {
    violations.push({ id: "aria-required-attr", nodes: incompleteComboboxes.map((el) => el.tagName.toLowerCase()) });
  }
  const seen = new Set<string>();
  const duplicates = new Set<string>();
  root.querySelectorAll("[id]").forEach((el) => {
    const id = el.getAttribute("id") ?? "";
    if (seen.has(id)) {
      duplicates.add(id);
    }
    seen.add(id);
  });
  if (duplicates.size) {
    violations.push({ id: "duplicate-id", nodes: [...duplicates] });
  }
  const unnamedSvgs = Array.from(root.querySelectorAll("svg")).filter(
    (el) => el.getAttribute("aria-hidden") !== "true" && !(el.getAttribute("role") === "img" && hasName(el, root)),
  );
  if (unnamedSvgs.length) {
    violations.push({ id: "svg-img-alt", nodes: unnamedSvgs.map(() => "svg") });
  }
  return violations;
}
