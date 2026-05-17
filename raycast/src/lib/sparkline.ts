// Block-character sparkline. Returns a single-line string suitable for
// embedding into Raycast markdown panels.

const BLOCKS = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"] as const;

export function sparkline(
  values: number[],
  opts?: { min?: number; max?: number },
): string {
  if (values.length === 0) return "";
  const finite = values.filter((v) => Number.isFinite(v));
  if (finite.length === 0) return "";
  const min = opts?.min ?? Math.min(...finite);
  const max = opts?.max ?? Math.max(...finite);
  const range = max - min;
  if (range <= 0) {
    // All identical — render the lowest block to indicate constant non-zero.
    return finite.map(() => BLOCKS[0]).join("");
  }
  return finite
    .map((v) => {
      const idx = Math.round(((v - min) / range) * (BLOCKS.length - 1));
      return BLOCKS[Math.max(0, Math.min(BLOCKS.length - 1, idx))];
    })
    .join("");
}

// Down-sample (or pad) to a fixed width using nearest-neighbor binning.
export function resampleForSparkline(values: number[], width = 60): number[] {
  if (values.length === 0 || width <= 0) return [];
  if (values.length === width) return values;
  if (values.length < width) {
    return values.concat(
      Array(width - values.length).fill(values[values.length - 1]),
    );
  }
  const out: number[] = [];
  const stride = values.length / width;
  for (let i = 0; i < width; i++) {
    out.push(values[Math.floor(i * stride)]);
  }
  return out;
}
