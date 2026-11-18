import type { Config } from "tailwindcss";

const config: Config = {
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      // Semantic colour tokens from app/globals.css (#4529): bg-surface,
      // text-ink-2, border-line, bg-accent-soft... Use these, not gray-*/hex.
      colors: {
        bg: "var(--bg)",
        surface: { DEFAULT: "var(--surface)", 2: "var(--surface-2)" },
        raised: "var(--raised)",
        line: "var(--line)",
        ink: { DEFAULT: "var(--ink)", 2: "var(--ink-2)", 3: "var(--ink-3)" },
        accent: { DEFAULT: "var(--accent)", fill: "var(--accent-fill)", soft: "var(--accent-soft)" },
        badge: "var(--badge)",
        danger: "var(--danger)",
        warn: { bg: "var(--warn-bg)", ink: "var(--warn-ink)", line: "var(--warn-line)" },
        scrim: "var(--scrim)",
      },
      borderRadius: {
        "token-sm": "var(--r-sm)",
        "token-md": "var(--r-md)",
        "token-lg": "var(--r-lg)",
      },
      boxShadow: {
        "token-sm": "var(--shadow-sm)",
        "token-lg": "var(--shadow-lg)",
      },
      padding: {
        "safe-bottom": "env(safe-area-inset-bottom)",
      },
    },
  },
  plugins: [],
};

export default config;
