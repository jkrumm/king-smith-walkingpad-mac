import { getPreferenceValues } from "@raycast/api";
import type {
  OkResponse,
  Period,
  SessionDetail,
  SessionsList,
  Status,
  Summary,
  SyncResult,
} from "./types";

interface Prefs {
  baseUrl: string;
  apiToken: string;
  quickSpeeds: string;
  speedStep: string;
}

export function prefs(): Prefs {
  return getPreferenceValues<Prefs>();
}

export function baseUrl(): string {
  const raw = prefs().baseUrl?.trim() || "http://127.0.0.1:7706";
  return raw.replace(/\/$/, "");
}

export function authHeaders(): Record<string, string> {
  const t = prefs().apiToken?.trim();
  return t ? { Authorization: `Bearer ${t}` } : {};
}

// quickSpeeds prefs string → number[], clamped to daemon's accepted range.
export function quickSpeeds(): number[] {
  const raw = prefs().quickSpeeds ?? "2,3,4,5";
  return raw
    .split(",")
    .map((s) => Number(s.trim()))
    .filter((n) => Number.isFinite(n) && n >= 0.5 && n <= 6.0)
    .map((n) => Math.round(n * 10) / 10);
}

export function speedStep(): number {
  const n = Number(prefs().speedStep);
  return Number.isFinite(n) && n > 0 ? n : 0.5;
}

// Clamp + round any user-driven speed before POSTing so we never trigger a 400.
export function clampSpeed(value: number): number {
  const clamped = Math.min(6.0, Math.max(0.5, value));
  return Math.round(clamped * 10) / 10;
}

class HTTPError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "HTTPError";
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(baseUrl() + path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...authHeaders(),
      ...(init.headers ?? {}),
    },
  });
  if (!res.ok) {
    let detail = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) detail = body.error;
    } catch {
      // best-effort
    }
    throw new HTTPError(res.status, `${res.status} ${detail}`);
  }
  // Some POSTs return 200 with no body; tolerate it.
  const text = await res.text();
  return (text ? JSON.parse(text) : ({} as T)) as T;
}

export const api = {
  status: () => request<Status>("/status"),
  start: (speed_kmh?: number) =>
    request<OkResponse>("/start", {
      method: "POST",
      body: JSON.stringify(speed_kmh ? { speed_kmh } : {}),
    }),
  stop: () => request<OkResponse>("/stop", { method: "POST" }),
  setSpeed: (speed_kmh: number) =>
    request<OkResponse>("/speed", {
      method: "POST",
      body: JSON.stringify({ speed_kmh: clampSpeed(speed_kmh) }),
    }),
  setStartSpeed: (speed_kmh: number) =>
    request<OkResponse>("/pref/start-speed", {
      method: "POST",
      body: JSON.stringify({ speed_kmh: clampSpeed(speed_kmh) }),
    }),
  sessions: (limit = 50) => request<SessionsList>(`/sessions?limit=${limit}`),
  session: (uuid: string) => request<SessionDetail>(`/sessions/${uuid}`),
  summary: (period: Period) => request<Summary>(`/summary?period=${period}`),
  sync: () => request<SyncResult>("/sync/argo", { method: "POST" }),
};

export { HTTPError };
