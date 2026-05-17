import {
  Action,
  ActionPanel,
  Detail,
  Form,
  Icon,
  Keyboard,
  Toast,
  launchCommand,
  LaunchType,
  showToast,
  useNavigation,
} from "@raycast/api";
import { useCallback, useMemo } from "react";
import {
  api,
  clampSpeed,
  defaultStartSpeed,
  SPEED_GRID,
  speedStep,
} from "./lib/api";
import {
  asImage,
  barChart,
  gauge,
  lineChart,
  progressRing,
} from "./lib/charts";
import {
  formatDistance,
  formatDuration,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
  formatTime,
} from "./lib/format";
import { useDailyBreakdown, useSessions, useStatus } from "./lib/hooks";
import { isRunning, stateDisplay } from "./lib/state";
import type { CurrentSession, Sample, Session, Status } from "./lib/types";

const REFRESH_MS = 1000;
const STEP_GOAL = 8000;
const DISTANCE_GOAL_KM = 5;

export default function Controller() {
  const status = useStatus(REFRESH_MS);
  const daily = useDailyBreakdown(7);
  const recent = useSessions(8);
  const step = useMemo(() => speedStep(), []);

  const data = status.data;
  const error = status.error;
  const running = isRunning(data?.belt_state);

  const runAction = useCallback(
    async (label: string, fn: () => Promise<unknown>) => {
      const toast = await showToast({
        style: Toast.Style.Animated,
        title: label,
      });
      try {
        await fn();
        toast.style = Toast.Style.Success;
        toast.title = `${label} ✓`;
        // Belt state transitions are slow (3-2-1 start ramp, decel frame on
        // stop). Burst-revalidate so the UI flips fast instead of waiting
        // for the next 1 s polling tick.
        status.revalidate();
        setTimeout(() => status.revalidate(), 400);
        setTimeout(() => status.revalidate(), 1200);
        setTimeout(() => status.revalidate(), 2500);
      } catch (e) {
        toast.style = Toast.Style.Failure;
        toast.title = label;
        toast.message = (e as Error).message;
      }
    },
    [status],
  );

  const onStart = (speed?: number) => {
    // Fall back to the user's Default Start Speed preference when no
    // explicit value was picked (e.g. the bare Start action / ⌘↩).
    const v = speed ?? defaultStartSpeed();
    return runAction(`Start at ${v.toFixed(1)} km/h`, () => api.start(v));
  };
  const onStop = () => runAction("Stop", () => api.stop());
  const onSetSpeed = (v: number) =>
    runAction(`Set Speed → ${v.toFixed(1)} km/h`, () => api.setSpeed(v));
  const onBumpSpeed = (delta: number) => {
    const next = clampSpeed((data?.speed_kmh ?? 1.0) + delta);
    return onSetSpeed(next);
  };
  const onPresetSpeed = (v: number) => (running ? onSetSpeed(v) : onStart(v));
  const onSync = () =>
    runAction("Sync to Argo", async () => {
      const r = await api.sync();
      return r;
    });

  // Memo the markdown so the 1 Hz refresh tick doesn't regenerate the big SVGs
  // unless the underlying numbers actually moved. We pass `markdownKey` through
  // the deps array to make the cache key explicit instead of relying on object
  // identity from useStatus.
  const markdownKey = useMemo(
    () => buildMarkdownKey(data, daily.buckets, recent.data?.sessions),
    [data, daily.buckets, recent.data?.sessions],
  );
  // Deliberately keyed only on markdownKey — including the raw deps would
  // bust the memo every 1 s revalidation tick and defeat the optimisation.
  const markdown = useMemo(
    () =>
      renderMarkdown(data, error, daily.buckets, recent.data?.sessions ?? []),
    [markdownKey],
  );

  return (
    <Detail
      isLoading={status.isLoading && !data}
      markdown={markdown}
      metadata={renderMetadata(data)}
      navigationTitle="WalkingPad"
      actions={
        <ActionPanel>
          {/* Always show both Start and Stop. The cached `running` flag picks
              which is primary (=top of the panel, default ⏎), but the other
              stays clickable in case the cache is stale — the daemon is
              idempotent so the worst case is a no-op. */}
          <ActionPanel.Section title={running ? "Active" : "Stopped"}>
            {running ? (
              <>
                <Action
                  title="Stop"
                  icon={Icon.Stop}
                  shortcut={{ modifiers: ["cmd"], key: "." }}
                  onAction={onStop}
                />
                <Action
                  title={`Speed +${step.toFixed(1)} km/h`}
                  icon={Icon.ArrowUp}
                  shortcut={{ modifiers: ["cmd"], key: "=" }}
                  onAction={() => onBumpSpeed(step)}
                />
                <Action
                  title={`Speed −${step.toFixed(1)} km/h`}
                  icon={Icon.ArrowDown}
                  shortcut={{ modifiers: ["cmd"], key: "-" }}
                  onAction={() => onBumpSpeed(-step)}
                />
                <Action
                  title="Start"
                  icon={Icon.Play}
                  shortcut={{ modifiers: ["cmd"], key: "return" }}
                  onAction={() => onStart()}
                />
              </>
            ) : (
              <>
                <Action
                  title="Start"
                  icon={Icon.Play}
                  shortcut={{ modifiers: ["cmd"], key: "return" }}
                  onAction={() => onStart()}
                />
                <Action
                  title="Stop"
                  icon={Icon.Stop}
                  shortcut={{ modifiers: ["cmd"], key: "." }}
                  onAction={onStop}
                />
              </>
            )}
            <SetSpeedAction onSubmit={onSetSpeed} />
            <SetStartSpeedAction />
          </ActionPanel.Section>

          <ActionPanel.Section title="Speed (km/h)">
            {SPEED_GRID.map((v, i) => (
              <Action
                key={v}
                title={`${v.toFixed(1)} km/h`}
                icon={Icon.Gauge}
                shortcut={quickShortcut(i)}
                onAction={() => onPresetSpeed(v)}
              />
            ))}
          </ActionPanel.Section>

          <ActionPanel.Section title="More">
            <Action
              title="Refresh"
              icon={Icon.ArrowClockwise}
              shortcut={{ modifiers: ["cmd"], key: "r" }}
              onAction={status.revalidate}
            />
            <Action
              title="Sync to Argo"
              icon={Icon.Upload}
              shortcut={{ modifiers: ["cmd"], key: "y" }}
              onAction={onSync}
            />
            <Action
              title="Open History"
              icon={Icon.List}
              shortcut={{ modifiers: ["cmd"], key: "h" }}
              onAction={() =>
                launchCommand({
                  name: "history",
                  type: LaunchType.UserInitiated,
                })
              }
            />
          </ActionPanel.Section>
        </ActionPanel>
      }
    />
  );
}

function quickShortcut(idx: number): Keyboard.Shortcut | undefined {
  // ⌘1..⌘9 for the first nine presets — anything beyond stays mouse-only.
  if (idx < 0 || idx > 8) return undefined;
  return { modifiers: ["cmd"], key: String(idx + 1) as Keyboard.KeyEquivalent };
}

// --- Markdown rendering ----------------------------------------------------

function buildMarkdownKey(
  data: Status | undefined,
  buckets: { distanceKm: number; steps: number }[],
  recent: Session[] | undefined,
): string {
  if (!data) return "none";
  const cs = data.current_session;
  const csKey = cs
    ? `${cs.uuid}:${cs.duration_s}:${Math.round(cs.distance_m)}:${cs.steps}:${Math.round(data.speed_kmh ?? 0 * 10) / 10}`
    : "no";
  const bk = buckets
    .map((b) => `${b.distanceKm.toFixed(2)}/${b.steps}`)
    .join(",");
  const rk = (recent ?? [])
    .slice(0, 5)
    .map((s) => s.uuid)
    .join(",");
  return `${data.connected}|${data.belt_state ?? "?"}|${(data.speed_kmh ?? 0).toFixed(1)}|${Math.round(data.today.distance_m)}|${data.today.steps}|${csKey}|${bk}|${rk}`;
}

function renderMarkdown(
  data: Status | undefined,
  error: Error | undefined,
  buckets: ReturnType<typeof useDailyBreakdown>["buckets"],
  recent: Session[],
): string {
  if (error) {
    return [
      "# Cannot reach daemon",
      "",
      "```",
      error.message,
      "```",
      "",
      "Check that the daemon is running and the URL/token in extension preferences match.",
    ].join("\n");
  }
  if (!data) return "# WalkingPad\n\nLoading…";

  if (!data.connected) return renderDisconnected(recent);
  if (isRunning(data.belt_state)) return renderActive(data, buckets, recent);
  return renderStopped(data, buckets, recent);
}

function renderActive(
  data: Status,
  buckets: ReturnType<typeof useDailyBreakdown>["buckets"],
  recent: Session[],
): string {
  const cs = data.current_session;
  const speed = data.speed_kmh ?? 0;

  const lines: string[] = [];
  lines.push(asImage(gauge({ value: speed, sublabel: "active" }), "speed"));
  lines.push("");

  if (cs) {
    lines.push(`## Session · ${formatDuration(cs.duration_s)}`);
    lines.push(
      `**${formatDistance(cs.distance_m)}** · **${formatStepsShort(cs.steps)} steps** · ${formatKcal(cs.kcal)}`,
    );
    lines.push(
      `avg ${formatSpeed(cs.avg_speed_kmh)} · peak ${formatSpeed(cs.max_speed_kmh)}`,
    );
    lines.push("");

    const speeds = sampleSpeeds(cs);
    if (speeds.length >= 2) {
      lines.push(
        asImage(
          lineChart({
            values: speeds,
            title: "speed profile",
            marker:
              cs.avg_speed_kmh > 0
                ? { value: cs.avg_speed_kmh, label: "avg" }
                : undefined,
            showTail: true,
          }),
          "session-speed",
        ),
      );
      lines.push("");
    } else {
      lines.push(
        "> Speed profile will appear after the session collects a few samples.",
      );
      lines.push("");
    }
  }

  // Same historicals as the stopped view — having Today's totals and the
  // 7-day chart visible during a walk lets you see the in-progress session
  // bump up the running totals in real time.
  appendHistoricals(lines, data, buckets, recent);

  return lines.join("\n");
}

function renderStopped(
  data: Status,
  buckets: ReturnType<typeof useDailyBreakdown>["buckets"],
  recent: Session[],
): string {
  const lines: string[] = [];
  appendHistoricals(lines, data, buckets, recent);
  return lines.join("\n");
}

function appendHistoricals(
  lines: string[],
  data: Status,
  buckets: ReturnType<typeof useDailyBreakdown>["buckets"],
  recent: Session[],
): void {
  const today = data.today;
  const todayKm = today.distance_m / 1000;

  lines.push("## Today");
  lines.push("");
  lines.push(
    asImage(
      progressRing({
        value: today.steps,
        goal: STEP_GOAL,
        primary: formatStepsShort(today.steps),
        secondary: `of ${formatStepsShort(STEP_GOAL)} steps`,
      }),
      "steps-progress",
    ),
  );
  lines.push(
    asImage(
      progressRing({
        value: todayKm,
        goal: DISTANCE_GOAL_KM,
        primary: todayKm.toFixed(2),
        secondary: `of ${DISTANCE_GOAL_KM} km`,
        color: "#0a84ff",
      }),
      "km-progress",
    ),
  );
  lines.push("");
  lines.push(
    `**${formatKcal(today.kcal)}** · ${formatDurationLong(today.duration_s)}`,
  );
  lines.push("");

  lines.push(
    asImage(
      barChart({
        data: buckets.map((b) => ({
          label: b.label,
          value: Math.round(b.distanceKm * 100) / 100,
          highlight: b.isToday,
          secondary: b.steps > 0 ? `${formatStepsShort(b.steps)}` : undefined,
        })),
        unit: "km",
        title: "last 7 days · km",
        goal: DISTANCE_GOAL_KM,
      }),
      "weekly",
    ),
  );
  lines.push("");

  if (recent.length > 0) {
    lines.push("### Recent sessions");
    const rows = recent
      .slice(0, 5)
      .map(
        (s) =>
          `- **${formatTime(s.started_at)}** · ${formatDistance(s.distance_m)} · ${formatStepsShort(s.steps)} steps · ${formatDurationLong(s.duration_s)} · avg ${formatSpeed(s.avg_speed_kmh)}`,
      );
    lines.push(...rows);
  } else {
    lines.push("> No sessions logged yet. Hit ⌘↩ to start your first walk.");
  }
}

function renderDisconnected(recent: Session[]): string {
  const lines: string[] = [
    "# Disconnected",
    "",
    "> Waiting for the WalkingPad to come into range. The daemon will reconnect automatically.",
    "",
  ];
  if (recent[0]) {
    const s = recent[0];
    lines.push("### Last session");
    lines.push(
      `**${formatTime(s.started_at)}** · ${formatDistance(s.distance_m)} · ${formatStepsShort(s.steps)} steps · ${formatDurationLong(s.duration_s)}`,
    );
  }
  return lines.join("\n");
}

function renderMetadata(data: Status | undefined): React.ReactNode {
  if (!data) return null;
  const sd = stateDisplay(data.connected, data.belt_state);
  return (
    <Detail.Metadata>
      <Detail.Metadata.TagList title="Belt">
        <Detail.Metadata.TagList.Item text={sd.label} color={sd.color} />
      </Detail.Metadata.TagList>
      <Detail.Metadata.Label title="Speed" text={formatSpeed(data.speed_kmh)} />
      <Detail.Metadata.Separator />
      <Detail.Metadata.Label
        title="Today distance"
        text={formatDistance(data.today.distance_m)}
      />
      <Detail.Metadata.Label
        title="Today steps"
        text={formatStepsShort(data.today.steps)}
      />
      <Detail.Metadata.Label
        title="Today time"
        text={formatDurationLong(data.today.duration_s)}
      />
      <Detail.Metadata.Label
        title="Today kcal"
        text={formatKcal(data.today.kcal)}
      />
      {data.current_session && (
        <>
          <Detail.Metadata.Separator />
          <Detail.Metadata.Label
            title="Session avg"
            text={formatSpeed(data.current_session.avg_speed_kmh)}
          />
          <Detail.Metadata.Label
            title="Session peak"
            text={formatSpeed(data.current_session.max_speed_kmh)}
          />
        </>
      )}
    </Detail.Metadata>
  );
}

function sampleSpeeds(cs: CurrentSession): number[] {
  return (cs.samples ?? [])
    .map((s: Sample) => s.speed_kmh)
    .filter((v): v is number => Number.isFinite(v));
}

// --- Action helpers --------------------------------------------------------

function SetSpeedAction({ onSubmit }: { onSubmit: (v: number) => void }) {
  return (
    <Action.Push
      title="Set Speed…"
      icon={Icon.Gauge}
      shortcut={{ modifiers: ["cmd"], key: "e" }}
      target={
        <SpeedForm
          title="Set Speed"
          submitLabel="Set Speed"
          onSubmit={onSubmit}
        />
      }
    />
  );
}

function SetStartSpeedAction() {
  return (
    <Action.Push
      title="Set Default Start Speed…"
      icon={Icon.BullsEye}
      shortcut={{ modifiers: ["cmd"], key: "d" }}
      target={<StartSpeedForm />}
    />
  );
}

function SpeedForm({
  title,
  submitLabel,
  onSubmit,
  initial = "3.0",
}: {
  title: string;
  submitLabel: string;
  onSubmit: (v: number) => void;
  initial?: string;
}) {
  const { pop } = useNavigation();
  return (
    <Form
      navigationTitle={title}
      actions={
        <ActionPanel>
          <Action.SubmitForm
            title={submitLabel}
            icon={Icon.Checkmark}
            onSubmit={(values: { speed: string }) => {
              const n = Number(values.speed);
              if (!Number.isFinite(n)) {
                showToast({
                  style: Toast.Style.Failure,
                  title: "Invalid number",
                  message: values.speed,
                });
                return;
              }
              onSubmit(clampSpeed(n));
              pop();
            }}
          />
        </ActionPanel>
      }
    >
      <Form.TextField
        id="speed"
        title="Speed (km/h)"
        placeholder="0.5 – 6.0"
        defaultValue={initial}
        info="Range 0.5 – 6.0 km/h, rounded to 0.1."
      />
    </Form>
  );
}

function StartSpeedForm() {
  return (
    <SpeedForm
      title="Default Start Speed"
      submitLabel="Save Default"
      onSubmit={(v) =>
        showToast({
          style: Toast.Style.Animated,
          title: `Saving ${v.toFixed(1)} km/h…`,
        }).then((toast) =>
          api
            .setStartSpeed(v)
            .then(() => {
              toast.style = Toast.Style.Success;
              toast.title = `Default set to ${v.toFixed(1)} km/h`;
            })
            .catch((e: Error) => {
              toast.style = Toast.Style.Failure;
              toast.title = "Failed";
              toast.message = e.message;
            }),
        )
      }
    />
  );
}
