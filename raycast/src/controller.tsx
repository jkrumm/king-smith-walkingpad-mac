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
import { api, clampSpeed, quickSpeeds, speedStep } from "./lib/api";
import {
  formatDistance,
  formatDuration,
  formatDurationLong,
  formatKcal,
  formatSpeed,
  formatStepsShort,
} from "./lib/format";
import { useStatus } from "./lib/hooks";
import { resampleForSparkline, sparkline } from "./lib/sparkline";
import { isRunning, stateDisplay } from "./lib/state";
import type { CurrentSession, Status } from "./lib/types";

const REFRESH_MS = 1000;

export default function Controller() {
  const { data, isLoading, revalidate, error } = useStatus(REFRESH_MS);
  const presets = useMemo(() => quickSpeeds(), []);
  const step = useMemo(() => speedStep(), []);

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
        revalidate();
      } catch (e) {
        toast.style = Toast.Style.Failure;
        toast.title = label;
        toast.message = (e as Error).message;
      }
    },
    [revalidate],
  );

  const onStart = (speed?: number) =>
    runAction(`Start${speed ? ` at ${speed.toFixed(1)} km/h` : ""}`, () =>
      api.start(speed),
    );
  const onStop = () => runAction("Stop", () => api.stop());
  const onSetSpeed = (v: number) =>
    runAction(`Speed → ${v.toFixed(1)} km/h`, () => api.setSpeed(v));
  const onBumpSpeed = (delta: number) => {
    const next = clampSpeed((data?.speed_kmh ?? 1.0) + delta);
    return onSetSpeed(next);
  };
  const onSync = () =>
    runAction("Sync to Argo", async () => {
      const r = await api.sync();
      return r;
    });

  const markdown = useMemo(() => renderMarkdown(data, error), [data, error]);
  const metadata = useMemo(() => renderMetadata(data), [data]);

  const running = isRunning(data?.belt_state);

  return (
    <Detail
      isLoading={isLoading}
      markdown={markdown}
      metadata={metadata}
      navigationTitle="WalkingPad"
      actions={
        <ActionPanel>
          <ActionPanel.Section title="Belt">
            {running ? (
              <Action
                title="Stop"
                icon={Icon.Stop}
                shortcut={{ modifiers: ["cmd"], key: "." }}
                onAction={onStop}
              />
            ) : (
              <Action
                title="Start"
                icon={Icon.Play}
                shortcut={{ modifiers: ["cmd"], key: "return" }}
                onAction={() => onStart()}
              />
            )}
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
            <SetSpeedAction onSubmit={onSetSpeed} />
            <SetStartSpeedAction />
          </ActionPanel.Section>

          <ActionPanel.Section title="Quick Set">
            {presets.map((v, i) => (
              <Action
                key={v}
                title={`${v.toFixed(1)} km/h`}
                icon={Icon.Gauge}
                shortcut={quickShortcut(i)}
                onAction={() => onSetSpeed(v)}
              />
            ))}
          </ActionPanel.Section>

          <ActionPanel.Section title="More">
            <Action
              title="Refresh"
              icon={Icon.ArrowClockwise}
              shortcut={{ modifiers: ["cmd"], key: "r" }}
              onAction={revalidate}
            />
            <Action
              title="Sync to Argo"
              icon={Icon.Upload}
              shortcut={{ modifiers: ["cmd"], key: "y" }}
              onAction={onSync}
            />
            <Action
              title="Open Today"
              icon={Icon.Calendar}
              shortcut={{ modifiers: ["cmd"], key: "t" }}
              onAction={() =>
                launchCommand({ name: "today", type: LaunchType.UserInitiated })
              }
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

function renderMarkdown(
  data: Status | undefined,
  error: Error | undefined,
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

  const sd = stateDisplay(data.connected, data.belt_state);
  const headline = data.connected
    ? `# ${sd.label}${data.belt_state === "ACTIVE" ? ` · ${formatSpeed(data.speed_kmh)}` : ""}`
    : "# Disconnected";

  const lines: string[] = [headline, ""];

  const cs = data.current_session;
  if (cs) {
    lines.push(`### Current session · ${formatDuration(cs.duration_s)}`);
    lines.push(
      `**${formatDistance(cs.distance_m)}** · **${formatStepsShort(cs.steps)} steps** · ${formatKcal(cs.kcal)}`,
    );
    lines.push(
      `avg ${formatSpeed(cs.avg_speed_kmh)} · peak ${formatSpeed(cs.max_speed_kmh)}`,
    );
    const spark = sessionSparkline(cs);
    if (spark) {
      lines.push("");
      lines.push("```");
      lines.push(spark);
      lines.push("```");
    }
    lines.push("");
  }

  lines.push("### Today");
  lines.push(
    `**${formatDistance(data.today.distance_m)}** · **${formatStepsShort(data.today.steps)} steps** · ${formatDurationLong(data.today.duration_s)} · ${formatKcal(data.today.kcal)}`,
  );

  if (!data.connected) {
    lines.push("");
    lines.push("---");
    lines.push(
      "> Belt is offline — the daemon will reconnect automatically once it appears.",
    );
  }

  return lines.join("\n");
}

function sessionSparkline(cs: CurrentSession): string | undefined {
  if (!cs.samples || cs.samples.length < 2) return undefined;
  const speeds = cs.samples.map((s) => s.speed_kmh);
  return sparkline(resampleForSparkline(speeds, 60));
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
      <Detail.Metadata.Label title="Mode" text={data.mode ?? "—"} />
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
      <Detail.Metadata.Separator />
      <Detail.Metadata.Label title="Device" text={data.device.name || "—"} />
      <Detail.Metadata.Label
        title="RSSI"
        text={data.device.rssi ? `${data.device.rssi} dBm` : "—"}
      />
    </Detail.Metadata>
  );
}

// Inline form, pushed onto the navigation stack so the controller stays focused.
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
