import { Color, Icon } from "@raycast/api";

// Belt state → display metadata. Kept centralised so every view renders the
// same colour and label for a given wire value.

export interface StateDisplay {
  label: string;
  icon: Icon;
  color: Color;
  // Short tag for the menu-bar title when we want one extra signal.
  tag: string;
}

export function stateDisplay(connected: boolean, belt?: string): StateDisplay {
  if (!connected) {
    return {
      label: "Disconnected",
      icon: Icon.XMarkCircle,
      color: Color.Red,
      tag: "offline",
    };
  }
  switch (belt) {
    case "ACTIVE":
      return {
        label: "Walking",
        icon: Icon.CircleFilled,
        color: Color.Green,
        tag: "walking",
      };
    case "STARTING":
      return {
        label: "Starting…",
        icon: Icon.Hourglass,
        color: Color.Yellow,
        tag: "starting",
      };
    case "STOPPING":
      return {
        label: "Stopping…",
        icon: Icon.Hourglass,
        color: Color.Orange,
        tag: "stopping",
      };
    case "STANDBY":
      return {
        label: "Standby",
        icon: Icon.MoonDown,
        color: Color.SecondaryText,
        tag: "standby",
      };
    case "STOPPED":
      return {
        label: "Stopped",
        icon: Icon.Circle,
        color: Color.SecondaryText,
        tag: "idle",
      };
    default:
      return {
        label: belt ?? "Idle",
        icon: Icon.Circle,
        color: Color.SecondaryText,
        tag: "idle",
      };
  }
}

export function isRunning(belt?: string): boolean {
  return belt === "ACTIVE" || belt === "STARTING" || belt === "STOPPING";
}
