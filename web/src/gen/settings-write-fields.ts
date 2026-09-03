// GENERATED from internal/store/settings_write_tables.json — do not edit.
//
// Rebuild: `go run ./cmd/settingswritegen`. `-check` runs in tools/verify.sh.
//
// This is the SERVER's own classification of every settings key, so the form
// collector can send a number where a number is expected and a boolean where a
// boolean is expected. The server accepts only a real `true` or the string
// "true" for a boolean — `1` and "on" both read as FALSE — and it IGNORES an
// invalid value rather than clamping it, so a key of the wrong type does not
// error, it silently fails to save.
//
// NOT every key here has an input on the Settings page, and not every input is
// here. The collector intersects this with the form map and skips whatever has
// no element.

/** Integer keys and their inclusive [min, max]. Out of range is IGNORED server-side. */
export const INT_FIELDS: Readonly<Record<string, readonly [number, number]>> = {
  "alertCpuThreshold": [1, 100],
  "alertPingLoss": [1, 100],
  "dbAlertRetentionDays": [1, 3650],
  "dbRetentionDays": [1, 3650],
  "firewallTopN": [1, 50],
  "historyMinutes": [5, 120],
  "maxConns": [1000, 100000],
  "notifCooldownSec": [10, 3600],
  "pollArp": [5000, 300000],
  "pollBandwidth": [1000, 60000],
  "pollBridges": [1000, 60000],
  "pollCapsman": [1000, 60000],
  "pollConns": [1000, 60000],
  "pollDhcp": [10000, 600000],
  "pollDns": [1000, 60000],
  "pollFirewall": [1000, 30000],
  "pollIfaces": [10000, 600000],
  "pollIfstatus": [1000, 60000],
  "pollPackages": [5000, 600000],
  "pollPing": [1000, 30000],
  "pollPpp": [1000, 60000],
  "pollQueues": [2000, 60000],
  "pollRosusers": [5000, 300000],
  "pollRouting": [500, 300000],
  "pollSystem": [1000, 60000],
  "pollTalkers": [1000, 60000],
  "pollTopology": [5000, 600000],
  "pollVlans": [1000, 60000],
  "pollVpn": [1000, 30000],
  "pollWan": [1000, 60000],
  "pollWifi": [10000, 600000],
  "pollWireless": [10000, 600000],
  "routerPort": [1, 65535],
  "smtpPort": [1, 65535],
  "topN": [1, 50],
  "topTalkersN": [1, 20],
  "updateCheckHours": [1, 168],
  "vpnDashTopN": [1, 50],
};

/** Trimmed and cut to 256 by the server. */
export const STR_FIELDS: readonly string[] = [
  "notifTitle",
  "ntfyUrl",
  "pingTarget",
  "smtpFrom",
  "smtpHost",
  "smtpTo",
  "telegramChatId",
];

/** Only a real `true` or the string "true" counts as true. */
export const BOOL_FIELDS: readonly string[] = [
  "notifBackupDrift",
  "notifBackupFail",
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
  "notifReportFail",
  "notifRouterStatus",
  "notifRouterUpdate",
  "notifVpn",
  "ntfyEnabled",
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
  "pingEnabled",
  "pushbulletEnabled",
  "rosDebug",
  "smtpEnabled",
  "smtpSecure",
  "telegramEnabled",
  "userNotifyEnabled",
];

/** Sealed at rest and NOT trimmed. A masked value is dropped; an EMPTY STRING is a destructive clear. */
export const CRED_FIELDS: readonly string[] = [
  "ntfyToken",
  "pushbulletApiKey",
  "smtpPass",
  "smtpUser",
  "telegramBotToken",
];

/** Validated outside the four tables — see internal/store/settings_write.go. */
export const SPECIAL_CASES: readonly string[] = [
  "authMode",
  "customPollProfile",
  "displayTimezone",
  "notifBody",
  "notifBodyUp",
  "sessionTimeoutMs",
];
