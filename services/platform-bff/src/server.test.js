import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "./server.js";

test("healthz reports the bff", async () => {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  const res = await fetch(`http://127.0.0.1:${port}/healthz`);
  const body = await res.json();
  assert.equal(res.status, 200);
  assert.equal(body.status, "ok");
  assert.equal(body.service, "platform-bff");
  await new Promise((resolve) => server.close(resolve));
});

test("unknown paths are 404", async () => {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  const res = await fetch(`http://127.0.0.1:${port}/missing`);
  assert.equal(res.status, 404);
  await new Promise((resolve) => server.close(resolve));
});
