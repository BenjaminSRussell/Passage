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
}

export class UnsafeWebhookUrlError extends Error {
  constructor(reason: string) {
    super(`unsafe webhook url: ${reason}`);
    this.name = "UnsafeWebhookUrlError";
  }
}

const blocked = new BlockList();
for (const [net, prefix] of [
  ["0.0.0.0", 8],
  ["10.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["169.254.0.0", 16],
  ["172.16.0.0", 12],
  ["192.0.0.0", 24],
  ["192.168.0.0", 16],
  ["198.18.0.0", 15],
  ["224.0.0.0", 4],
  ["240.0.0.0", 4],
] as const) {
  blocked.addSubnet(net, prefix, "ipv4");
}
for (const [net, prefix] of [
  ["::", 128],
  ["::1", 128],
  ["64:ff9b::", 96],
  ["fc00::", 7],
  ["fe80::", 10],
  ["ff00::", 8],
] as const) {
  blocked.addSubnet(net, prefix, "ipv6");
}

function mappedIPv4(address: string): string | null {
  const m = /^::ffff:(.+)$/i.exec(address);
  if (m === null) {
    return null;
  }
  if (isIP(m[1] as string) === 4) {
    return m[1] as string;
  }
  const hex = /^([0-9a-f]{1,4}):([0-9a-f]{1,4})$/i.exec(m[1] as string);
  if (hex === null) {
    return null;
  }
  const hi = parseInt(hex[1] as string, 16);
  const lo = parseInt(hex[2] as string, 16);
  return `${hi >> 8}.${hi & 255}.${lo >> 8}.${lo & 255}`;
}

/** True for loopback, private, link-local, CGNAT, unspecified, multicast, reserved and metadata addresses. */
export function isBlockedAddress(address: string): boolean {
  const family = isIP(address);
  if (family === 0) {
    return true;
  }
  if (family === 6) {
    const v4 = mappedIPv4(address);
    if (v4 !== null) {
      return blocked.check(v4, "ipv4");
    }
    return blocked.check(address, "ipv6");
  }
  return blocked.check(address, "ipv4");
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
  const bad = addresses.find(isBlockedAddress);
  if (bad !== undefined) {
    throw new UnsafeWebhookUrlError(`host resolves to a non-public address (${bad})`);
  }
  const address = addresses[0] as string;
  return { url, address, family: isIP(address) === 6 ? 6 : 4 };
}
