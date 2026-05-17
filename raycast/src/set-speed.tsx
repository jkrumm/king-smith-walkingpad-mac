import {
  Action,
  ActionPanel,
  Icon,
  List,
  Toast,
  closeMainWindow,
  showHUD,
  showToast,
} from "@raycast/api";
import { useState } from "react";
import { api, clampSpeed, SPEED_GRID } from "./lib/api";
import { useStatus } from "./lib/hooks";
import { isRunning, stateDisplay } from "./lib/state";
import { formatSpeed } from "./lib/format";

export default function SetSpeed() {
  const { data } = useStatus(2000);
  const [search, setSearch] = useState("");

  const parsed = Number(search.replace(",", "."));
  const free =
    Number.isFinite(parsed) && parsed >= 0.5 && parsed <= 6.0
      ? clampSpeed(parsed)
      : null;

  const running = isRunning(data?.belt_state);
  const sd = stateDisplay(data?.connected ?? false, data?.belt_state);
  const header = data
    ? data.connected
      ? `${sd.label}${data.belt_state === "ACTIVE" ? ` · ${formatSpeed(data.speed_kmh)}` : ""}`
      : "Disconnected"
    : "Loading…";

  return (
    <List
      navigationTitle="Set Speed"
      searchBarPlaceholder="Type a value (e.g. 3.5) or pick a preset"
      searchText={search}
      onSearchTextChange={setSearch}
    >
      <List.Section title={header}>
        {free !== null && (
          <List.Item
            title={`Set to ${free.toFixed(1)} km/h`}
            icon={Icon.Gauge}
            actions={<SpeedActions value={free} running={running ?? false} />}
          />
        )}
      </List.Section>

      <List.Section title="Speeds (km/h)">
        {SPEED_GRID.map((v) => (
          <List.Item
            key={`a-${v}`}
            title={v.toFixed(1)}
            icon={Icon.Gauge}
            actions={<SpeedActions value={v} running={running ?? false} />}
          />
        ))}
      </List.Section>
    </List>
  );
}

function SpeedActions({ value, running }: { value: number; running: boolean }) {
  // The cached `running` flag drives the label so the user can see what the
  // click *will* do. The actual action re-fetches live status, because if the
  // cache is stale (e.g. the user just started walking) firing /start on a
  // running belt would halt it. The daemon also short-circuits this, but
  // doing it here means the toast wording is honest.
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
            ? `Set Speed → ${value.toFixed(1)} km/h`
            : `Start at ${value.toFixed(1)} km/h`;
          try {
            if (isLive) await api.setSpeed(value);
            else await api.start(value);
            await closeMainWindow();
            await showHUD(`✓ ${liveTitle}`);
          } catch (e) {
            await showToast({
              style: Toast.Style.Failure,
              title: liveTitle,
              message: (e as Error).message,
            });
          }
        }}
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
