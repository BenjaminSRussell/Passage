import { lookup } from "node:dns/promises";
import { BlockList, isIP } from "node:net";

export interface PinnedTarget {
  url: URL;
  address: string;
  family: 4 | 6;
}

export type Resolver = (hostname: string) => Promise<string[]>;

export interface GuardOptions {
  resolve?: Resolver;
  allowHttp?: boolean;
  /** Operator allowlist for private addresses (INTEGRATION_HUB_EGRESS_ALLOW); default: read from the environment. */
  egressAllow?: EgressPolicy;
}

export class UnsafeWebhookUrlError extends Error {
  constructor(reason: string) {
    super(`unsafe webhook url: ${reason}`);
    this.name = "UnsafeWebhookUrlError";
  }
}

// Always denied, whatever the operator allows: unspecified, CGNAT, loopback, link-local (cloud metadata),
// protocol assignments, benchmarking, multicast and reserved.
const blocked = new BlockList();
for (const [net, prefix] of [
  ["0.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["169.254.0.0", 16],
  ["192.0.0.0", 24],
  ["198.18.0.0", 15],
  ["224.0.0.0", 4],
  ["240.0.0.0", 4],
] as const) {
  blocked.addSubnet(net, prefix, "ipv4");
}
// Private (RFC 1918 and fc00::/7): reachable only when the operator lists them.
const priv = new BlockList();
for (const [net, prefix] of [
  ["10.0.0.0", 8],
  ["172.16.0.0", 12],
  ["192.168.0.0", 16],
] as const) {
  priv.addSubnet(net, prefix, "ipv4");
}

export type AddressClass = "public" | "private" | "denied";

function ipv6Groups(address: string): number[] {
  const [head = "", tail] = new URL(`http://[${address}]`).hostname.slice(1, -1).split("::");
  const side = (part: string) => (part === "" ? [] : part.split(":").map((g) => parseInt(g, 16)));
  const front = side(head);
  const back = tail === undefined ? [] : side(tail);
  return tail === undefined ? front : [...front, ...Array<number>(8 - front.length - back.length).fill(0), ...back];
}

function dotted(hi: number, lo: number): string {
  return `${hi >> 8}.${hi & 255}.${lo >> 8}.${lo & 255}`;
}

function classify4(address: string): AddressClass {
  return blocked.check(address, "ipv4") ? "denied" : priv.check(address, "ipv4") ? "private" : "public";
}

function classify6(address: string): AddressClass {
  const g = ipv6Groups(address) as [number, number, number, number, number, number, number, number];
  if (g.slice(0, 5).every((x) => x === 0) && g[5] === 0xffff) {
    return classify4(dotted(g[6], g[7]));
  }
  if ((g[0] & 0xfe00) === 0xfc00) {
    return "private";
  }
  if ((g[0] & 0xe000) !== 0x2000) {
    return "denied";
  }
  if (g[0] === 0x2001 && (g[1] < 0x200 || g[1] === 0x0db8)) {
    return "denied";
  }
  if (g[0] === 0x2002) {
    // A private IPv4 is never allowed through a translation prefix.
    return classify4(dotted(g[1], g[2])) === "public" ? "public" : "denied";
  }
  return "public";
}

/** How the guard treats one address: public, private (operator allowlist only) or always denied. */
export function classifyAddress(address: string): AddressClass {
  const family = address.includes("%") ? 0 : isIP(address);
  if (family === 0) {
    return "denied";
  }
  return family === 6 ? classify6(address) : classify4(address);
}

/** True for every address that is not public, including private ranges the operator may allow. */
export function isBlockedAddress(address: string): boolean {
  return classifyAddress(address) !== "public";
}

/**
 * The operator allowlist for private addresses (INTEGRATION_HUB_EGRESS_ALLOW, same rules as services/migration's
 * MIG_EGRESS_ALLOW): comma, space or newline separated CIDRs ("10.20.0.0/16", "fd00::/8") and host:port entries
 * ("erp.corp.example:443", "10.1.2.3:443", "[fd00::5]:443"). Empty allows no private address. Loopback, link-local,
 * metadata and the other always-denied ranges stay denied whatever the list says. Deployment configuration, never tenant-settable.
 */
export class EgressPolicy {
  readonly #cidrs = new BlockList();
  readonly #hosts = new Set<string>();

  constructor(raw = "") {
    for (const entry of raw.split(/[,\s]+/).filter((e) => e !== "")) {
      if (entry.includes("/")) {
        const [net = "", bits = ""] = entry.split("/");
        const family = isIP(net);
        const n = Number(bits);
        if (family === 0 || !/^\d+$/.test(bits) || n > (family === 4 ? 32 : 128)) {
          throw new Error(`INTEGRATION_HUB_EGRESS_ALLOW entry ${JSON.stringify(entry)} is not a CIDR`);
        }
        this.#cidrs.addSubnet(net, n, family === 4 ? "ipv4" : "ipv6");
        continue;
      }
      const at = entry.lastIndexOf(":");
      const host = entry.slice(0, at).replace(/^\[|\]$/g, "");
      const port = entry.slice(at + 1);
      if (at < 1 || host === "" || !/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535) {
        throw new Error(`INTEGRATION_HUB_EGRESS_ALLOW entry ${JSON.stringify(entry)} is neither a CIDR nor host:port`);
      }
      this.#hosts.add(EgressPolicy.#key(host, port));
    }
  }

  static #key(host: string, port: string): string {
    return `${unmap(host).toLowerCase()}:${port}`;
  }

  allows(address: string, host: string, port: string): boolean {
    const a = unmap(address);
    const family = isIP(a);
    return (
      (family !== 0 && this.#cidrs.check(a, family === 4 ? "ipv4" : "ipv6")) ||
      this.#hosts.has(EgressPolicy.#key(host, port)) ||
      this.#hosts.has(EgressPolicy.#key(a, port))
    );
  }
}

function unmap(address: string): string {
  const m = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(address);
  if (m !== null) {
    return m[1] as string;
  }
  if (isIP(address) === 6 && /^::ffff:/i.test(address)) {
    const g = ipv6Groups(address);
    return dotted(g[6] as number, g[7] as number);
  }
  return address;
}

export function egressPolicyFromEnv(env: NodeJS.ProcessEnv = process.env): EgressPolicy {
  return new EgressPolicy(env["INTEGRATION_HUB_EGRESS_ALLOW"] ?? "");
}

const systemResolver: Resolver = async (hostname) => (await lookup(hostname, { all: true })).map((r) => r.address);

export function allowHttpFromEnv(env: NodeJS.ProcessEnv = process.env): boolean {
  return env["INTEGRATION_HUB_ALLOW_HTTP"] === "1";
}

/**
 * Parses and resolves a subscription URL and returns the address the connection
 * must be pinned to. Rejects non-https URLs (http only with allowHttp), embedded
 * credentials, and any host that resolves to a blocked address.
 */
export async function validateWebhookUrl(raw: string, options: GuardOptions = {}): Promise<PinnedTarget> {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new UnsafeWebhookUrlError("not a valid URL");
  }
  const allowHttp = options.allowHttp ?? allowHttpFromEnv();
  if (url.protocol !== "https:" && !(allowHttp && url.protocol === "http:")) {
    throw new UnsafeWebhookUrlError(`scheme ${url.protocol} is not allowed`);
  }
  if (url.username !== "" || url.password !== "") {
    throw new UnsafeWebhookUrlError("credentials in the URL are not allowed");
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, "");
  const addresses = isIP(hostname) === 0 ? await (options.resolve ?? systemResolver)(hostname).catch(() => []) : [hostname];
  if (addresses.length === 0) {
    throw new UnsafeWebhookUrlError("host does not resolve");
  }
  const policy = options.egressAllow ?? egressPolicyFromEnv();
  const port = url.port !== "" ? url.port : url.protocol === "https:" ? "443" : "80";
  for (const address of addresses) {
    const kind = classifyAddress(address);
    if (kind === "denied" || (kind === "private" && !policy.allows(address, hostname, port))) {
      throw new UnsafeWebhookUrlError(`host resolves to a non-public address (${address})`);
    }
  }
  const address = addresses[0] as string;
  return { url, address, family: isIP(address) === 6 ? 6 : 4 };
}
