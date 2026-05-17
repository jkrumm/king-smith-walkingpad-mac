import {
  Icon,
  MenuBarExtra,
  launchCommand,
  LaunchType,
  showHUD,
  showToast,
  Toast,
} from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { useMemo } from "react";
import { api, clampSpeed, SPEED_GRID, speedStep } from "./lib/api";
import {
  formatDistance,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
} from "./lib/format";
import { isRunning, stateDisplay } from "./lib/state";
import type { Status } from "./lib/types";

export default function MenuBar() {
  const { data, isLoading, revalidate } = useCachedPromise(
    () => api.status(),
    [],
    {
      keepPreviousData: true,
    },
  );
  const step = useMemo(() => speedStep(), []);

  const known = data !== undefined;
  const connected = !!data?.connected;
  const running = known && isRunning(data?.belt_state);
  const stopped = known && connected && !running;

  const fire = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      await showHUD(`✓ ${label}`);
      revalidate();
    } catch (e) {
      await showToast({
        style: Toast.Style.Failure,
        title: label,
        message: (e as Error).message,
      });
    }
  };

  const onPresetSpeed = (v: number) => {
    if (running) {
      return fire(`Speed → ${v.toFixed(1)} km/h`, () => api.setSpeed(v));
    }
    if (stopped) {
      return fire(`Start at ${v.toFixed(1)} km/h`, () => api.start(v));
    }
    // State unknown — just attempt to set the speed; the daemon will reject
    // if the belt isn't ready.
    return fire(`Speed → ${v.toFixed(1)} km/h`, () => api.setSpeed(v));
  };

  return (
    <MenuBarExtra
      isLoading={isLoading}
      title={renderTitle(data)}
      tooltip={renderTooltip(data)}
    >
      <MenuBarExtra.Section title={statusLine(data)}>
        {data?.current_session && (
          <MenuBarExtra.Item
            title={`Session ${formatDurationLong(data.current_session.duration_s)} · ${formatDistance(
              data.current_session.distance_m,
            )}`}
            subtitle={`${formatStepsShort(data.current_session.steps)} steps · ${formatKcal(
              data.current_session.kcal,
            )}`}
          />
        )}
        <MenuBarExtra.Item
          title={`Today ${formatDistance(data?.today.distance_m ?? 0)}`}
          subtitle={`${formatStepsShort(data?.today.steps ?? 0)} steps · ${formatDurationLong(
            data?.today.duration_s ?? 0,
          )} · ${formatKcal(data?.today.kcal ?? 0)}`}
        />
      </MenuBarExtra.Section>

      {/* Start/Stop is gated on KNOWN state so we never offer Start while the
          belt is actually running (or while we don't yet know). */}
      {running && (
        <MenuBarExtra.Section title="Belt">
          <MenuBarExtra.Item
            title="Stop"
            icon={Icon.Stop}
            onAction={() => fire("Stopped", () => api.stop())}
          />
          <MenuBarExtra.Item
            title={`Speed +${step.toFixed(1)} km/h`}
            icon={Icon.ArrowUp}
            onAction={() =>
              fire(`Speed +${step.toFixed(1)}`, () =>
                api.setSpeed(clampSpeed((data?.speed_kmh ?? 1.0) + step)),
              )
            }
          />
          <MenuBarExtra.Item
            title={`Speed −${step.toFixed(1)} km/h`}
            icon={Icon.ArrowDown}
            onAction={() =>
              fire(`Speed −${step.toFixed(1)}`, () =>
                api.setSpeed(clampSpeed((data?.speed_kmh ?? 1.0) - step)),
              )
            }
          />
        </MenuBarExtra.Section>
      )}

      {stopped && (
        <MenuBarExtra.Section title="Belt">
          <MenuBarExtra.Item
            title="Start"
            icon={Icon.Play}
            onAction={() => fire("Started", () => api.start())}
          />
        </MenuBarExtra.Section>
      )}

      <MenuBarExtra.Section
        title={running ? "Set Speed (km/h)" : "Start At (km/h)"}
      >
        {SPEED_GRID.map((v) => (
          <MenuBarExtra.Item
            key={v}
            title={v.toFixed(1)}
            icon={Icon.Gauge}
            onAction={() => onPresetSpeed(v)}
          />
        ))}
      </MenuBarExtra.Section>

      <MenuBarExtra.Section>
        <MenuBarExtra.Item
          title="Open Controller"
          icon={Icon.ComputerChip}
          onAction={() =>
            launchCommand({
              name: "controller",
              type: LaunchType.UserInitiated,
            })
          }
        />
        <MenuBarExtra.Item
          title="Today"
          icon={Icon.Calendar}
          onAction={() =>
            launchCommand({ name: "today", type: LaunchType.UserInitiated })
          }
        />
        <MenuBarExtra.Item
          title="History"
          icon={Icon.List}
          onAction={() =>
            launchCommand({ name: "history", type: LaunchType.UserInitiated })
          }
        />
        <MenuBarExtra.Item
          title="Sync to Argo"
          icon={Icon.Upload}
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
          onAction={revalidate}
        />
      </MenuBarExtra.Section>
    </MenuBarExtra>
  );
}

// Title is the bit that lives in the system menu bar — keep it short.
// User explicitly does NOT want a walking emoji; we use plain text only.
function renderTitle(data: Status | undefined): string {
  if (!data) return "WalkingPad";
  if (!data.connected) return "WalkingPad offline";
  if (data.belt_state === "ACTIVE") {
    const steps = data.current_session?.steps ?? 0;
    return `${formatSpeed(data.speed_kmh)} · ${formatStepsShort(steps)}`;
  }
  if (data.belt_state === "STARTING") return "Starting…";
  if (data.belt_state === "STOPPING") return "Stopping…";
  // Idle states (STOPPED / STANDBY / unknown): show today's progress, or just
  // the app name when nothing has happened yet today.
  const km = data.today.distance_m;
  const steps = data.today.steps;
  if (km > 0 || steps > 0) {
    return `Today ${formatDistance(km)} · ${formatStepsShort(steps)}`;
  }
  return "WalkingPad";
}

function statusLine(data: Status | undefined): string {
  if (!data) return "Loading…";
  if (!data.connected) return "Disconnected — pad not in range";
  const sd = stateDisplay(true, data.belt_state);
  if (data.belt_state === "ACTIVE")
    return `${sd.label} · ${formatSpeed(data.speed_kmh)}`;
  return sd.label;
}

function renderTooltip(data: Status | undefined): string {
  if (!data) return "WalkingPad";
  if (!data.connected) return "WalkingPad daemon: pad offline";
  const sd = stateDisplay(true, data.belt_state);
  const cs = data.current_session
    ? ` · session ${formatDurationLong(data.current_session.duration_s)} / ${formatDistance(
        data.current_session.distance_m,
      )}`
    : "";
  return `${sd.label}${cs} · today ${formatDistance(data.today.distance_m)} (${formatStepsShort(
    data.today.steps,
  )} steps)`;
}
