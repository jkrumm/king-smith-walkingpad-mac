// Formatting helpers — kept presentation-only.

export function formatDistance(meters: number): string {
  if (!Number.isFinite(meters) || meters <= 0) return "0 m";
  if (meters < 1000) return `${Math.round(meters)} m`;
  return `${(meters / 1000).toFixed(2)} km`;
}

export function formatSteps(steps: number): string {
  if (!Number.isFinite(steps) || steps <= 0) return "0 steps";
  return `${Math.round(steps).toLocaleString("en-US")} steps`;
}

export function formatStepsShort(steps: number): string {
  if (!Number.isFinite(steps) || steps <= 0) return "0";
  return Math.round(steps).toLocaleString("en-US");
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0:00";
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0)
    return `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
  return `${m}:${String(sec).padStart(2, "0")}`;
}

export function formatDurationLong(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0 min";
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m} min`;
}

export function formatSpeed(kmh: number | undefined): string {
  if (!kmh || !Number.isFinite(kmh)) return "—";
  return `${kmh.toFixed(1)} km/h`;
}

export function formatKcal(kcal: number): string {
  if (!Number.isFinite(kcal) || kcal <= 0) return "0 kcal";
  return `${Math.round(kcal)} kcal`;
}

const dateFmt = new Intl.DateTimeFormat("en-US", {
  weekday: "short",
  month: "short",
  day: "numeric",
});

const timeFmt = new Intl.DateTimeFormat("en-US", {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

export function formatDate(iso: string): string {
  return dateFmt.format(new Date(iso));
}

export function formatTime(iso: string): string {
  return timeFmt.format(new Date(iso));
}

// Bucket label used to section the history list.
export function dateBucket(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const startOf = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const today = startOf(now);
  const day = 86_400_000;
  const diff = (today - startOf(d)) / day;
  if (diff <= 0) return "Today";
  if (diff === 1) return "Yesterday";
  if (diff < 7) return "This Week";
  if (diff < 30) return "This Month";
  return "Older";
}
