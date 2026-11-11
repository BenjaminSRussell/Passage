import { describe, expect, it } from "vitest";
import { clientAddress, NO_TRUSTED_PROXIES, parseTrustedProxies, rateKey } from "../src/clientAddress.ts";

describe("trusted proxies", () => {
  it("parse CIDRs and bare addresses of both families, and empty means none", () => {
    expect(parseTrustedProxies(undefined).size).toBe(0);
    expect(parseTrustedProxies(" , ").size).toBe(0);
    const p = parseTrustedProxies("10.0.0.0/8, 192.0.2.10, fd00::/8, ::ffff:172.16.0.0/108");
    expect(p.size).toBe(4);
    expect(p.list.check("10.200.1.1", "ipv4")).toBe(true);
    expect(p.list.check("192.0.2.10", "ipv4")).toBe(true);
    expect(p.list.check("192.0.2.11", "ipv4")).toBe(false);
    expect(p.list.check("fd12::1", "ipv6")).toBe(true);
    expect(p.list.check("172.16.5.5", "ipv4")).toBe(true);
    expect(p.list.check("172.32.0.1", "ipv4")).toBe(false);
  });

  it("refuse an entry that is not an address or CIDR instead of ignoring it", () => {
    for (const bad of ["10.0.0.0/33", "10.0.0.0/8/1", "proxy.internal", "10.0.0.0/x", "fd00::/129", "::ffff:10.0.0.0/64", "10.0.0.0/-1"]) {
      expect(() => parseTrustedProxies(bad), bad).toThrow(/PASSAGE_TRUSTED_PROXIES/);
    }
  });
});

describe("client address", () => {
  const edge = parseTrustedProxies("10.0.0.0/8");

  it("is the TCP peer when no proxy is trusted, whatever the header says", () => {
    expect(clientAddress("203.0.113.5", "198.51.100.1", NO_TRUSTED_PROXIES)).toBe("203.0.113.5");
    expect(clientAddress("::ffff:203.0.113.5", undefined, NO_TRUSTED_PROXIES)).toBe("203.0.113.5");
    expect(clientAddress(undefined, "198.51.100.1", NO_TRUSTED_PROXIES)).toBeUndefined();
  });

  it("is the TCP peer when the peer is not a trusted proxy", () => {
    expect(clientAddress("203.0.113.5", "198.51.100.1", edge)).toBe("203.0.113.5");
  });

  it("is the rightmost entry that is not a trusted proxy when the peer is one", () => {
    expect(clientAddress("10.0.0.2", "198.51.100.1", edge)).toBe("198.51.100.1");
    expect(clientAddress("::ffff:10.0.0.2", "6.6.6.6, 198.51.100.1, 10.9.9.9", edge)).toBe("198.51.100.1");
    expect(clientAddress("10.0.0.2", ["6.6.6.6", "198.51.100.1"], edge)).toBe("198.51.100.1");
  });

  it("stops at the peer on a malformed hop, a chain of only proxies, or no header", () => {
    expect(clientAddress("10.0.0.2", "198.51.100.1, not-an-ip", edge)).toBe("10.0.0.2");
    expect(clientAddress("10.0.0.2", "198.51.100.1:4711", edge)).toBe("10.0.0.2");
    expect(clientAddress("10.0.0.2", "10.0.0.3, 10.0.0.4", edge)).toBe("10.0.0.2");
    expect(clientAddress("10.0.0.2", undefined, edge)).toBe("10.0.0.2");
    expect(clientAddress("10.0.0.2", " ", edge)).toBe("10.0.0.2");
  });
});

describe("rate key", () => {
  it("is the IPv4 address itself", () => {
    expect(rateKey("198.51.100.7")).toBe("198.51.100.7");
  });

  it("counts an IPv6 client by its /64", () => {
    expect(rateKey("2001:db8:1:2:aaaa::1")).toBe("2001:db8:1:2::/64");
    expect(rateKey("2001:DB8:1:2:ffff:ffff:ffff:fffe")).toBe("2001:db8:1:2::/64");
    expect(rateKey("2001:db8::1")).toBe("2001:db8:0:0::/64");
    expect(rateKey("::1")).toBe("0:0:0:0::/64");
    expect(rateKey("fe80::1%eth0")).toBe("fe80:0:0:0::/64");
    expect(rateKey("64:ff9b::198.51.100.7")).toBe("64:ff9b:0:0::/64");
  });

  it("is a fixed key for an unknown peer", () => {
    expect(rateKey(undefined)).toBe("unknown");
    expect(rateKey("garbage")).toBe("unknown");
  });
});
