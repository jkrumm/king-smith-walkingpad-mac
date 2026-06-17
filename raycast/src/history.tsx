import {
  Action,
  ActionPanel,
  Color,
  Icon,
  List,
  launchCommand,
  LaunchType,
} from "@raycast/api";
import { useMemo, useState } from "react";
import { asImage, barChart, lineChart } from "./lib/charts";
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
import {
  useDailyBreakdown,
  useSessionDetail,
  useSessions,
  useSummary,
} from "./lib/hooks";
import type { Period, Sample, Session, Summary } from "./lib/types";

const BUCKETS = [
  "Today",
  "Yesterday",
  "This Week",
  "This Month",
  "Older",
] as const;

const DAYS_BY_PERIOD: Record<Period, number> = {
  today: 1,
  week: 7,
  month: 30,
  all: 30,
};

export default function History() {
  const sessions = useSessions(200);
  const [period, setPeriod] = useState<Period>("week");
  const summary = useSummary(period);
  const daily = useDailyBreakdown(DAYS_BY_PERIOD[period]);
  const [selected, setSelected] = useState<string | undefined>(undefined);

  const sectioned = useMemo(() => {
    const filtered = (sessions.data?.sessions ?? []).filter((s) =>
      withinPeriod(s, period),
    );
    return groupBy(filtered, (s) => dateBucket(s.started_at));
  }, [sessions.data, period]);

  const totalsLine = summary.data
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
      <List.Section title="Summary">
        <SummaryItem
          period={period}
          totals={totalsLine}
          summary={summary.data}
          buckets={daily.buckets}
          isSelected={selected === "__summary__"}
        />
      </List.Section>

      {BUCKETS.flatMap((bucket) => {
        const rows = sectioned.get(bucket);
        if (!rows || rows.length === 0) return [];
        return [
          <List.Section key={bucket} title={bucket}>
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

function SummaryItem({
  period,
  totals,
  summary,
  buckets,
}: {
  period: Period;
  totals: string;
  summary: Summary | undefined;
  buckets: ReturnType<typeof useDailyBreakdown>["buckets"];
  isSelected: boolean;
}) {
  const md = useMemo(() => {
    const lines: string[] = [];
    const periodLabel: Record<Period, string> = {
      today: "Today",
      week: "Last 7 days",
      month: "Last 30 days",
      all: "All time (last 30 days)",
    };
    lines.push(`# ${periodLabel[period]}`);
    if (totals) lines.push(`**${totals}**`);
    lines.push("");
    if (buckets.length > 1) {
      lines.push(
        asImage(
          barChart({
            data: buckets.map((b) => ({
              label: b.label,
              value: Math.round(b.distanceKm * 100) / 100,
              highlight: b.isToday,
              secondary: b.steps > 0 ? formatStepsShort(b.steps) : undefined,
            })),
            unit: "km",
            title: "distance · km",
          }),
          "distance-chart",
        ),
      );
      lines.push("");
      lines.push(
        asImage(
          barChart({
            data: buckets.map((b) => ({
              label: b.label,
              value: Math.round(b.steps / 100) / 10, // thousands of steps
              highlight: b.isToday,
              secondary: b.steps > 0 ? formatStepsShort(b.steps) : undefined,
            })),
            unit: "k",
            title: "steps · k",
          }),
          "steps-chart",
        ),
      );
    }
    return lines.join("\n");
  }, [period, totals, buckets]);

  return (
    <List.Item
      id="__summary__"
      title="Period summary"
      subtitle={totals}
      icon={Icon.BarChart}
      detail={
        <List.Item.Detail
          markdown={md}
          metadata={summary ? <SummaryMetadata summary={summary} /> : undefined}
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
        </ActionPanel>
      }
    />
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
          markdown={renderSessionChart(session, detail.data?.samples)}
          metadata={<SessionMetadata session={session} />}
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

// Markdown side of the detail pane — just the heading + speed-profile chart.
// All scalar stats moved into the structured metadata sidebar below.
function renderSessionChart(session: Session, samples?: Sample[]): string {
  const lines: string[] = [];
  lines.push(`# ${formatDate(session.started_at)}`);
  lines.push(
    session.ended_at
      ? `*${formatTime(session.started_at)} → ${formatTime(session.ended_at)}*`
      : `*${formatTime(session.started_at)}*`,
  );
  lines.push("");
  if (samples && samples.length >= 2) {
    lines.push(
      asImage(
        lineChart({
          values: samples.map((s) => s.speed_kmh),
          title: "speed profile",
          marker:
            session.avg_speed_kmh > 0
              ? { value: session.avg_speed_kmh, label: "avg" }
              : undefined,
        }),
        "speed-profile",
      ),
    );
  } else {
    lines.push("> Speed profile unavailable for this session.");
  }
  return lines.join("\n");
}

function SessionMetadata({ session }: { session: Session }) {
  const M = List.Item.Detail.Metadata;
  const synced = Boolean(session.synced_at);
  return (
    <M>
      <M.Label
        title="Distance"
        text={formatDistance(session.distance_m)}
        icon={{ source: Icon.Map, tintColor: Color.Blue }}
      />
      <M.Label
        title="Steps"
        text={formatStepsShort(session.steps)}
        icon={{ source: Icon.Footprints, tintColor: Color.Purple }}
      />
      <M.Label
        title="Duration"
        text={formatDurationLong(session.duration_s)}
        icon={{ source: Icon.Clock, tintColor: Color.Green }}
      />
      <M.Label
        title="Energy"
        text={formatKcal(session.kcal)}
        icon={{ source: Icon.Bolt, tintColor: Color.Orange }}
      />
      <M.Separator />
      <M.Label title="Avg pace" text={formatSpeed(session.avg_speed_kmh)} />
      <M.Label title="Peak pace" text={formatSpeed(session.max_speed_kmh)} />
      {session.pause_count > 0 && (
        <M.Label title="Pauses" text={String(session.pause_count)} />
      )}
      <M.Separator />
      <M.TagList title="Argo sync">
        <M.TagList.Item
          text={
            synced ? `Synced ${formatTime(session.synced_at!)}` : "Not synced"
          }
          color={synced ? Color.Green : Color.Orange}
        />
      </M.TagList>
    </M>
  );
}

function SummaryMetadata({ summary }: { summary: Summary }) {
  const M = List.Item.Detail.Metadata;
  return (
    <M>
      <M.Label
        title="Distance"
        text={formatDistance(summary.distance_m)}
        icon={{ source: Icon.Map, tintColor: Color.Blue }}
      />
      <M.Label
        title="Steps"
        text={formatStepsShort(summary.steps)}
        icon={{ source: Icon.Footprints, tintColor: Color.Purple }}
      />
      <M.Label
        title="Duration"
        text={formatDurationLong(summary.duration_s)}
        icon={{ source: Icon.Clock, tintColor: Color.Green }}
      />
      <M.Label
        title="Energy"
        text={formatKcal(summary.kcal)}
        icon={{ source: Icon.Bolt, tintColor: Color.Orange }}
      />
      <M.Separator />
      <M.Label
        title="Sessions"
        text={String(summary.sessions)}
        icon={{ source: Icon.Footprints, tintColor: Color.SecondaryText }}
      />
    </M>
  );
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
