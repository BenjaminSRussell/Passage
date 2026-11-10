import { defineConfig, devices } from "@playwright/test";

// Scratch config for a local run against system Chrome; not committed.
const DEMO_PORT = 4791;

export default defineConfig({
  testDir: "./e2e",
  workers: 2,
  reporter: [["list"]],
  outputDir: "/tmp/claude-0/-home-user-Passage/64b295f8-c2c3-48f0-ae07-930c32290dcd/scratchpad/pw-506",
  use: { trace: "off", screenshot: "off", reducedMotion: "reduce" },
  projects: [
    {
      name: "demo-flows",
      use: { ...devices["Desktop Chrome"], baseURL: `http://localhost:${DEMO_PORT}/Passage/`, launchOptions: { executablePath: "/usr/bin/google-chrome" } },
      testMatch: /palette-records\.demo\.spec/,
    },
  ],
  webServer: [
    {
      command: `npx vite --mode demo --port ${DEMO_PORT} --strictPort`,
      url: `http://localhost:${DEMO_PORT}/Passage/`,
      reuseExistingServer: false,
      timeout: 120_000,
    },
  ],
});
