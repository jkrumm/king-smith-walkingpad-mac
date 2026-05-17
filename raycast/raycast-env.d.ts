/// <reference types="@raycast/api">

/* 🚧 🚧 🚧
 * This file is auto-generated from the extension's manifest.
 * Do not modify manually. Instead, update the `package.json` file.
 * 🚧 🚧 🚧 */

/* eslint-disable @typescript-eslint/ban-types */

type ExtensionPreferences = {
  /** Daemon URL - Where the WalkingPad daemon HTTP API is listening */
  "baseUrl": string,
  /** API Token - Bearer token for the daemon. Leave empty when running on loopback with auth disabled. */
  "apiToken": string,
  /** Speed Step - km/h increment for Speed Up / Speed Down actions */
  "speedStep": string
}

/** Preferences accessible in all the extension's commands */
declare type Preferences = ExtensionPreferences

declare namespace Preferences {
  /** Preferences accessible in the `controller` command */
  export type Controller = ExtensionPreferences & {}
  /** Preferences accessible in the `menu-bar` command */
  export type MenuBar = ExtensionPreferences & {}
  /** Preferences accessible in the `today` command */
  export type Today = ExtensionPreferences & {}
  /** Preferences accessible in the `history` command */
  export type History = ExtensionPreferences & {}
  /** Preferences accessible in the `set-speed` command */
  export type SetSpeed = ExtensionPreferences & {}
}

declare namespace Arguments {
  /** Arguments passed to the `controller` command */
  export type Controller = {}
  /** Arguments passed to the `menu-bar` command */
  export type MenuBar = {}
  /** Arguments passed to the `today` command */
  export type Today = {}
  /** Arguments passed to the `history` command */
  export type History = {}
  /** Arguments passed to the `set-speed` command */
  export type SetSpeed = {}
}

