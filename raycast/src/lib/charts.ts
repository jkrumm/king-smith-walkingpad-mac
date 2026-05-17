import { palette } from "./theme";

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

// --- Speedometer gauge -----------------------------------------------------

export interface GaugeOpts {
  value: number;
  max?: number;
  min?: number;
  unit?: string;
  // Optional sub-line under the big value (e.g. "active", "stopped").
  sublabel?: string;
}

export function gauge(opts: GaugeOpts): string {
  const p = palette();
  const min = opts.min ?? 0;
  const max = opts.max ?? 6;
  const value = Math.max(min, Math.min(max, opts.value));
  const unit = opts.unit ?? "km/h";

  const w = 540;
  const h = 280;
  const cx = w / 2;
  const cy = h - 60;
  const r = 190;
  const arcW = 26;

  const startA = Math.PI;
  const endA = 0;
  const t = (value - min) / (max - min);
  const valueA = startA - t * Math.PI;

  const pt = (a: number, rr: number) => ({
    x: cx + rr * Math.cos(a),
    y: cy - rr * Math.sin(a),
  });

  const arcPath = (a0: number, a1: number, rr: number): string => {
    const s = pt(a0, rr);
    const e = pt(a1, rr);
    const large = Math.abs(a0 - a1) > Math.PI ? 1 : 0;
    // Sweep flag: 0 = anticlockwise — we draw from left (π) to right (0).
    return `M ${s.x.toFixed(2)} ${s.y.toFixed(2)} A ${rr} ${rr} 0 ${large} 0 ${e.x.toFixed(2)} ${e.y.toFixed(2)}`;
  };

  // Colour shifts subtly with speed so you can tell intensity at a glance.
  const activeColor =
    value < 2
      ? p.speed
      : value < 4
        ? p.warn
        : value < 5.5
          ? "#ff9f0a"
          : p.danger;

  const bgArc = `<path d="${arcPath(startA, endA, r)}" stroke="${p.grid}" stroke-width="${arcW}" fill="none" stroke-linecap="round"/>`;
  const fgArc =
    t > 0
      ? `<path d="${arcPath(startA, valueA, r)}" stroke="${activeColor}" stroke-width="${arcW}" fill="none" stroke-linecap="round"/>`
      : "";

  // Tick marks at integer steps with labels.
  let ticks = "";
  for (let i = min; i <= max; i++) {
    const tt = (i - min) / (max - min);
    const ta = startA - tt * Math.PI;
    const inner = pt(ta, r - arcW / 2 - 6);
    const outer = pt(ta, r + arcW / 2 + 4);
    const lbl = pt(ta, r + arcW / 2 + 24);
    ticks += `<line x1="${inner.x.toFixed(2)}" y1="${inner.y.toFixed(2)}" x2="${outer.x.toFixed(2)}" y2="${outer.y.toFixed(2)}" stroke="${p.muted}" stroke-width="2"/>`;
    ticks += `<text x="${lbl.x.toFixed(2)}" y="${(lbl.y + 5).toFixed(2)}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="14" fill="${p.muted}">${i}</text>`;
  }

  // Needle.
  const needleEnd = pt(valueA, r - arcW / 2 - 14);
  const needle = `<line x1="${cx}" y1="${cy}" x2="${needleEnd.x.toFixed(2)}" y2="${needleEnd.y.toFixed(2)}" stroke="${p.fg}" stroke-width="3" stroke-linecap="round"/>`;
  const hub = `<circle cx="${cx}" cy="${cy}" r="8" fill="${p.fg}"/><circle cx="${cx}" cy="${cy}" r="3" fill="${p.bg}"/>`;

  // Big value text and unit.
  const display = value > 0 ? value.toFixed(1) : "0.0";
  const valueText = `<text x="${cx}" y="${cy - 30}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="72" font-weight="700" fill="${p.fg}">${display}</text>`;
  const unitText = `<text x="${cx}" y="${cy - 6}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="16" fill="${p.muted}">${unit}</text>`;
  const sub = opts.sublabel
    ? `<text x="${cx}" y="${cy + 28}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="13" font-weight="600" fill="${activeColor}" letter-spacing="2">${escapeXml(opts.sublabel.toUpperCase())}</text>`
    : "";

  return svg(
    w,
    h,
    bgArc + fgArc + ticks + valueText + unitText + sub + needle + hub,
    p.panel,
  );
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

// --- Progress ring ---------------------------------------------------------

export interface ProgressRingOpts {
  value: number;
  goal: number;
  // Big label inside (e.g. "1.4 km" or "1,234").
  primary: string;
  secondary?: string;
  // Override the fill color (defaults to the steps accent).
  color?: string;
}

export function progressRing(opts: ProgressRingOpts): string {
  const p = palette();
  const w = 200;
  const h = 200;
  const cx = w / 2;
  const cy = h / 2;
  const r = 78;
  const stroke = 14;
  const circumference = 2 * Math.PI * r;
  const ratio = Math.max(
    0,
    Math.min(1, opts.value / Math.max(0.0001, opts.goal)),
  );
  const dash = circumference * ratio;
  const color = opts.color ?? p.steps;

  return svg(
    w,
    h,
    `
      <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${p.grid}" stroke-width="${stroke}"/>
      <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${color}" stroke-width="${stroke}"
              stroke-linecap="round"
              stroke-dasharray="${dash.toFixed(2)} ${(circumference - dash).toFixed(2)}"
              transform="rotate(-90 ${cx} ${cy})"/>
      <text x="${cx}" y="${cy + 2}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="26" font-weight="700" fill="${p.fg}">${escapeXml(opts.primary)}</text>
      ${opts.secondary ? `<text x="${cx}" y="${cy + 24}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="12" fill="${p.muted}">${escapeXml(opts.secondary)}</text>` : ""}
    `,
    p.panel,
  );
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
