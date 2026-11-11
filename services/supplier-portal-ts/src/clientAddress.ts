// The address a public request came from, for the per-client rate limit. The same rule as platform/go/httpx TrustedRealIP:
// X-Forwarded-For is read only when the TCP peer is a configured trusted proxy (PASSAGE_TRUSTED_PROXIES, CIDRs or bare
// addresses), and it is walked right to left past the trusted proxies, so a client cannot choose its address by sending the
// header itself or by prepending entries. With no trusted proxies the address is always the TCP peer's.

import { BlockList, isIP } from "node:net";

export const TRUSTED_PROXIES_ENV = "PASSAGE_TRUSTED_PROXIES";

export interface TrustedProxies {
  readonly list: BlockList;
  readonly size: number;
}

export const NO_TRUSTED_PROXIES: TrustedProxies = { list: new BlockList(), size: 0 };

const MAPPED_V4 = /^::ffff:(\d{1,3}(?:\.\d{1,3}){3})$/i;

/** An address in canonical family form: an IPv4-mapped IPv6 address becomes IPv4, an IPv6 zone is dropped. Undefined when it is not an address. */
function normalize(raw: string | undefined): { address: string; family: "ipv4" | "ipv6" } | undefined {
  if (raw === undefined) return undefined;
  let a = raw.trim();
  const zone = a.indexOf("%");
  if (zone >= 0) a = a.slice(0, zone);
  const mapped = MAPPED_V4.exec(a);
  if (mapped) a = mapped[1] as string;
  const family = isIP(a);
  if (family === 4) return { address: a, family: "ipv4" };
  if (family === 6) return { address: a.toLowerCase(), family: "ipv6" };
  return undefined;
}

/** Parses PASSAGE_TRUSTED_PROXIES. Empty means none. An entry that is not an address or CIDR stops the service: a typo must not silently trust nobody or everybody. */
export function parseTrustedProxies(raw: string | undefined): TrustedProxies {
  const list = new BlockList();
  let size = 0;
  for (const entry of (raw ?? "").split(",")) {
    const e = entry.trim();
    if (e === "") continue;
    const refuse = (): never => {
      throw new Error(`${TRUSTED_PROXIES_ENV}: ${JSON.stringify(e)} is not an IP address or CIDR`);
    };
    const [addr, bits, extra] = e.split("/");
    if (extra !== undefined || (bits !== undefined && !/^\d{1,3}$/.test(bits))) refuse();
    const n = normalize(addr);
    if (!n) return refuse();
    let prefix = bits === undefined ? undefined : Number(bits);
    if (prefix !== undefined && n.family === "ipv4" && MAPPED_V4.test((addr ?? "").trim())) {
      // ::ffff:a.b.c.d/n names the IPv4 range a.b.c.d/(n-96).
      if (prefix < 96) refuse();
      prefix -= 96;
    }
    const max = n.family === "ipv4" ? 32 : 128;
    prefix ??= max;
    if (prefix > max) refuse();
    list.addSubnet(n.address, prefix, n.family);
    size++;
  }
  return { list, size };
}

function trusted(a: { address: string; family: "ipv4" | "ipv6" }, proxies: TrustedProxies): boolean {
  return proxies.list.check(a.address, a.family);
}

/**
 * The client address of a request: the TCP peer, unless the peer is a trusted proxy, in which case the rightmost
 * X-Forwarded-For entry that is not itself a trusted proxy. A malformed entry stops the walk at the peer, and so does a
 * chain made only of trusted proxies. Undefined only when the peer itself is unknown (the socket is gone).
 */
export function clientAddress(peer: string | undefined, forwardedFor: string | string[] | undefined, proxies: TrustedProxies): string | undefined {
  const p = normalize(peer);
  if (!p) return undefined;
  if (proxies.size === 0 || !trusted(p, proxies)) return p.address;
  const header = Array.isArray(forwardedFor) ? forwardedFor.join(",") : (forwardedFor ?? "");
  if (header.trim() === "") return p.address;
  const hops = header.split(",");
  for (let i = hops.length - 1; i >= 0; i--) {
    const hop = normalize(hops[i]);
    if (!hop) return p.address;
    if (!trusted(hop, proxies)) return hop.address;
  }
  return p.address;
}

/** Expands an IPv6 address into its eight 16-bit groups. */
function groups(v6: string): number[] {
  let text = v6;
  const lastColon = text.lastIndexOf(":");
  const v4 = text.slice(lastColon + 1);
  if (isIP(v4) === 4) {
    const [a, b, c, d] = v4.split(".").map(Number) as [number, number, number, number];
    text = `${text.slice(0, lastColon + 1)}${((a << 8) | b).toString(16)}:${((c << 8) | d).toString(16)}`;
  }
  const [head = "", rest] = text.split("::");
  const hex = (part: string): number[] => (part === "" ? [] : part.split(":").map((g) => parseInt(g, 16)));
  const left = hex(head);
  const right = rest === undefined ? [] : hex(rest);
  const fill = rest === undefined ? [] : new Array<number>(8 - left.length - right.length).fill(0);
  return [...left, ...fill, ...right];
}

/**
 * The rate-limit key of a client address. An IPv4 address is its own key; an IPv6 address counts by its /64, the block one
 * subscriber is normally given, so a client cannot get a fresh allowance by moving to another address in its own network.
 */
export function rateKey(address: string | undefined): string {
  const n = normalize(address);
  if (!n) return "unknown";
  if (n.family === "ipv4") return n.address;
  const g = groups(n.address);
  return `${g.slice(0, 4).map((x) => x.toString(16)).join(":")}::/64`;
}
