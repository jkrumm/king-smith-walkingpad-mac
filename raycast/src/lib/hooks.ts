import { useCachedPromise } from "@raycast/utils";
import { useEffect, useMemo } from "react";
import { api } from "./api";
import type { Period, Session } from "./types";

// --- Status -----------------------------------------------------------------

// Cached so we show last-known data instantly on open (no flash to red) and
// only swap to fresh values once the new fetch resolves. The interval keeps
// the readout live while the view stays mounted.
export function useStatus(refreshMs = 1000) {
  const result = useCachedPromise(() => api.status(), [], {
    keepPreviousData: true,
  });
  useEffect(() => {
    const id = setInterval(() => result.revalidate(), refreshMs);
    return () => clearInterval(id);
  }, [refreshMs, result.revalidate]);
  return result;
}

// --- Sessions ---------------------------------------------------------------

export function useSessions(limit = 50) {
  return useCachedPromise((l: number) => api.sessions(l), [limit], {
    keepPreviousData: true,
  });
}

export function useSummary(period: Period) {
  return useCachedPromise((p: Period) => api.summary(p), [period], {
    keepPreviousData: true,
  });
}

export function useSessionDetail(uuid: string | undefined) {
  return useCachedPromise(
    async (id: string | undefined) => (id ? await api.session(id) : null),
    [uuid],
    { keepPreviousData: true },
  );
}

// --- Daily aggregation ------------------------------------------------------

export interface DayBucket {
  date: Date;
  label: string;
  distanceKm: number;
  steps: number;
  durationS: number;
  kcal: number;
  sessions: number;
  isToday: boolean;
}

// Derive a windowed per-day breakdown from /sessions. The daemon doesn't
// expose this directly (PRD §8) but the chart needs it, so we bucket
// client-side. Cheap: ~200 sessions max.
export function useDailyBreakdown(days = 7) {
  const sessions = useSessions(200);
  const buckets = useMemo(() => {
    const list = sessions.data?.sessions ?? [];
    return bucketByDay(list, days);
  }, [sessions.data, days]);
  return { ...sessions, buckets };
}

function bucketByDay(sessions: Session[], days: number): DayBucket[] {
  const now = new Date();
  const out: DayBucket[] = [];
  const labelFmt = new Intl.DateTimeFormat("en-US", { weekday: "short" });
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i);
    out.push({
      date: d,
      label: i === 0 ? "Today" : labelFmt.format(d),
      distanceKm: 0,
      steps: 0,
      durationS: 0,
      kcal: 0,
      sessions: 0,
      isToday: i === 0,
    });
  }
  const index = new Map<string, DayBucket>();
  for (const b of out) index.set(dayKey(b.date), b);

  for (const s of sessions) {
    const d = new Date(s.started_at);
    const key = dayKey(d);
    const b = index.get(key);
    if (!b) continue;
    b.distanceKm += s.distance_m / 1000;
    b.steps += s.steps;
    b.durationS += s.duration_s;
    b.kcal += s.kcal;
    b.sessions += 1;
  }
  return out;
}

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}
