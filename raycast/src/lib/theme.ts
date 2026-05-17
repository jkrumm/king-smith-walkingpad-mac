import { environment } from "@raycast/api";

// Theme-aware palette. Read at render time — Raycast re-mounts views when the
// appearance flips, so this gives reactive theme updates without a context.

export interface Palette {
  bg: string;
  panel: string;
  fg: string;
  muted: string;
  grid: string;
  // Semantic accents — used identically in both themes so charts stay readable.
  active: string;
  activeSoft: string;
  warn: string;
  danger: string;
  // Per-metric accents.
  distance: string;
  steps: string;
  kcal: string;
  speed: string;
}

const DARK: Palette = {
  bg: "#1c1c1e",
  panel: "#2c2c2e",
  fg: "#f5f5f7",
  muted: "#9a9aa1",
  grid: "#3a3a3c",
  active: "#34c759",
  activeSoft: "rgba(52, 199, 89, 0.18)",
  warn: "#ffd60a",
  danger: "#ff453a",
  distance: "#0a84ff",
  steps: "#bf5af2",
  kcal: "#ff9f0a",
  speed: "#34c759",
};

const LIGHT: Palette = {
  bg: "#ffffff",
  panel: "#f2f2f7",
  fg: "#1c1c1e",
  muted: "#6c6c70",
  grid: "#d1d1d6",
  active: "#34c759",
  activeSoft: "rgba(52, 199, 89, 0.22)",
  warn: "#ff9500",
  danger: "#ff3b30",
  distance: "#007aff",
  steps: "#af52de",
  kcal: "#ff9500",
  speed: "#34c759",
};

export function palette(): Palette {
  return environment.appearance === "light" ? LIGHT : DARK;
}
