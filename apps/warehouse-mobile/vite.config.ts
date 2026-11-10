import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The shared web code imports these; resolve them from this app, which installs only its own node_modules.
const reactPackages = ["react", "react-dom", "zod"].map((name) => ({
  find: new RegExp(`^${name}(/.*)?$`),
  replacement: `${fileURLToPath(new URL(`./node_modules/${name}`, import.meta.url))}$1`,
}));

export default defineConfig({
  plugins: [react()],
  resolve: { alias: reactPackages },
  server: { fs: { allow: ["../.."] } },
  build: { outDir: "dist", sourcemap: true },
});
