import { checkAxeAA, type AxeViolation } from "../../shell/a11y";

/** Rules the shell satisfies (h1, nav, main, skip link); a page rendered alone cannot. */
const SHELL_RULES = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

export function pageViolations(root: ParentNode = document.body): AxeViolation[] {
  return checkAxeAA(root).filter((v) => !SHELL_RULES.has(v.id));
}
