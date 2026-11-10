declare module "node:fs" {
  export function readdirSync(path: URL): string[];
  export function readFileSync(path: URL, encoding: "utf8"): string;
}
