import { checkAxeAA, type AxeViolation } from "../../shell/a11y";

/** Rules the shell satisfies (h1, nav, main, skip link); a page rendered alone cannot. */
const SHELL_RULES = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

export function pageViolations(root: ParentNode = document.body): AxeViolation[] {
  return checkAxeAA(root).filter((v) => !SHELL_RULES.has(v.id));
}

const PANEL_HEADING_NAMES = ["Tools called", "Records read"];

export function panelHeadingSteps(root: ParentNode = document.body): number[] {
  const levelOf = (h: Element) => Number(h.tagName.slice(1));
  let host: number | undefined;
  const steps: number[] = [];
  for (const heading of root.querySelectorAll("h1,h2,h3,h4,h5,h6")) {
    if (PANEL_HEADING_NAMES.includes(heading.textContent ?? "")) steps.push(levelOf(heading) - (host ?? 0));
    else host = levelOf(heading);
  }
  return steps;
}
