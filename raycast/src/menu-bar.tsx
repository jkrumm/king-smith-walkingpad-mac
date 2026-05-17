import {
  Color,
  Icon,
  MenuBarExtra,
  LaunchType,
  launchCommand,
  showHUD,
  showToast,
  Toast,
} from "@raycast/api";
import { useMemo } from "react";
import {
  api,
  clampSpeed,
  defaultStartSpeed,
  SPEED_GRID,
  speedStep,
} from "./lib/api";
import {
  formatDistance,
  formatDuration,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
  formatTime,
} from "./lib/format";
import { isRunning, stateDisplay } from "./lib/state";
import { useSessions, useStatus } from "./lib/hooks";
import type { Status } from "./lib/types";

export default function MenuBar() {
  // 1 s poll while the menu is open — fast enough to catch the belt's
  // start ramp (it cycles through STARTING/STOPPED frames over ~3 s). The
  // 30 s `interval:` in package.json drives the background title refresh.
  const status = useStatus(1000);
  const sessions = useSessions(5);
  const step = useMemo(() => speedStep(), []);

  const data = status.data;
  const known = data !== undefined;
  const connected = !!data?.connected;

  const fire = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      await showHUD(`✓ ${label}`);
      // Belt state transitions can take 1–3 s (3-2-1 ramp, decel). Fire a
      // short burst of revalidations so the UI catches the transition fast
      // rather than waiting for the next 1 s polling tick.
      status.revalidate();
      setTimeout(() => status.revalidate(), 400);
      setTimeout(() => status.revalidate(), 1200);
      setTimeout(() => status.revalidate(), 2500);
    } catch (e) {
      await showToast({
        style: Toast.Style.Failure,
        title: label,
        message: (e as Error).message,
      });
    }
  };

  // Re-fetch live status *before* deciding the action so a stale cache
  // doesn't make us call /start on an already-running belt. The daemon is
  // also defensive about this (see /start handler) but doing both means the
  // toast wording matches what actually happened.
  const onPresetSpeed = async (v: number) => {
    let live = data;
    try {
      live = await api.status();
    } catch {
      // network blip — fall back to the cached snapshot.
    }
    const live_running = isRunning(live?.belt_state);
    if (live_running) {
      return fire(`Speed → ${v.toFixed(1)} km/h`, () => api.setSpeed(v));
    }
    return fire(`Start at ${v.toFixed(1)} km/h`, () => api.start(v));
  };

  const sd = stateDisplay(connected, data?.belt_state);

  return (
    <MenuBarExtra
      isLoading={status.isLoading && !data}
      title={renderTitle(data, status.isLoading)}
      tooltip={renderTooltip(data)}
      icon={menuBarIcon(data, status.isLoading)}
    >
      {/* Header: state + freshness */}
      <MenuBarExtra.Section title={renderHeader(data)}>
        {data?.current_session && (
          <MenuBarExtra.Item
            title={`Session ${formatDuration(data.current_session.duration_s)}`}
            subtitle={`${formatDistance(data.current_session.distance_m)} · ${formatStepsShort(data.current_session.steps)} steps · ${formatKcal(data.current_session.kcal)}`}
            icon={{ source: Icon.CircleProgress, tintColor: Color.Green }}
          />
        )}
        <MenuBarExtra.Item
          title={`Today  ${formatDistance(data?.today.distance_m ?? 0)}`}
          subtitle={`${formatStepsShort(data?.today.steps ?? 0)} steps · ${formatDurationLong(data?.today.duration_s ?? 0)} · ${formatKcal(data?.today.kcal ?? 0)}`}
          icon={{ source: Icon.Calendar, tintColor: sd.color }}
        />
      </MenuBarExtra.Section>

      {/* Belt control — always show Stop + Start when daemon is connected
          (or when we don't yet know its state). The daemon is idempotent in
          both directions, so worst case the click is a no-op — vastly better
          than hiding Stop just because a stale cache said the belt was off. */}
      {(connected || !known) && (
        <MenuBarExtra.Section title="Belt">
          <MenuBarExtra.Item
            title={`Start at ${defaultStartSpeed().toFixed(1)} km/h`}
            icon={{ source: Icon.Play, tintColor: Color.Green }}
            shortcut={{ modifiers: ["cmd"], key: "return" }}
            onAction={() =>
              fire(`Started at ${defaultStartSpeed().toFixed(1)} km/h`, () =>
                api.start(defaultStartSpeed()),
              )
            }
          />
          <MenuBarExtra.Item
            title="Stop"
            icon={{ source: Icon.Stop, tintColor: Color.Red }}
            shortcut={{ modifiers: ["cmd"], key: "." }}
            onAction={() => fire("Stopped", () => api.stop())}
          />
          <MenuBarExtra.Item
            title={`Speed +${step.toFixed(1)} km/h`}
            icon={Icon.ArrowUp}
            shortcut={{ modifiers: ["cmd"], key: "=" }}
            onAction={() =>
              fire(`Speed +${step.toFixed(1)}`, () =>
                api.setSpeed(clampSpeed((data?.speed_kmh ?? 1.0) + step)),
              )
            }
          />
          <MenuBarExtra.Item
            title={`Speed −${step.toFixed(1)} km/h`}
            icon={Icon.ArrowDown}
            shortcut={{ modifiers: ["cmd"], key: "-" }}
            onAction={() =>
              fire(`Speed −${step.toFixed(1)}`, () =>
                api.setSpeed(clampSpeed((data?.speed_kmh ?? 1.0) - step)),
              )
            }
          />
        </MenuBarExtra.Section>
      )}

      {(connected || !known) && (
        <MenuBarExtra.Section title="Speed (km/h)">
          {SPEED_GRID.map((v) => (
            <MenuBarExtra.Item
              key={v}
              title={`${v.toFixed(1)} km/h`}
              icon={{ source: Icon.Gauge, tintColor: speedTint(v) }}
              onAction={() => onPresetSpeed(v)}
            />
          ))}
        </MenuBarExtra.Section>
      )}

      {sessions.data && sessions.data.sessions.length > 0 && (
        <MenuBarExtra.Submenu title="Recent sessions" icon={Icon.Clock}>
          {sessions.data.sessions.slice(0, 5).map((s) => (
            <MenuBarExtra.Item
              key={s.uuid}
              title={`${formatTime(s.started_at)} · ${formatDistance(s.distance_m)}`}
              subtitle={`${formatStepsShort(s.steps)} steps · ${formatDurationLong(s.duration_s)} · avg ${formatSpeed(s.avg_speed_kmh)}`}
              icon={Icon.Footprints}
              onAction={() =>
                launchCommand({
                  name: "history",
                  type: LaunchType.UserInitiated,
                })
              }
            />
          ))}
        </MenuBarExtra.Submenu>
      )}

      <MenuBarExtra.Section>
        <MenuBarExtra.Item
          title="Open Controller"
          icon={Icon.ComputerChip}
          shortcut={{ modifiers: ["cmd"], key: "o" }}
          onAction={() =>
            launchCommand({
              name: "controller",
              type: LaunchType.UserInitiated,
            })
          }
        />
        <MenuBarExtra.Item
          title="History"
          icon={Icon.List}
          shortcut={{ modifiers: ["cmd"], key: "h" }}
          onAction={() =>
            launchCommand({ name: "history", type: LaunchType.UserInitiated })
          }
        />
        <MenuBarExtra.Item
          title="Sync to Argo"
          icon={Icon.Upload}
          shortcut={{ modifiers: ["cmd"], key: "y" }}
          onAction={() =>
            fire("Sync to Argo", async () => {
              const r = await api.sync();
              await showHUD(`✓ Synced ${r.synced} (failed ${r.failed})`);
            })
          }
        />
        <MenuBarExtra.Item
          title="Refresh"
          icon={Icon.ArrowClockwise}
          shortcut={{ modifiers: ["cmd"], key: "r" }}
          onAction={() => status.revalidate()}
        />
      </MenuBarExtra.Section>
    </MenuBarExtra>
  );
}

// Title is the bit that lives in the system menu bar — keep it short.
// User explicitly does NOT want a walking emoji; we use plain text + a
// neutral SF Symbol icon to the left.
function renderTitle(data: Status | undefined, isLoading: boolean): string {
  // While the first fetch is in flight we don't yet know if the daemon is up,
  // so don't claim "offline" — that flashed red on every menu open.
  if (!data) return isLoading ? "WalkingPad" : "offline";
  if (!data.connected) return "offline";
  if (data.belt_state === "ACTIVE") {
    const steps = data.current_session?.steps ?? 0;
    return `${formatSpeed(data.speed_kmh)}  ·  ${formatStepsShort(steps)}`;
  }
  if (data.belt_state === "STARTING") return "starting…";
  if (data.belt_state === "STOPPING") return "stopping…";
  const km = data.today.distance_m;
  const steps = data.today.steps;
  if (km > 0 || steps > 0) {
    return `${formatDistance(km)}  ·  ${formatStepsShort(steps)}`;
  }
  return "WalkingPad";
}

// SF Symbol that flips based on belt state. Raycast tints these — no emoji
// involvement, so it doesn't violate the "no walking emoji" rule.
function menuBarIcon(
  data: Status | undefined,
  isLoading: boolean,
): Icon | { source: Icon; tintColor: Color } {
  // No data and still fetching: neutral pulse, NOT red. Red is reserved for
  // the case where we've actually confirmed the daemon is unreachable.
  if (!data)
    return {
      source: Icon.Circle,
      tintColor: isLoading ? Color.SecondaryText : Color.Red,
    };
  if (!data.connected)
    return { source: Icon.ExclamationMark, tintColor: Color.Red };
  if (data.belt_state === "ACTIVE")
    return { source: Icon.CircleFilled, tintColor: Color.Green };
  if (data.belt_state === "STARTING" || data.belt_state === "STOPPING")
    return { source: Icon.Hourglass, tintColor: Color.Yellow };
  return { source: Icon.Circle, tintColor: Color.SecondaryText };
}

function renderHeader(data: Status | undefined): string {
  if (!data) return "Loading…";
  if (!data.connected) return "Disconnected — pad not in range";
  const sd = stateDisplay(true, data.belt_state);
  if (data.belt_state === "ACTIVE")
    return `${sd.label}  ·  ${formatSpeed(data.speed_kmh)}`;
  if (data.observed_at) {
    return `${sd.label}  ·  updated ${formatTime(data.observed_at)}`;
  }
  return sd.label;
}

function renderTooltip(data: Status | undefined): string {
  if (!data) return "WalkingPad";
  if (!data.connected) return "WalkingPad daemon: pad offline";
  const sd = stateDisplay(true, data.belt_state);
  const cs = data.current_session
    ? ` · session ${formatDuration(cs_duration(data))} / ${formatDistance(data.current_session.distance_m)}`
    : "";
  return `${sd.label}${cs} · today ${formatDistance(data.today.distance_m)} (${formatStepsShort(data.today.steps)} steps)`;
}

function cs_duration(data: Status): number {
  return data.current_session?.duration_s ?? 0;
}

// Pace-aware tint for the preset speed grid.
function speedTint(v: number): Color {
  if (v <= 2.0) return Color.Green;
  if (v <= 3.5) return Color.Blue;
  if (v <= 5.0) return Color.Orange;
  return Color.Red;
}
