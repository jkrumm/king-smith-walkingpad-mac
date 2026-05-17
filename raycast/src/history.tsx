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
  dateBucket,
  formatDate,
  formatDistance,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
  formatTime,
} from "./lib/format";
import { useSessionDetail, useSessions, useSummary } from "./lib/hooks";
import { resampleForSparkline, sparkline } from "./lib/sparkline";
import type { Period, Session } from "./lib/types";

const BUCKETS = [
  "Today",
  "Yesterday",
  "This Week",
  "This Month",
  "Older",
] as const;

export default function History() {
  const sessions = useSessions(50);
  const [period, setPeriod] = useState<Period>("week");
  const summary = useSummary(period);
  const [selected, setSelected] = useState<string | undefined>(undefined);

  const sectioned = useMemo(() => {
    const filtered = (sessions.data?.sessions ?? []).filter((s) =>
      withinPeriod(s, period),
    );
    return groupBy(filtered, (s) => dateBucket(s.started_at));
  }, [sessions.data, period]);

  const header = summary.data
    ? `${formatDistance(summary.data.distance_m)} · ${formatStepsShort(summary.data.steps)} steps · ${formatDurationLong(summary.data.duration_s)} · ${formatKcal(summary.data.kcal)} · ${summary.data.sessions} session${summary.data.sessions === 1 ? "" : "s"}`
    : "";

  return (
    <List
      isLoading={sessions.isLoading || summary.isLoading}
      isShowingDetail
      navigationTitle="History"
      searchBarPlaceholder="Filter sessions…"
      onSelectionChange={(id) => setSelected(id ?? undefined)}
      searchBarAccessory={
        <List.Dropdown
          tooltip="Window"
          value={period}
          onChange={(v) => setPeriod(v as Period)}
        >
          <List.Dropdown.Item title="Today" value="today" />
          <List.Dropdown.Item title="Last 7 days" value="week" />
          <List.Dropdown.Item title="Last 30 days" value="month" />
          <List.Dropdown.Item title="All time" value="all" />
        </List.Dropdown>
      }
    >
      {BUCKETS.flatMap((bucket) => {
        const rows = sectioned.get(bucket);
        if (!rows || rows.length === 0) return [];
        return [
          <List.Section
            key={bucket}
            title={bucket}
            subtitle={bucket === "Today" ? header : undefined}
          >
            {rows.map((s) => (
              <HistoryItem
                key={s.uuid}
                session={s}
                isSelected={selected === s.uuid}
              />
            ))}
          </List.Section>,
        ];
      })}
      {summary.data && summary.data.sessions === 0 && (
        <List.EmptyView
          icon={Icon.Footprints}
          title="No sessions in this window"
          description="Switch to a longer window or open the Controller to start one."
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
      )}
    </List>
  );
}

function HistoryItem({
  session,
  isSelected,
}: {
  session: Session;
  isSelected: boolean;
}) {
  const detail = useSessionDetail(isSelected ? session.uuid : undefined);
  return (
    <List.Item
      id={session.uuid}
      title={formatDate(session.started_at)}
      subtitle={formatTime(session.started_at)}
      icon={Icon.Footprints}
      keywords={[session.uuid]}
      accessories={[
        { tag: formatDistance(session.distance_m) },
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
            title="Open Controller"
            icon={Icon.ComputerChip}
            onAction={() =>
              launchCommand({
                name: "controller",
                type: LaunchType.UserInitiated,
              })
            }
          />
          <Action
            title="Open Today"
            icon={Icon.Calendar}
            onAction={() =>
              launchCommand({ name: "today", type: LaunchType.UserInitiated })
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
  lines.push(
    `# ${formatDate(session.started_at)} · ${formatTime(session.started_at)}`,
  );
  if (session.ended_at) {
    lines.push(
      `*${formatTime(session.started_at)} → ${formatTime(session.ended_at)}*`,
    );
  }
  lines.push("");
  lines.push(
    `**${formatDistance(session.distance_m)}** · **${formatStepsShort(session.steps)} steps** · ${formatDurationLong(session.duration_s)} · ${formatKcal(session.kcal)}`,
  );
  lines.push(
    `avg ${formatSpeed(session.avg_speed_kmh)} · peak ${formatSpeed(session.max_speed_kmh)}`,
  );
  if (session.pause_count > 0) {
    lines.push(
      `${session.pause_count} pause${session.pause_count > 1 ? "s" : ""}`,
    );
  }
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
  if (session.synced_at) {
    lines.push("");
    lines.push(`> Synced to Argo at ${formatTime(session.synced_at)}`);
  } else {
    lines.push("");
    lines.push("> Not yet synced");
  }
  return lines.join("\n");
}

function withinPeriod(s: Session, period: Period, now = new Date()): boolean {
  if (period === "all") return true;
  const d = new Date(s.started_at);
  const day = 86_400_000;
  const startOf = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const today = startOf(now);
  if (period === "today") return startOf(d) === today;
  if (period === "week") return today - startOf(d) < 7 * day;
  if (period === "month") return today - startOf(d) < 30 * day;
  return true;
}

function groupBy<T, K>(arr: T[], key: (t: T) => K): Map<K, T[]> {
  const out = new Map<K, T[]>();
  for (const item of arr) {
    const k = key(item);
    const existing = out.get(k);
    if (existing) existing.push(item);
    else out.set(k, [item]);
  }
  return out;
}
