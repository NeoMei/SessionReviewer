import eslint from "@eslint/js";
import { defineConfig } from "eslint/config";
import obsidianmd from "eslint-plugin-obsidianmd";

const pluginSources = ["src/**/*.ts", "tests/**/*.ts"];

export default defineConfig([
  { ignores: ["main.js", "node_modules/**"] },
  eslint.configs.recommended,
  {
    files: [...pluginSources, "*.ts", "*.mts"],
    languageOptions: {
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname }
    },
  },
  ...obsidianmd.configs.recommended,
  {
    files: pluginSources,
    rules: {
      "@typescript-eslint/consistent-type-imports": "error",
      "@typescript-eslint/no-unused-vars": ["error", { "argsIgnorePattern": "^_" }]
    }
  },
  {
    files: ["tests/**/*.ts"],
    rules: {
      "obsidianmd/no-global-this": "off",
      "obsidianmd/no-forbidden-elements": "off",
      "obsidianmd/prefer-create-el": "off",
      "obsidianmd/prefer-window-timers": "off",
      "@typescript-eslint/no-deprecated": "off"
    }
  }
]);
