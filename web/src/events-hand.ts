// The events whose payload is a Go MAP, typed by hand.
//
// Every other event's payload is a Go struct, and its type is generated into
// gen/payloads.ts by cmd/tsgen. These are the ones Go builds as
// `map[string]any` — mostly replies to a request, like `packages:ok` or
// `res:error` — so there is no struct to generate from. Each type here is read
// off the Go that builds it: every send site, with a key that only some of
// them set marked optional.
//
// ── IT CANNOT FALL OUT OF STEP WITH THE LIST ────────────────────────────────
//
// HandEventName is generated: exactly the events Go declares with a map
// payload. The check at the bottom fails tsc, naming the events, if this file
// misses one or types one that is not a map event — so an event added in Go,
// or moved from a map to a struct, is a compile error here rather than a
// silent gap.
//
// What it cannot check is the KEYS, which are read from the Go by hand. So a Go
// struct that a map carries is IMPORTED from gen/payloads.ts, never restated —
// cmd/tsgen's `extraRoots` exists to generate the ones no declaration reaches.
//
// ── TWO DISAGREEMENTS, RECORDED RATHER THAN RESOLVED ────────────────────────
//
// `res:ok`'s `movedId` is a string from a move and null from undo/redo, and
// `wifiscan:error`'s `scanId` is null from a refused start and a string from a
// failed scan. The types say exactly that. Making the Go agree is a behaviour
// change, not a typing one.

import type {
  AlertRow, ConnsPayload, HandEventName, Hunk, LanPayload, PingPoint, ResFieldError, WifiscanRow,
} from './gen/payloads';

/** The event carries no data — only the fact that it happened. */
type Nothing = Record<string, never>;

/** A write refused by a guard: the guard's own detail, and the fingerprint to confirm it with. */
interface GuardWarning {
  warning?: Record<string, unknown> | null;
  fingerprint?: string;
}

/**
 * One field of a resource form, as `resource.Describe` sends it.
 */
export interface ResSchemaField {
  name: string;
  label: string;
  type: string;
  input: string;
  required: boolean;
  options: string[] | null;
  placeholder: string;
  help: string;
  showIf: { field: string; in: string[] } | null;
  min: number | null;
  max: number | null;
  /** Shown in the form and never sent; the server skips it in every write. */
  display: boolean;
}

/**
 * A resource's form, as `res.Describe()` builds it, plus the three flags the
 * server adds. The only `res:*` payload with no `resource` key: it carries the
 * resource's name as `key`.
 */
export interface ResSchema {
  key: string;
  label: string;
  title: string;
  page: string;
  /** One identity field is a string; a composite identity is several. */
  identity: string | string[];
  actions: { key: string; label: string }[];
  fields: ResSchemaField[];
  /** False for a resource the router only lets you edit, never add. */
  creatable: boolean;
  permitted: boolean;
  unsupported: boolean;
  ordered: boolean;
}

/**
 * A stored router record, as `store.PublicRouter` passes it to the browser.
 *
 * MOSTLY UNTYPED ON PURPOSE. Go does not define most of these keys: they are
 * whatever routers.json holds, passed through. Only the keys Go sets or
 * guarantees are named.
 */
export type RouterRecord = Record<string, unknown> & {
  id: string;
  /** `""`, or the mask — never the password. */
  password: string;
  siteIds: string[];
  siteId: string | null;
  /** The link's rated speed. A record the store wrote carries both; the
   *  `|| 1000` default the pages apply is for one it did not. */
  bwDownMbps?: number;
  bwUpMbps?: number;
  backup?: Record<string, unknown> & { hasPassword: boolean };
};

/**
 * The fleet-wide settings a browser may read, from the whitelist in
 * internal/store/pagekeys.json. Each is optional on the wire: PageSettings
 * copies a key only when the stored settings have it.
 */
export interface PageSettings {
  pageWan: boolean; pageInterfaces: boolean; pageVlans: boolean; pageBridges: boolean;
  pageTopology: boolean; pageWifi: boolean; pageWireless: boolean; pageWifiMap: boolean; pageNetwatch: boolean; pageIpAddresses: boolean; pageCapsman: boolean;
  pageDhcp: boolean; pageDns: boolean; pageRouting: boolean; pagePpp: boolean;
  pageVpn: boolean; pageBandwidth: boolean; pageQueues: boolean; pageConnections: boolean;
  pageFirewall: boolean; pageRosusers: boolean; pageLogs: boolean; pagePackages: boolean;
  pageDevices: boolean; pageAudit: boolean; pageBackups: boolean;
  pingEnabled: boolean; userNotifyEnabled: boolean;
  /** DERIVED, not stored: the assistant is enabled AND has an endpoint and a
   *  model. The three settings behind it stay on the server — `aiBaseUrl` is
   *  infrastructure config a viewer has no business reading. */
  aiReady: boolean;
  notifIfaceUpDown: boolean; notifVpn: boolean; notifCpu: boolean; notifPing: boolean;
  notifNetwatch: boolean; notifRouterStatus: boolean; notifBackupDrift: boolean;
  notifBackupFail: boolean; notifReportFail: boolean; notifRouterUpdate: boolean;
  notifBgp: boolean; notifIfaceEther: boolean; notifIfaceWlan: boolean;
  notifIfaceBridge: boolean; notifIfaceVlan: boolean; notifIfaceOther: boolean;
  alertCpuThreshold: number; alertPingLoss: number; vpnDashTopN: number;
  displayTimezone: string;
}

export interface HandEvents {
  /** One finished answer. Markdown, rendered as DOM nodes — never as markup. */
  'ai:reply': { text: string; model: string };
  /** A refusal, already sanitised: it can carry the endpoint's host. */
  'ai:error': { error: string };
  'access:none': Nothing;
  'access:revoked': Nothing;
  'alerts:cleared-all': { routerId: string; ids: number[]; clearedAt: number; clearedBy: string | null };
  'alerts:open': { routerId: string; open: AlertRow[]; recent: AlertRow[] };
  'backups:diff': {
    id: number; against: number | null; baseline: boolean;
    /** Null when the diff was truncated. */
    added: number | null; removed: number | null;
    truncated: boolean; hunks: Hunk[];
  };
  'backups:error': { code: string; message?: string; was?: string; now?: string };
  'backups:ran': { routerId: string };
  'backups:restored': { routerId: string; id: number };
  'backups:restoring': { routerId: string; id: number };
  'backups:running': { routerId: string };
  'collection:config': {
    routerId: string; mode: string;
    enabled: Record<string, boolean>; stream: Record<string, boolean>;
    poll: Record<string, number>;
  };
  'collection:status': { routerId: string; dormant: string[] };
  // `ts` is always sent here, where ConnsPayload's own is omitempty.
  'conn:country-data': Pick<ConnsPayload, 'countryDests' | 'countryPorts'> & { ts: number };
  'conn:source-data': Pick<ConnsPayload, 'sourceDests' | 'sourcePorts'> & { ts: number };
  'lan:wan': Pick<LanPayload, 'ts' | 'wanIp'>;
  'packages:applying': { routerName: string; count: number; upgrade?: boolean };
  'packages:caps': { permitted: boolean; routerName: string };
  'packages:error': {
    code: string; name?: string; message?: string; routerName?: string;
    installed?: string; latest?: string;
    // The firmware refusals carry the pair they were judged on.
    current?: string; upgrade?: string;
  };
  'packages:notes': { version: string; error: string } | { version: string; notes: string };
  // `routerId` is on the upgrade's replies only: the router it went to, which
  // the dialog watches come back.
  'packages:ok': {
    action: string; name?: string; routerName?: string; routerId?: string;
    latest?: string; rebooting?: boolean;
    // `on` is the autoupgrade reply only: what the router holds after the write,
    // read back rather than echoed from the request.
    on?: boolean;
  };
  'perms:changed': Nothing;
  // minRtt / maxRtt are added only once a ping has landed, and then may be
  // null — unlike PingPayload, where they are omitted when absent.
  'ping:history': { target: string; history: PingPoint[]; minRtt?: number | null; maxRtt?: number | null };
  'queues:caps': { permitted: boolean; routerName: string };
  'queues:error': { code: string; name?: string; message?: string } & GuardWarning;
  'queues:ok': { action: string; name: string; menu: string };
  'res:error': {
    code: string; resource?: string; name?: string; message?: string;
    errors?: ResFieldError[];
  } & GuardWarning;
  'res:history': { resource: string; canUndo: boolean; canRedo: boolean; undoLabel: string; redoLabel: string };
  'res:new': { resource: string; options: Record<string, string[]> };
  'res:ok': { resource: string; action: string; name: string; movedId?: string | null };
  'res:preview': { resource: string; command: string };
  'res:row': {
    resource: string; id: string; identity: string; readOnly: boolean; removable: boolean;
    actions: string[]; values: Record<string, unknown>; options: Record<string, string[]>;
  };
  'res:schema': ResSchema;
  'rosusers:caps': { permitted: boolean; routerName: string };
  'rosusers:error': { code: string; name?: string; message?: string; minLength?: number };
  'rosusers:ok': { action: string; name: string };
  'router:active': { activeId: string };
  'router:disabled': { routerId: string };
  // `reason` is "" rather than null when there is no error, and absent from
  // the pooled-status path.
  'router:status': { routerId: string; connected: boolean; reason?: string };
  'router:switched': { activeId: string };
  'routers:update': RouterRecord[];
  'session:expired': Nothing;
  'settings:pages': Partial<PageSettings>;
  'setup:required': Nothing;
  'stream:health': { collector: string; degraded: boolean; restarts: number };
  'wan:caps': { permitted: boolean; routerName: string };
  // `name` is the interface, and `verb` the action the guard refused.
  'wan:error': { code: string; message?: string; name?: string; verb?: string } & GuardWarning;
  'wan:ok': { action: string; name: string };
  'wifiscan:done': { scanId: string; reason: string; rows: WifiscanRow[]; sampleCount: number; truncated: boolean };
  'wifiscan:error': { scanId: string | null; code: string; message?: string; iface?: string; retryAt?: number };
  'wifiscan:interfaces': {
    permitted: boolean; scanning: boolean;
    interfaces: { name: string; running: boolean; clients: number }[];
  };
  'wifiscan:rows': { scanId: string; rows: WifiscanRow[]; truncated: boolean };
  // Sent once, as a scan starts: `scanning` is always true and `rows` empty.
  'wifiscan:state': {
    scanning: boolean; scanId: string; iface: string; durationSec: number;
    startedAt: number; endsAt: number; currentChannelMhz: number | null;
  };
}

// ── BOTH DIRECTIONS, AT COMPILE TIME ────────────────────────────────────────
//
// If this file and the Go declarations disagree about which events carry a
// map, the type below stops being `true` and the assignment fails — with the
// offending event names in the error, under the key that says which way round.
type Missing = Exclude<HandEventName, keyof HandEvents>;
type NotAMapEvent = Exclude<keyof HandEvents, HandEventName>;
const handEventsMatchGo: [Missing, NotAMapEvent] extends [never, never]
  ? true
  : { missingFromThisFile: Missing; notAMapEventInGo: NotAMapEvent } = true;
void handEventsMatchGo;
