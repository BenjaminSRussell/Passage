import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The app imports the shared pure domain (GS1 parser, offline queue) from the web module,
// so the dev server and build must be allowed to read the repository's apps/web tree.
export default defineConfig({
  plugins: [react()],
  server: { fs: { allow: ["../.."] } },
  build: { outDir: "dist", sourcemap: true },
});
