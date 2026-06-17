import { palette, type Palette } from "./theme";

// SVG charts rendered straight into Raycast's markdown via data-URI <img>.
// Everything here is theme-aware and self-contained — no external assets.

// --- Helpers ---------------------------------------------------------------

function svgUri(svg: string): string {
  // Base64 avoids percent-encoding traps with `#` colour literals.
  const b64 = Buffer.from(svg, "utf8").toString("base64");
  return `data:image/svg+xml;base64,${b64}`;
}

function svg(
  width: number,
  height: number,
  body: string,
  bgFill?: string,
): string {
  const bg = bgFill
    ? `<rect width="${width}" height="${height}" fill="${bgFill}" rx="12"/>`
    : "";
  // `shape-rendering="geometricPrecision"` makes the smooth curves look right
  // even at the small sizes Raycast scales us down to.
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" shape-rendering="geometricPrecision">${bg}${body}</svg>`;
}

// Markdown image — Raycast scales to the available width automatically.
export function asImage(svgStr: string, alt = "chart"): string {
  return `![${alt}](${svgUri(svgStr)})`;
}

// --- Vertical bar chart ----------------------------------------------------

export interface BarDatum {
  label: string;
  value: number;
  highlight?: boolean;
  // Sub-label rendered just under the main one (e.g. step count).
  secondary?: string;
}

export interface BarChartOpts {
  data: BarDatum[];
  unit?: string;
  title?: string;
  // Optional goal line (same units as values).
  goal?: number;
}

export function barChart(opts: BarChartOpts): string {
  const p = palette();
  const w = 560;
  const h = 240;
  const padTop = opts.title ? 36 : 24;
  const padBottom = 56;
  const padLeft = 16;
  const padRight = 16;
  const innerW = w - padLeft - padRight;
  const innerH = h - padTop - padBottom;
  const data = opts.data;
  const goal = opts.goal ?? 0;
  const maxVal = Math.max(0.1, goal, ...data.map((d) => d.value));
  const barSlot = innerW / Math.max(1, data.length);
  const barGap = Math.max(6, barSlot * 0.18);
  const barW = barSlot - barGap;

  const title = opts.title
    ? `<text x="${padLeft}" y="22" font-family="-apple-system, system-ui, sans-serif" font-size="13" font-weight="600" fill="${p.muted}" letter-spacing="1">${escapeXml(opts.title.toUpperCase())}</text>`
    : "";

  // Goal line if any.
  const goalLine =
    goal > 0
      ? (() => {
          const y = padTop + innerH - (goal / maxVal) * innerH;
          return `<line x1="${padLeft}" y1="${y.toFixed(2)}" x2="${w - padRight}" y2="${y.toFixed(2)}" stroke="${p.muted}" stroke-width="1" stroke-dasharray="3,4"/>
            <text x="${w - padRight}" y="${(y - 4).toFixed(2)}" text-anchor="end" font-family="-apple-system, system-ui, sans-serif" font-size="11" fill="${p.muted}">goal ${goal}${opts.unit ?? ""}</text>`;
        })()
      : "";

  const bars = data
    .map((d, i) => {
      const x = padLeft + i * barSlot + barGap / 2;
      const barH = Math.max(2, (d.value / maxVal) * innerH);
      const y = padTop + innerH - barH;
      const fill = d.value <= 0 ? p.grid : d.highlight ? p.active : p.distance;
      const valueText =
        d.value > 0
          ? `<text x="${(x + barW / 2).toFixed(2)}" y="${(y - 8).toFixed(2)}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="11" font-weight="600" fill="${p.fg}">${formatBarValue(d.value, opts.unit)}</text>`
          : "";
      const secondary = d.secondary
        ? `<text x="${(x + barW / 2).toFixed(2)}" y="${(padTop + innerH + 36).toFixed(2)}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="10" fill="${p.muted}">${escapeXml(d.secondary)}</text>`
        : "";
      return `
        <rect x="${x.toFixed(2)}" y="${y.toFixed(2)}" width="${barW.toFixed(2)}" height="${barH.toFixed(2)}" rx="5" fill="${fill}"/>
        ${valueText}
        <text x="${(x + barW / 2).toFixed(2)}" y="${(padTop + innerH + 18).toFixed(2)}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="12" font-weight="${d.highlight ? "600" : "500"}" fill="${d.highlight ? p.fg : p.muted}">${escapeXml(d.label)}</text>
        ${secondary}`;
    })
    .join("");

  return svg(w, h, title + goalLine + bars, p.panel);
}

// --- Speed-profile line chart ----------------------------------------------

export interface LineChartOpts {
  values: number[];
  maxY?: number;
  // Optional secondary horizontal line (e.g. avg).
  marker?: { value: number; label: string };
  // Tail point gets emphasised — used during a live session to show "current".
  showTail?: boolean;
  title?: string;
}

export function lineChart(opts: LineChartOpts): string {
  const p = palette();
  const w = 560;
  const h = 180;
  const padTop = opts.title ? 32 : 16;
  const padBottom = 28;
  const padLeft = 38;
  const padRight = 14;
  const innerW = w - padLeft - padRight;
  const innerH = h - padTop - padBottom;
  const values = opts.values.filter((v) => Number.isFinite(v));
  const n = values.length;
  if (n < 2) {
    return svg(
      w,
      h,
      `<text x="${w / 2}" y="${h / 2}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="13" fill="${p.muted}">collecting samples…</text>`,
      p.panel,
    );
  }

  const maxY = Math.max(opts.maxY ?? 6, ...values, 0.5);
  const xs = (i: number) => padLeft + (i / (n - 1)) * innerW;
  const ys = (v: number) => padTop + innerH - (v / maxY) * innerH;

  // Y-axis labels (0, mid, max).
  const yTicks = [0, maxY / 2, maxY]
    .map((v) => {
      const y = ys(v);
      return `<line x1="${padLeft}" y1="${y.toFixed(2)}" x2="${(w - padRight).toFixed(2)}" y2="${y.toFixed(2)}" stroke="${p.grid}" stroke-width="1" stroke-dasharray="2,3"/>
              <text x="${padLeft - 6}" y="${(y + 4).toFixed(2)}" text-anchor="end" font-family="-apple-system, system-ui, sans-serif" font-size="10" fill="${p.muted}">${v % 1 === 0 ? v : v.toFixed(1)}</text>`;
    })
    .join("");

  const linePath = values
    .map(
      (v, i) =>
        `${i === 0 ? "M" : "L"} ${xs(i).toFixed(2)} ${ys(v).toFixed(2)}`,
    )
    .join(" ");
  const areaPath =
    linePath +
    ` L ${xs(n - 1).toFixed(2)} ${(padTop + innerH).toFixed(2)} L ${xs(0).toFixed(2)} ${(padTop + innerH).toFixed(2)} Z`;

  const lineColor = p.speed;
  const area = `<path d="${areaPath}" fill="${p.activeSoft}"/>`;
  const line = `<path d="${linePath}" fill="none" stroke="${lineColor}" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>`;

  const tail = opts.showTail
    ? (() => {
        const tx = xs(n - 1);
        const ty = ys(values[n - 1]);
        return `<circle cx="${tx.toFixed(2)}" cy="${ty.toFixed(2)}" r="6" fill="${p.bg}" stroke="${lineColor}" stroke-width="2.5"/>
                <circle cx="${tx.toFixed(2)}" cy="${ty.toFixed(2)}" r="3" fill="${lineColor}"/>`;
      })()
    : "";

  const marker = opts.marker
    ? (() => {
        const y = ys(opts.marker!.value);
        return `<line x1="${padLeft}" y1="${y.toFixed(2)}" x2="${(w - padRight).toFixed(2)}" y2="${y.toFixed(2)}" stroke="${p.muted}" stroke-width="1" stroke-dasharray="4,3"/>
                <text x="${(w - padRight - 4).toFixed(2)}" y="${(y - 4).toFixed(2)}" text-anchor="end" font-family="-apple-system, system-ui, sans-serif" font-size="10" fill="${p.muted}">${escapeXml(opts.marker!.label)} ${opts.marker!.value.toFixed(1)}</text>`;
      })()
    : "";

  const title = opts.title
    ? `<text x="${padLeft}" y="20" font-family="-apple-system, system-ui, sans-serif" font-size="13" font-weight="600" fill="${p.muted}" letter-spacing="1">${escapeXml(opts.title.toUpperCase())}</text>`
    : "";

  return svg(w, h, title + yTicks + area + line + marker + tail, p.panel);
}

// --- Activity rings card ---------------------------------------------------

// A single SVG that pairs Apple-Watch-style concentric goal rings with a dense
// readout column. Combining everything into ONE image is deliberate: Raycast's
// markdown renderer lays each `![]()` out as its own block, so two separate
// ring images stack vertically. One self-contained card gives us full layout
// control (rings + stats side by side) regardless of how the renderer flows
// blocks, and reads as a proper dashboard widget rather than loose circles.

// Maps a metric to its stable palette accent so callers stay theme-agnostic —
// they pass a semantic tone, charts.ts owns the colour resolution.
export type MetricTone = "steps" | "distance" | "time" | "energy" | "speed";

function toneColor(p: Palette, tone: MetricTone): string {
  switch (tone) {
    case "steps":
      return p.steps;
    case "distance":
      return p.distance;
    case "time":
      return p.active;
    case "energy":
      return p.kcal;
    case "speed":
      return p.speed;
  }
}

export interface ActivityRing {
  label: string;
  value: number;
  goal: number;
  tone: MetricTone;
  // Pre-formatted for the readout column (e.g. "5,432" and "8,000").
  valueText: string;
  goalText: string;
}

export interface ActivityStat {
  label: string;
  valueText: string;
  tone: MetricTone;
}

export interface ActivityCardOpts {
  title?: string;
  // Outer → inner, max 3 rendered as rings.
  rings: ActivityRing[];
  // Extra readouts shown under the ringed rows (no goal arc).
  stats?: ActivityStat[];
}

const FONT = "-apple-system, system-ui, sans-serif";

export function activityCard(opts: ActivityCardOpts): string {
  const p = palette();
  const w = 560;
  const h = 236;
  const cx = 126;
  const cy = 134;
  const strokeW = 18;
  const gap = 7;
  const baseR = 84;

  const title = opts.title
    ? `<text x="20" y="26" font-family="${FONT}" font-size="13" font-weight="600" fill="${p.muted}" letter-spacing="1">${escapeXml(opts.title.toUpperCase())}</text>`
    : "";

  const rings = opts.rings
    .slice(0, 3)
    .map((ring, i) => {
      const r = baseR - i * (strokeW + gap);
      const circ = 2 * Math.PI * r;
      const ratio = Math.max(
        0,
        Math.min(1, ring.value / Math.max(0.0001, ring.goal)),
      );
      const dash = circ * ratio;
      const color = toneColor(p, ring.tone);
      // Track is the ring colour at low opacity (Apple-style) so an empty ring
      // still reads as "this metric exists, just not started".
      const track = `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${color}" stroke-opacity="0.18" stroke-width="${strokeW}"/>`;
      const arc =
        ratio > 0
          ? `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${color}" stroke-width="${strokeW}" stroke-linecap="round" stroke-dasharray="${dash.toFixed(2)} ${(circ - dash).toFixed(2)}" transform="rotate(-90 ${cx} ${cy})"/>`
          : "";
      return track + arc;
    })
    .join("");

  const rows = [
    ...opts.rings.slice(0, 3).map((r) => ({
      label: r.label,
      value: r.valueText,
      sub: `/ ${r.goalText}`,
      tone: r.tone,
    })),
    ...(opts.stats ?? []).map((s) => ({
      label: s.label,
      value: s.valueText,
      sub: "",
      tone: s.tone,
    })),
  ];

  const colX = 250;
  const dotX = colX + 6;
  const textX = colX + 22;
  const top = 58;
  const rowH = (h - top - 16) / Math.max(1, rows.length);

  const rowsSvg = rows
    .map((row, i) => {
      const rowCy = top + rowH * i + rowH / 2;
      const color = toneColor(p, row.tone);
      const dot = `<circle cx="${dotX}" cy="${(rowCy - 12).toFixed(1)}" r="4.5" fill="${color}"/>`;
      const label = `<text x="${textX}" y="${(rowCy - 8).toFixed(1)}" font-family="${FONT}" font-size="12" font-weight="600" fill="${p.muted}" letter-spacing="0.5">${escapeXml(row.label.toUpperCase())}</text>`;
      const sub = row.sub
        ? `<tspan font-size="13" font-weight="500" fill="${p.muted}"> ${escapeXml(row.sub)}</tspan>`
        : "";
      const value = `<text x="${textX}" y="${(rowCy + 16).toFixed(1)}" font-family="${FONT}" font-size="22" font-weight="700" fill="${p.fg}">${escapeXml(row.value)}${sub}</text>`;
      return dot + label + value;
    })
    .join("");

  return svg(w, h, title + rings + rowsSvg, p.panel);
}

// --- Util ------------------------------------------------------------------

function formatBarValue(v: number, unit?: string): string {
  if (unit === "km") return v >= 10 ? v.toFixed(1) : v.toFixed(2);
  if (unit === "k") return v >= 10 ? v.toFixed(1) + "k" : v.toFixed(1) + "k";
  return v.toFixed(1);
}

function escapeXml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
