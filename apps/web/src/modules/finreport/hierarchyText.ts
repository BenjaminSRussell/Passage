// Text forms of hierarchy membership rules and members, so a user can type them
// into the hierarchy editor and the service still receives structured JSON.
import type { HierarchyMember, HierarchyRule } from "./api/client";

/**
 * One rule per line:
 *   100000..199999         key range (inclusive, compared as text)
 *   61*, 6200              key values; a trailing * is a prefix wildcard
 *   region=EMEA, APAC      values of a member attribute
 *   segment=10..19         range over a member attribute
 */
export function parseRules(text: string): HierarchyRule[] {
  const out: HierarchyRule[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    let attribute: string | undefined;
    let body = line;
    const eq = line.indexOf("=");
    if (eq > 0) {
      attribute = line.slice(0, eq).trim();
      body = line.slice(eq + 1).trim();
    }
    const rule: HierarchyRule = attribute ? { attribute } : {};
    const range = body.split("..");
    if (range.length === 2) {
      rule.from = range[0].trim();
      rule.to = range[1].trim();
    } else {
      rule.values = body
        .split(",")
        .map((v) => v.trim())
        .filter((v) => v !== "");
    }
    out.push(rule);
  }
  return out;
}

export function formatRules(rules: HierarchyRule[] | undefined): string {
  return (rules ?? [])
    .map((r) => {
      const body = r.from !== undefined && r.from !== "" ? `${r.from}..${r.to ?? ""}` : (r.values ?? []).join(", ");
      return r.attribute ? `${r.attribute}=${body}` : body;
    })
    .join("\n");
}

/**
 * One member per line: id, key, then optional attribute=value pairs, separated
 * by commas, such as "acct-110100, 110100, type=bank". Returns the members or
 * the first line that cannot be read.
 */
export function parseMembers(text: string): { members: HierarchyMember[]; error?: string } {
  const members: HierarchyMember[] = [];
  const lines = text.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (line === "") continue;
    const parts = line.split(",").map((p) => p.trim());
    const [id, key, ...rest] = parts;
    if (!id || !key || id.includes("=") || key.includes("=")) {
      return { members, error: `Line ${i + 1}: write the member id and key first, such as "acct-110100, 110100".` };
    }
    const member: HierarchyMember = { id, key };
    for (const pair of rest) {
      const eq = pair.indexOf("=");
      if (eq <= 0) {
        return { members, error: `Line ${i + 1}: "${pair}" is not an attribute=value pair.` };
      }
      member.attributes = { ...member.attributes, [pair.slice(0, eq).trim()]: pair.slice(eq + 1).trim() };
    }
    members.push(member);
  }
  return { members };
}
