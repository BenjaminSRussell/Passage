/** In-repo WCAG 2.1 AA checks used because apps/web cannot take new npm deps (Playwright/axe). */
export interface AxeViolation {
  id: string;
  nodes: string[];
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
  return violations;
}
