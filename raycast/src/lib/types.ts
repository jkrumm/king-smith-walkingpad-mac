// Wire types — mirrors internal/api/types.go in the Go daemon.
// Keep field names snake_case to match the JSON exactly.

export type BeltState =
  | "STOPPED"
  | "STARTING"
  | "ACTIVE"
  | "STOPPING"
  | "STANDBY"
  | "UNKNOWN";

export type Period = "today" | "week" | "month" | "all";

export interface DeviceInfo {
  name: string;
  address: string;
  rssi: number;
}

export interface Sample {
  ts: string;
  belt_state?: string;
  speed_kmh: number;
  distance_m: number;
  steps: number;
}

export interface CurrentSession {
  uuid: string;
  started_at: string;
  duration_s: number;
  distance_m: number;
  steps: number;
  kcal: number;
  avg_speed_kmh: number;
  max_speed_kmh: number;
  samples: Sample[];
}

export interface DailySummary {
  duration_s: number;
  distance_m: number;
  steps: number;
  kcal: number;
}

export interface Status {
  connected: boolean;
  belt_state?: string;
  mode?: string;
  speed_kmh?: number;
  observed_at?: string;
  current_session: CurrentSession | null;
  today: DailySummary;
  device: DeviceInfo;
}

export interface Session {
  uuid: string;
  started_at: string;
  ended_at: string | null;
  duration_s: number;
  distance_m: number;
  steps: number;
  avg_speed_kmh: number;
  max_speed_kmh: number;
  kcal: number;
  pause_count: number;
  synced_at: string | null;
}

export interface SessionsList {
  sessions: Session[];
}

export interface SessionDetail {
  session: Session;
  samples: Sample[];
}

export interface Summary {
  period: Period;
  sessions: number;
  duration_s: number;
  distance_m: number;
  steps: number;
  kcal: number;
}

export interface SyncResult {
  synced: number;
  failed: number;
}

export interface OkResponse {
  ok: boolean;
}
