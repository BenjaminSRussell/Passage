import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";

import { httpTransport } from "../src/dispatcher.ts";
import { isBlockedAddress, validateWebhookUrl, UnsafeWebhookUrlError } from "../src/ssrf.ts";
import { nextStep } from "../src/dispatch.ts";

const publicResolver = async () => ["93.184.216.34"];

test("http is rejected unless explicitly allowed", async () => {
  await assert.rejects(validateWebhookUrl("http://example.com/h", { resolve: publicResolver, allowHttp: false }), UnsafeWebhookUrlError);
  const ok = await validateWebhookUrl("http://example.com/h", { resolve: publicResolver, allowHttp: true });
  assert.equal(ok.address, "93.184.216.34");
});

test("non-http schemes, credentials and garbage are rejected", async () => {
  for (const url of ["ftp://example.com/", "file:///etc/passwd", "https://user:pw@example.com/", "not a url"]) {
    await assert.rejects(validateWebhookUrl(url, { resolve: publicResolver, allowHttp: true }), UnsafeWebhookUrlError, url);
  }
});

test("every private, loopback, link-local, CGNAT, unspecified, multicast and metadata address is blocked", () => {
  const blockedAddresses = [
    "127.0.0.1", "127.1.2.3", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1",
    "169.254.169.254", "169.254.170.2", "100.64.0.1", "100.127.255.255", "0.0.0.0", "224.0.0.1", "255.255.255.255",
    "::", "::1", "fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:7f00:1", "64:ff9b::a00:1",
    "2002:7f00:0001::", "2002:a9fe:a9fe::1", "2002:0a00:0001::", "64:ff9b:1::a9fe:a9fe", "::127.0.0.1", "::7f00:1", "::a9fe:a9fe",
    "::ffff:0:a9fe:a9fe", "fe80::1%eth0", "FE80::1%en0", "FD00:EC2::254", "::FFFF:7F00:1", "2002:7F00:0001::", "2001:db8::1", "2001:2::1", "2001:10::1", "2001:20::1", "2001:1ff::1", "::ffff:0:127.0.0.1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1", "::ffff:10.0.0.1",
  ];
  for (const address of blockedAddresses) {
    assert.equal(isBlockedAddress(address), true, address);
  }
});

test("public addresses are not blocked", () => {
  for (const address of ["93.184.216.34", "8.8.8.8", "172.15.255.255", "172.32.0.1", "100.63.255.255", "2606:4700::1111", "::ffff:8.8.8.8", "2002:0808:0808::1", "2a00:1450:4001::200e"]) {
    assert.equal(isBlockedAddress(address), false, address);
  }
});

test("IP literals are validated without resolving", async () => {
  const neverResolve = async () => {
    throw new Error("must not resolve");
  };
  for (const url of ["https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/latest/meta-data", "https://10.1.2.3/"]) {
    await assert.rejects(validateWebhookUrl(url, { resolve: neverResolve }), UnsafeWebhookUrlError, url);
  }
});

test("a hostname resolving to a private address is rejected, even when another record is public", async () => {
  await assert.rejects(validateWebhookUrl("https://evil.example/", { resolve: async () => ["10.0.0.5"] }), UnsafeWebhookUrlError);
  await assert.rejects(validateWebhookUrl("https://evil.example/", { resolve: async () => ["93.184.216.34", "169.254.169.254"] }), UnsafeWebhookUrlError);
});

test("an unresolvable host is rejected", async () => {
  await assert.rejects(validateWebhookUrl("https://nx.example/", { resolve: async () => { throw new Error("ENOTFOUND"); } }), UnsafeWebhookUrlError);
});

test("public https is allowed and pinned to the resolved address", async () => {
  const pinned = await validateWebhookUrl("https://hooks.example.com/a", { resolve: async () => ["2606:4700::1111", "93.184.216.34"] });
  assert.equal(pinned.address, "2606:4700::1111");
  assert.equal(pinned.family, 6);
  assert.equal(pinned.url.hostname, "hooks.example.com");
});

async function listen(handler: Parameters<typeof createServer>[1]): Promise<{ server: Server; port: number }> {
  const server = createServer(handler);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const addr = server.address();
  return { server, port: typeof addr === "object" && addr ? addr.port : 0 };
}

test("httpTransport sends nothing for a blocked target", async () => {
  let hits = 0;
  const { server, port } = await listen((_req, res) => {
    hits += 1;
    res.end();
  });
  try {
    const status = await httpTransport({ timeoutMs: 1000 })(`http://127.0.0.1:${port}/`, {}, "{}");
    assert.equal(status, null);
    const viaHost = await httpTransport({ timeoutMs: 1000, allowHttp: true, resolve: async () => ["127.0.0.1"] })(`http://local.example:${port}/`, {}, "{}");
    assert.equal(viaHost, null);
    assert.equal(hits, 0);
  } finally {
    server.close();
  }
});

test("httpTransport pins the validated address regardless of what DNS says at connect time", async () => {
  const { server, port } = await listen((_req, res) => res.writeHead(204).end());
  try {
    const pin = httpTransport({
      timeoutMs: 1000,
      validate: async (url) => ({ url: new URL(url), address: "127.0.0.1", family: 4 }),
    });
    assert.equal(await pin(`http://does-not-resolve.invalid:${port}/`, {}, "{}"), 204);
  } finally {
    server.close();
  }
});

test("a redirect is not followed and is a failed delivery that retries", async () => {
  let targetHits = 0;
  const target = await listen((_req, res) => {
    targetHits += 1;
    res.writeHead(200).end();
  });
  const redirector = await listen((_req, res) => res.writeHead(302, { location: `http://127.0.0.1:${target.port}/internal` }).end());
  try {
    const transport = httpTransport({
      timeoutMs: 1000,
      validate: async (url) => ({ url: new URL(url), address: "127.0.0.1", family: 4 }),
    });
    const status = await transport(`http://127.0.0.1:${redirector.port}/hook`, {}, "{}");
    assert.equal(status, 302);
    assert.equal(targetHits, 0);
    assert.equal(nextStep(0, status, 1000).kind, "retry");
  } finally {
    target.server.close();
    redirector.server.close();
  }
});

test("httpTransport enforces a total deadline against a slow-drip body", async () => {
  const { server, port } = await listen((_req, res) => {
    res.writeHead(200);
    const timer = setInterval(() => res.write("x"), 20);
    res.on("close", () => clearInterval(timer));
  });
  try {
    const transport = httpTransport({
      timeoutMs: 300,
      validate: async (url) => ({ url: new URL(url), address: "127.0.0.1", family: 4 }),
    });
    const started = Date.now();
    const status = await transport(`http://127.0.0.1:${port}/`, {}, "{}");
    assert.equal(status, 200);
    assert.ok(Date.now() - started < 1500);
  } finally {
    server.closeAllConnections();
    server.close();
  }
});

test("httpTransport resolves null within the deadline when the server trickles headers forever", async () => {
  const { server, port } = await listen((req) => {
    req.socket.write("HTTP/1.1 200 OK\r\n");
    const timer = setInterval(() => req.socket.write("x-a: b\r\n"), 20);
    req.socket.on("close", () => clearInterval(timer));
  });
  try {
    const transport = httpTransport({
      timeoutMs: 300,
      validate: async (url) => ({ url: new URL(url), address: "127.0.0.1", family: 4 }),
    });
    const started = Date.now();
    assert.equal(await transport(`http://127.0.0.1:${port}/`, {}, "{}"), null);
    assert.ok(Date.now() - started < 1500);
  } finally {
    server.closeAllConnections();
    server.close();
  }
});

test("httpTransport counts a slow DNS resolver inside the total deadline", async () => {
  const transport = httpTransport({
    timeoutMs: 200,
    allowHttp: true,
    resolve: () => new Promise<string[]>((resolve) => setTimeout(() => resolve(["93.184.216.34"]), 5000).unref()),
  });
  const started = Date.now();
  assert.equal(await transport("http://slow.example/", {}, "{}"), null);
  assert.ok(Date.now() - started < 1500);
});
