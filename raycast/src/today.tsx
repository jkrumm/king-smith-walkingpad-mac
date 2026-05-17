import {
  Action,
  ActionPanel,
  Icon,
  List,
  launchCommand,
  LaunchType,
} from "@raycast/api";
import { useMemo, useState } from "react";
import {
  formatDistance,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
  formatTime,
} from "./lib/format";
import { useSessionDetail, useSessions, useSummary } from "./lib/hooks";
import { resampleForSparkline, sparkline } from "./lib/sparkline";
import type { Session } from "./lib/types";

export default function Today() {
  const sessions = useSessions(100);
  const summary = useSummary("today");
  const [selected, setSelected] = useState<string | undefined>(undefined);

  const todaysSessions = useMemo(
    () => (sessions.data?.sessions ?? []).filter((s) => isToday(s)),
    [sessions.data],
  );

  const header = summary.data
    ? `Today · ${formatDistance(summary.data.distance_m)} · ${formatStepsShort(summary.data.steps)} steps · ${formatDurationLong(summary.data.duration_s)} · ${formatKcal(summary.data.kcal)}`
    : "Today";

  return (
    <List
      isLoading={sessions.isLoading || summary.isLoading}
      isShowingDetail
      navigationTitle="Today"
      searchBarPlaceholder="Filter sessions…"
      onSelectionChange={(id) => setSelected(id ?? undefined)}
    >
      {todaysSessions.length === 0 ? (
        <List.EmptyView
          icon={Icon.Footprints}
          title="No sessions yet today"
          description="Open the Controller and hit Start to begin your first walk."
          actions={
            <ActionPanel>
              <Action
                title="Open Controller"
                icon={Icon.ComputerChip}
                onAction={() =>
                  launchCommand({
                    name: "controller",
                    type: LaunchType.UserInitiated,
                  })
                }
              />
            </ActionPanel>
          }
        />
      ) : (
        <List.Section title={header}>
          {todaysSessions.map((s) => (
            <SessionItem
              key={s.uuid}
              session={s}
              isSelected={selected === s.uuid}
            />
          ))}
        </List.Section>
      )}
    </List>
  );
}

function SessionItem({
  session,
  isSelected,
}: {
  session: Session;
  isSelected: boolean;
}) {
  // Only load samples for the focused item to keep this snappy on long lists.
  const detail = useSessionDetail(isSelected ? session.uuid : undefined);
  return (
    <List.Item
      id={session.uuid}
      title={formatTime(session.started_at)}
      subtitle={formatDistance(session.distance_m)}
      icon={Icon.Footprints}
      accessories={[
        { tag: `${formatStepsShort(session.steps)} steps` },
        { tag: formatDurationLong(session.duration_s) },
      ]}
      detail={
        <List.Item.Detail
          markdown={renderDetail(session, detail.data?.samples)}
        />
      }
      actions={
        <ActionPanel>
          <Action
            title="Open History"
            icon={Icon.List}
            onAction={() =>
              launchCommand({ name: "history", type: LaunchType.UserInitiated })
            }
          />
          <Action
            title="Open Controller"
            icon={Icon.ComputerChip}
            onAction={() =>
              launchCommand({
                name: "controller",
                type: LaunchType.UserInitiated,
              })
            }
          />
          <Action.CopyToClipboard
            title="Copy Session UUID"
            content={session.uuid}
            shortcut={{ modifiers: ["cmd"], key: "c" }}
          />
        </ActionPanel>
      }
    />
  );
}

function renderDetail(
  session: Session,
  samples?: { speed_kmh: number }[],
): string {
  const lines: string[] = [];
  lines.push(`# ${formatTime(session.started_at)}`);
  if (session.ended_at) {
    lines.push(`*ended ${formatTime(session.ended_at)}*`);
  } else {
    lines.push("*in progress*");
  }
  lines.push("");
  lines.push(
    `**${formatDistance(session.distance_m)}** · **${formatStepsShort(session.steps)} steps** · ${formatDurationLong(session.duration_s)} · ${formatKcal(session.kcal)}`,
  );
  lines.push(
    `avg ${formatSpeed(session.avg_speed_kmh)} · peak ${formatSpeed(session.max_speed_kmh)}`,
  );

  if (samples && samples.length >= 2) {
    const series = resampleForSparkline(
      samples.map((s) => s.speed_kmh),
      60,
    );
    lines.push("");
    lines.push("**Speed profile**");
    lines.push("```");
    lines.push(sparkline(series));
    lines.push("```");
  }

  if (session.pause_count > 0) {
    lines.push("");
    lines.push(
      `*${session.pause_count} pause${session.pause_count > 1 ? "s" : ""}*`,
    );
  }

  if (session.synced_at) {
    lines.push("");
    lines.push(`> Synced to Argo at ${formatTime(session.synced_at)}`);
  }

  return lines.join("\n");
}

function isToday(s: Session, now = new Date()): boolean {
  const d = new Date(s.started_at);
  return (
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  );
}
