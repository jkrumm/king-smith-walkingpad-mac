import { showHUD, showToast, Toast } from "@raycast/api";
import { api, clampSpeed, speedStep } from "./lib/api";

// Global-hotkey-friendly no-view command. Pair with the user-bound shortcut
// in Raycast → Extensions → WalkingPad → Speed Down → Record.
export default async function speedDown() {
  try {
    const status = await api.status();
    if (!status.connected) {
      await showHUD("WalkingPad offline");
      return;
    }
    const current = status.speed_kmh ?? 0.5;
    const next = clampSpeed(current - speedStep());
    if (next === current) {
      await showHUD(`At min speed (${next.toFixed(1)} km/h)`);
      return;
    }
    await api.setSpeed(next);
    await showHUD(`▼ ${next.toFixed(1)} km/h`);
  } catch (e) {
    await showToast({
      style: Toast.Style.Failure,
      title: "Speed down failed",
      message: (e as Error).message,
    });
  }
}
