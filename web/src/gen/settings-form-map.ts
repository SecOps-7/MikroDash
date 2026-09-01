// GENERATED from testdata/settings-form-map.json — do not edit.
//
// Rebuild this file from the committed JSON, which is frozen (its generator
// read the Node app and was deleted on 2026-09-01): `node tools/settings-form-map-ts.js`.
// It exists so the renderer is driven
// by the SAME table the generator captured from the live populate(), rather than
// by a second copy that can drift.

export type FieldKind = 'value' | 'checkOn' | 'checkOff' | 'checkGuarded';

export interface ValueDefault {
  kind: string;
  /** Present for the `orNumber` shape: what an absent value renders as. */
  fallback?: number;
  /**
   * The assignment expression as the live populate() writes it, kept verbatim.
   * The renderer does NOT read this — the settings-populate check
   * evaluates it, so the comparison is against the original text rather than a
   * retyped copy of it.
   */
  expr?: string;
}

/** Inputs filled from a settings key, by how an ABSENT value is treated. */
export const FORM_FIELDS: Record<FieldKind, readonly string[]> = {
  "checkOff": [
    "ntfyEnabled",
    "pushbulletEnabled",
    "rosDebug",
    "routerTls",
    "routerTlsInsecure",
    "smtpEnabled",
    "smtpSecure",
    "telegramEnabled",
    "userNotifyEnabled"
  ],
  "checkOn": [
    "notifBackupDrift",
    "notifBackupFail",
    "pageAudit",
    "pageBackups",
    "pageBandwidth",
    "pageBridges",
    "pageCapsman",
    "pageConnections",
    "pageDevices",
    "pageDhcp",
    "pageDns",
    "pageFirewall",
    "pageInterfaces",
    "pageLogs",
    "pagePackages",
    "pagePpp",
    "pageQueues",
    "pageRosusers",
    "pageRouting",
    "pageTopology",
    "pageVlans",
    "pageVpn",
    "pageWan",
    "pageWifi",
    "pageWireless",
    "pingEnabled"
  ],
  "value": [
    "alertCpuThreshold",
    "alertPingLoss",
    "dbAlertRetentionDays",
    "dbRetentionDays",
    "defaultIf",
    "displayTimezone",
    "firewallTopN",
    "historyMinutes",
    "maxConns",
    "notifBody",
    "notifBodyUp",
    "notifCooldownSec",
    "notifTitle",
    "ntfyUrl",
    "pingTarget",
    "routerHost",
    "routerPort",
    "routerUser",
    "sessionTimeoutMs",
    "smtpFrom",
    "smtpHost",
    "smtpPort",
    "smtpTo",
    "smtpUser",
    "telegramChatId",
    "topN",
    "topTalkersN",
    "updateCheckHours",
    "vpnDashTopN"
  ],
  "checkGuarded": [
    "notifBgp",
    "notifCpu",
    "notifIfaceBridge",
    "notifIfaceEther",
    "notifIfaceOther",
    "notifIfaceUpDown",
    "notifIfaceVlan",
    "notifIfaceWlan",
    "notifNetwatch",
    "notifPing",
    "notifRouterStatus",
    "notifRouterUpdate",
    "notifVpn"
  ]
};

/** Per-field rule for an absent value; see the generator's valueKind(). */
export const VALUE_DEFAULTS: Record<string, ValueDefault> = {
  "alertCpuThreshold": {
    "kind": "bare",
    "expr": "data.alertCpuThreshold"
  },
  "alertPingLoss": {
    "kind": "bare",
    "expr": "data.alertPingLoss"
  },
  "dbAlertRetentionDays": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "dbRetentionDays": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "defaultIf": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "displayTimezone": {
    "kind": "orEmpty",
    "expr": "data.displayTimezone || ''"
  },
  "firewallTopN": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "historyMinutes": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "maxConns": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "notifBody": {
    "kind": "undefinedToEmpty",
    "expr": "data.notifBody   !== undefined ? data.notifBody   : ''"
  },
  "notifBodyUp": {
    "kind": "undefinedToEmpty",
    "expr": "data.notifBodyUp !== undefined ? data.notifBodyUp : ''"
  },
  "notifCooldownSec": {
    "kind": "bare",
    "expr": "data.notifCooldownSec"
  },
  "notifTitle": {
    "kind": "undefinedToEmpty",
    "expr": "data.notifTitle  !== undefined ? data.notifTitle  : ''"
  },
  "ntfyToken": {
    "kind": "blank",
    "expr": "''"
  },
  "ntfyUrl": {
    "kind": "orEmpty",
    "expr": "data.ntfyUrl || ''"
  },
  "pingTarget": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "pushbulletApiKey": {
    "kind": "blank",
    "expr": "''"
  },
  "routerHost": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "routerPass": {
    "kind": "blank",
    "expr": "''"
  },
  "routerPort": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "routerUser": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "sessionTimeoutMs": {
    "kind": "stringOf",
    "expr": "String(data.sessionTimeoutMs)"
  },
  "smtpFrom": {
    "kind": "orEmpty",
    "expr": "data.smtpFrom  || ''"
  },
  "smtpHost": {
    "kind": "orEmpty",
    "expr": "data.smtpHost  || ''"
  },
  "smtpPass": {
    "kind": "blank",
    "expr": "''"
  },
  "smtpPort": {
    "kind": "orNumber",
    "fallback": 587,
    "expr": "data.smtpPort  || 587"
  },
  "smtpTo": {
    "kind": "orEmpty",
    "expr": "data.smtpTo    || ''"
  },
  "smtpUser": {
    "kind": "orEmpty",
    "expr": "data.smtpUser  || ''"
  },
  "telegramBotToken": {
    "kind": "blank",
    "expr": "''"
  },
  "telegramChatId": {
    "kind": "orEmpty",
    "expr": "data.telegramChatId || ''"
  },
  "topN": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "topTalkersN": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  },
  "updateCheckHours": {
    "kind": "bare",
    "expr": "data.updateCheckHours"
  },
  "vpnDashTopN": {
    "kind": "undefinedToEmpty",
    "expr": "data[f] !== undefined ? data[f] : ''"
  }
};

/**
 * The credential inputs that are never given a value.
 *
 * populate() blanks them and uses the PLACEHOLDER to say whether one is stored.
 * `smtpUser` is deliberately NOT here: it is set as an ordinary value, so it
 * receives the mask and hands it back on save — which is what the server's
 * isMasked guard exists to catch.
 */
export const PLACEHOLDER_CREDENTIALS: Record<string, { whenSet: string; whenNot: string }> = {
  "routerPass": {
    "whenSet": "leave blank to keep current",
    "whenNot": "not set"
  },
  "telegramBotToken": {
    "whenSet": "leave blank to keep current",
    "whenNot": "paste token here"
  },
  "pushbulletApiKey": {
    "whenSet": "leave blank to keep current",
    "whenNot": "paste API key here"
  },
  "smtpPass": {
    "whenSet": "leave blank to keep current",
    "whenNot": "optional"
  },
  "ntfyToken": {
    "whenSet": "leave blank to keep current",
    "whenNot": "optional"
  }
};
