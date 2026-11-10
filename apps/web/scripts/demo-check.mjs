// Loads demo pages in headless Chromium and reports what each one needed from the fake backend.
//
//   npx vite build --mode demo && npx vite preview --mode demo --port 4301 --strictPort &
//   node scripts/demo-check.mjs --port 4301 --out /some/dir /sales/orders /production ...
//
// Per path it prints one JSON line: unhandled API calls (misses), console and page errors, alert texts, whether a
// loading skeleton is still showing, and the screenshot file. Paths are app paths, without the /Passage base.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "@playwright/test";

const args = process.argv.slice(2);
let port = 4301;
let out = "demo-check";
let base = "/Passage";
const paths = [];
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--port") port = Number(args[++i]);
  else if (args[i] === "--out") out = args[++i];
  else if (args[i] === "--base") base = args[++i];
  else paths.push(args[i]);
}
mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
let failed = 0;
for (const path of paths) {
  const page = await context.newPage();
  const errors = [];
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("favicon")) errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  await page.goto(`http://localhost:${port}${base}${path}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(800);
  const misses = await page.evaluate(() => window.__passageDemoMisses ?? null);
  const alerts = await page.locator('[role="alert"]').allInnerTexts();
  const loading = await page.locator('[aria-busy="true"]').count();
  const h1 = (await page.locator("h1").first().innerText().catch(() => "")).trim();
  const file = join(out, `${path.replace(/^\//, "").replace(/[^a-zA-Z0-9]+/g, "_") || "home"}.png`);
  await page.screenshot({ path: file });
  const ok = misses !== null && misses.length === 0 && errors.length === 0 && alerts.filter((a) => a.trim()).length === 0 && loading === 0;
  if (!ok) failed++;
  console.log(JSON.stringify({ path, ok, h1, misses, errors, alerts: alerts.filter((a) => a.trim()), loading, screenshot: file }));
  await page.close();
}
await browser.close();
process.exit(failed ? 1 : 0);
