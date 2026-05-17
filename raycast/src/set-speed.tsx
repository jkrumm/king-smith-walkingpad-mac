import {
  Action,
  ActionPanel,
  Color,
  Icon,
  List,
  Toast,
  closeMainWindow,
  showHUD,
  showToast,
} from "@raycast/api";
import { useState } from "react";
import { api, clampSpeed, SPEED_GRID, speedStep } from "./lib/api";
import { useStatus } from "./lib/hooks";
import { isRunning, stateDisplay } from "./lib/state";
import {
  formatDistance,
  formatDuration,
  formatSpeed,
  formatStepsShort,
} from "./lib/format";

export default function SetSpeed() {
  const { data, revalidate } = useStatus(2000);
  const [search, setSearch] = useState("");

  const parsed = Number(search.replace(",", "."));
  const free =
    Number.isFinite(parsed) && parsed >= 0.5 && parsed <= 6.0
      ? clampSpeed(parsed)
      : null;

  const running = isRunning(data?.belt_state);
  const connected = !!data?.connected;
  const currentSpeed = data?.speed_kmh ?? null;
  const step = speedStep();

  const sd = stateDisplay(connected, data?.belt_state);
  const header = !data
    ? "Loading…"
    : !data.connected
      ? "Disconnected — pad not in range"
      : data.belt_state === "ACTIVE" && currentSpeed
        ? `${sd.label} · ${formatSpeed(currentSpeed)}${
            data.current_session
              ? ` · ${formatDuration(data.current_session.duration_s)} · ${formatDistance(data.current_session.distance_m)} · ${formatStepsShort(data.current_session.steps)} steps`
              : ""
          }`
        : sd.label;

  // Hide the preset that exactly matches the typed custom value so we don't
  // show it twice.
  const presets = SPEED_GRID.filter((v) => v !== free);

  const burst = () => {
    revalidate();
    setTimeout(revalidate, 400);
    setTimeout(revalidate, 1200);
    setTimeout(revalidate, 2500);
  };

  const runAction = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      burst();
      await closeMainWindow();
      await showHUD(`✓ ${label}`);
    } catch (e) {
      await showToast({
        style: Toast.Style.Failure,
        title: label,
        message: (e as Error).message,
      });
    }
  };

  return (
    <List
      navigationTitle="Set Speed"
      searchBarPlaceholder="Type a value (e.g. 3.5) or pick a preset"
      searchText={search}
      onSearchTextChange={setSearch}
    >
      {/* Belt control — always visible whenever the daemon is reachable. */}
      {(connected || !data) && (
        <List.Section title={header}>
          <List.Item
            title="Stop"
            icon={{ source: Icon.Stop, tintColor: Color.Red }}
            accessories={[{ tag: { value: "⌘.", color: Color.SecondaryText } }]}
            actions={
              <ActionPanel>
                <Action
                  title="Stop the Belt"
                  icon={Icon.Stop}
                  onAction={() => runAction("Stopped", () => api.stop())}
                />
              </ActionPanel>
            }
          />
          {currentSpeed !== null && (
            <List.Item
              title={`Speed up  →  ${clampSpeed(currentSpeed + step).toFixed(1)} km/h`}
              icon={{ source: Icon.ArrowUp, tintColor: Color.Green }}
              accessories={[
                { tag: { value: `+${step.toFixed(1)}`, color: Color.Green } },
                { tag: { value: "⌘=", color: Color.SecondaryText } },
              ]}
              actions={
                <ActionPanel>
                  <Action
                    title={`Set Speed → ${clampSpeed(currentSpeed + step).toFixed(1)} km/h`}
                    icon={Icon.ArrowUp}
                    onAction={() =>
                      runAction(
                        `Speed → ${clampSpeed(currentSpeed + step).toFixed(1)} km/h`,
                        () => api.setSpeed(clampSpeed(currentSpeed + step)),
                      )
                    }
                  />
                </ActionPanel>
              }
            />
          )}
          {currentSpeed !== null && (
            <List.Item
              title={`Speed down  →  ${clampSpeed(currentSpeed - step).toFixed(1)} km/h`}
              icon={{ source: Icon.ArrowDown, tintColor: Color.Orange }}
              accessories={[
                { tag: { value: `−${step.toFixed(1)}`, color: Color.Orange } },
                { tag: { value: "⌘-", color: Color.SecondaryText } },
              ]}
              actions={
                <ActionPanel>
                  <Action
                    title={`Set Speed → ${clampSpeed(currentSpeed - step).toFixed(1)} km/h`}
                    icon={Icon.ArrowDown}
                    onAction={() =>
                      runAction(
                        `Speed → ${clampSpeed(currentSpeed - step).toFixed(1)} km/h`,
                        () => api.setSpeed(clampSpeed(currentSpeed - step)),
                      )
                    }
                  />
                </ActionPanel>
              }
            />
          )}
        </List.Section>
      )}

      {free !== null && (
        <List.Section title="Custom">
          <PresetItem
            value={free}
            currentSpeed={currentSpeed}
            running={running}
            runAction={runAction}
            customLabel
          />
        </List.Section>
      )}

      <List.Section title={running ? "Set speed" : "Start at"}>
        {presets.map((v) => (
          <PresetItem
            key={v}
            value={v}
            currentSpeed={currentSpeed}
            running={running}
            runAction={runAction}
          />
        ))}
      </List.Section>
    </List>
  );
}

function PresetItem({
  value,
  currentSpeed,
  running,
  runAction,
  customLabel,
}: {
  value: number;
  currentSpeed: number | null;
  running: boolean;
  runAction: (label: string, fn: () => Promise<unknown>) => Promise<void>;
  customLabel?: boolean;
}) {
  const isCurrent =
    running && currentSpeed !== null && Math.abs(currentSpeed - value) < 0.05;
  const delta =
    currentSpeed !== null && currentSpeed > 0
      ? Math.round((value - currentSpeed) * 10) / 10
      : null;

  const accessories: List.Item.Accessory[] = [];
  if (isCurrent) {
    accessories.push({
      tag: { value: "current", color: Color.Green },
      icon: { source: Icon.CircleFilled, tintColor: Color.Green },
    });
  } else if (delta !== null && delta !== 0) {
    accessories.push({
      tag: {
        value: `${delta > 0 ? "+" : ""}${delta.toFixed(1)}`,
        color: delta > 0 ? Color.Blue : Color.Orange,
      },
    });
  }

  const title = customLabel
    ? `Custom — ${value.toFixed(1)} km/h`
    : `${value.toFixed(1)} km/h`;

  return (
    <List.Item
      title={title}
      icon={{
        source: isCurrent ? Icon.CircleFilled : Icon.Gauge,
        tintColor: isCurrent
          ? Color.Green
          : delta !== null && delta > 0
            ? Color.Blue
            : Color.SecondaryText,
      }}
      accessories={accessories}
      actions={
        <SpeedActions value={value} running={running} runAction={runAction} />
      }
    />
  );
}

function SpeedActions({
  value,
  running,
  runAction,
}: {
  value: number;
  running: boolean;
  runAction: (label: string, fn: () => Promise<unknown>) => Promise<void>;
}) {
  // Cached `running` picks the primary label so the user sees what *will*
  // happen; the action re-fetches live status before deciding the actual
  // call, in case the cache is stale.
  const primaryTitle = running
    ? `Set Speed → ${value.toFixed(1)} km/h`
    : `Start at ${value.toFixed(1)} km/h`;
  return (
    <ActionPanel>
      <Action
        title={primaryTitle}
        icon={running ? Icon.Gauge : Icon.Play}
        onAction={async () => {
          let isLive = running;
          try {
            const fresh = await api.status();
            isLive = isRunning(fresh.belt_state);
          } catch {
            // network blip — fall back to the prop.
          }
          const liveTitle = isLive
            ? `Speed → ${value.toFixed(1)} km/h`
            : `Start at ${value.toFixed(1)} km/h`;
          await runAction(liveTitle, () =>
            isLive ? api.setSpeed(value) : api.start(value),
          );
        }}
      />
      <Action
        title="Stop the Belt"
        icon={{ source: Icon.Stop, tintColor: Color.Red }}
        shortcut={{ modifiers: ["cmd"], key: "." }}
        onAction={() => runAction("Stopped", () => api.stop())}
      />
      <Action
        title={`Save as Default Start Speed (${value.toFixed(1)} km/h)`}
        icon={Icon.BullsEye}
        shortcut={{ modifiers: ["cmd"], key: "d" }}
        onAction={async () => {
          try {
            await api.setStartSpeed(value);
            await showHUD(`✓ Default → ${value.toFixed(1)} km/h`);
          } catch (e) {
            await showToast({
              style: Toast.Style.Failure,
              title: "Save default failed",
              message: (e as Error).message,
            });
          }
        }}
      />
    </ActionPanel>
  );
}
