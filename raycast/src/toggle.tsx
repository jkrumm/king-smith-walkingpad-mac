import { showHUD, showToast, Toast } from "@raycast/api";
import { api, defaultStartSpeed } from "./lib/api";
import { isRunning } from "./lib/state";

// Global-hotkey-friendly no-view command. Bind in Raycast → Extensions →
// WalkingPad → Start / Stop → Record (recommended ⌥*). Reads the live belt
// state and flips it: running → Stop, otherwise → Start at the user's
// Default Start Speed preference (3.0 km/h out of the box).
export default async function toggle() {
  try {
    const status = await api.status();
    if (!status.connected) {
      await showHUD("WalkingPad offline");
      return;
    }
    if (isRunning(status.belt_state)) {
      await api.stop();
      await showHUD("⏹ Stopped");
      return;
    }
    const v = defaultStartSpeed();
    await api.start(v);
    await showHUD(`▶ Started at ${v.toFixed(1)} km/h`);
  } catch (e) {
    await showToast({
      style: Toast.Style.Failure,
      title: "Toggle failed",
      message: (e as Error).message,
    });
  }
}
