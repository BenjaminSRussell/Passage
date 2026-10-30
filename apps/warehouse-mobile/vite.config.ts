import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const reactPackages = ["react", "react-dom"].map((name) => ({
  find: new RegExp(`^${name}(/.*)?$`),
  replacement: `${fileURLToPath(new URL(`./node_modules/${name}`, import.meta.url))}$1`,
}));

export default defineConfig({
  plugins: [react()],
  resolve: { alias: reactPackages },
  server: { fs: { allow: ["../.."] } },
  build: { outDir: "dist", sourcemap: true },
});
