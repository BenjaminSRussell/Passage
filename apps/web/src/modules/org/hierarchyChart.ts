import type { TreeNode } from "./hierarchyTree";

export interface ChartHeader {
  typeCode: string;
  typeName: string;
  purposes: readonly string[];
  asOf: string;
}

export interface ChartFooter {
  versionNo: number;
  printedAt: string;
}

export interface ChartLabel {
  code: string;
  name: string;
  /** Text after "Manager:"; null prints "none", undefined (a node kind with no manager) prints no manager line. */
  manager?: string | null;
}

const BOX_W = 168;
const BOX_H = 58;
const GAP_X = 16;
const GAP_Y = 36;
const MARGIN = 24;
const HEADER_H = 64;
const FOOTER_H = 36;
const MIN_W = 480;

/** Above this many nodes the designer asks before exporting: the chart is far wider than any page and slow to open. */
export const CHART_WARN_NODES = 1000;

// XML 1.0 forbids C0 controls other than tab, newline and carriage return, U+FFFE, U+FFFF and unpaired surrogates;
// one of them in a unit name would make the whole file unreadable, so they are dropped.
const NOT_XML = /[\u0000-\u0008\u000B\u000C\u000E-\u001F￾￿]|[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g;

export const escapeXml = (text: string) =>
  text
    .replace(NOT_XML, "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&apos;");

// Counts code points so a cut never splits a surrogate pair.
const clip = (text: string, max: number) => {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max - 1).join("")}…` : text;
};

interface Placed {
  id: string;
  x: number;
  y: number;
  parent: Placed | null;
}

// Each leaf takes one box column; a parent is centred over its children. One visit per node.
function place(roots: readonly TreeNode[]): { placed: Placed[]; width: number; depth: number } {
  const placed: Placed[] = [];
  let nextLeaf = 0;
  let depth = 0;
  const walk = (tree: TreeNode, level: number, parent: Placed | null): Placed => {
    depth = Math.max(depth, level + 1);
    const self: Placed = { id: tree.node.orgUnitId, x: 0, y: level * (BOX_H + GAP_Y), parent };
    placed.push(self);
    if (tree.children.length === 0) {
      self.x = nextLeaf * (BOX_W + GAP_X);
      nextLeaf += 1;
    } else {
      const kids = tree.children.map((child) => walk(child, level + 1, self));
      self.x = (kids[0].x + kids[kids.length - 1].x) / 2;
    }
    return self;
  };
  roots.forEach((root) => walk(root, 0, null));
  return { placed, width: Math.max(nextLeaf, 1) * (BOX_W + GAP_X) - GAP_X, depth };
}

const size = (width: number, depth: number) => {
  const bodyH = Math.max(depth, 1) * (BOX_H + GAP_Y) - GAP_Y;
  return { bodyH, width: Math.max(width + MARGIN * 2, MIN_W), height: MARGIN + HEADER_H + bodyH + FOOTER_H + MARGIN };
};

/** The chart's size in pixels, for warning before a very large export. */
export function chartSize(roots: readonly TreeNode[]): { width: number; height: number } {
  const { width, depth } = place(roots);
  const { width: totalW, height } = size(width, depth);
  return { width: totalW, height };
}

export function hierarchyChartSvg(
  roots: readonly TreeNode[],
  describe: (id: string) => ChartLabel,
  header: ChartHeader,
  footer: ChartFooter,
): string {
  const { placed, width, depth } = place(roots);
  const { bodyH, width: totalW, height: totalH } = size(width, depth);
  const offsetX = (totalW - width) / 2;
  const offsetY = MARGIN + HEADER_H;
  const purposes = header.purposes.length > 0 ? header.purposes.join(", ") : "none";
  const title = `Organization chart, ${header.typeCode} ${header.typeName}, as of ${header.asOf}`;
  const desc = `${placed.length} ${placed.length === 1 ? "node" : "nodes"}, ${depth} ${depth === 1 ? "level" : "levels"}. Purpose: ${purposes}. Version ${footer.versionNo}, printed ${footer.printedAt}.`;
  const out: string[] = [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${totalW}" height="${totalH}" viewBox="0 0 ${totalW} ${totalH}" role="img" aria-labelledby="chart-title" aria-describedby="chart-desc" font-family="Helvetica, Arial, sans-serif">`,
    `<title id="chart-title">${escapeXml(title)}</title>`,
    `<desc id="chart-desc">${escapeXml(desc)}</desc>`,
    `<rect width="100%" height="100%" fill="#ffffff"/>`,
    `<text x="${MARGIN}" y="${MARGIN + 18}" font-size="18" font-weight="bold" fill="#111111">${escapeXml(`${header.typeCode} · ${header.typeName}`)}</text>`,
    `<text x="${MARGIN}" y="${MARGIN + 40}" font-size="12" fill="#333333">${escapeXml(`Purpose: ${purposes}    As of: ${header.asOf}`)}</text>`,
  ];
  for (const box of placed) {
    if (box.parent) {
      const x1 = offsetX + box.parent.x + BOX_W / 2;
      const y1 = offsetY + box.parent.y + BOX_H;
      const x2 = offsetX + box.x + BOX_W / 2;
      const y2 = offsetY + box.y;
      const mid = (y1 + y2) / 2;
      out.push(`<path d="M${x1} ${y1} V${mid} H${x2} V${y2}" fill="none" stroke="#777777" stroke-width="1"/>`);
    }
  }
  // Each box is a nested viewport, so text that is still too wide after clipping is cut at the box edge.
  for (const box of placed) {
    const label = describe(box.id);
    const manager =
      label.manager === undefined ? "" : `<text x="8" y="49" font-size="10" fill="#555555">${escapeXml(`Manager: ${label.manager === null ? "none" : clip(label.manager, 24)}`)}</text>`;
    out.push(
      `<svg x="${offsetX + box.x}" y="${offsetY + box.y}" width="${BOX_W}" height="${BOX_H}" overflow="hidden">` +
        `<rect x="0.5" y="0.5" width="${BOX_W - 1}" height="${BOX_H - 1}" rx="4" fill="#f4f6f8" stroke="#333333" stroke-width="1"/>` +
        `<text x="8" y="18" font-size="12" font-weight="bold" fill="#111111">${escapeXml(clip(label.code, 22))}</text>` +
        `<text x="8" y="34" font-size="11" fill="#111111">${escapeXml(clip(label.name, 26))}</text>${manager}</svg>`,
    );
  }
  const footerY = offsetY + bodyH + MARGIN + 12;
  out.push(
    `<text x="${MARGIN}" y="${footerY}" font-size="11" fill="#333333">${escapeXml(`Version ${footer.versionNo}    Printed ${footer.printedAt}`)}</text>`,
    `</svg>`,
  );
  return out.join("\n");
}
