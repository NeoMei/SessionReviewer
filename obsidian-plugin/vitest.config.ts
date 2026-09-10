import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Real Go wire and TypeScript package builds share the runner CPU/memory.
    // Keep test files sequential so their compiler children cannot overlap.
    fileParallelism: false,
    environment: "jsdom",
    clearMocks: true,
    setupFiles: ["./tests/setup.ts"]
  },
  resolve: {
    alias: {
      obsidian: fileURLToPath(new URL("./tests/mocks/obsidian.ts", import.meta.url))
    }
  }
});
