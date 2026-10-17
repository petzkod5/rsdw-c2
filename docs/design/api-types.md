# RSDW C2 HTTP API: TypeScript type drafts

> **DRAFT.** Derived by static reading of the Go source at commit `ee2746e`. Go was not compiled or run to produce this. Treat every type as a hypothesis until it is checked by contract tests that replay real responses (token mode, OIDC admin, OIDC viewer, demo mode). Anything that could not be settled from source is marked `UNVERIFIED:`.

Source references are `file:line` relative to the repository root. Shared enums and aliases live in the last `ts` block ("Errors / Enums") and are referenced by the domain blocks above it.

## Reading rules used for the drafts

- `time.Time` becomes `ISODateTime` (RFC 3339, Go emits RFC3339Nano). A zero `time.Time` marshals as `"0001-01-01T00:00:00Z"`. A few timestamp-like fields are plain Go `string` (`Server.lastSeen`, `Server.lastRestart`) and can be `""`.
- `omitempty` becomes `?:`. A pointer with `omitempty` is absent, never `null`. A pointer without `omitempty` becomes `| null`. Go struct values tagged `omitempty` are **never omitted** (`encoding/json` ignores `omitempty` for struct values), so they are required here (`Event.evidence`, `BackupRun.bundle`, `BackupRun.idempotency`).
- A nil slice or map without `omitempty` marshals as `null`; each such case is called out on the field.
- `map[string]T` becomes `Record<string, T>`; `map[string]any` handlers are typed with exactly the keys the handler writes.
- Request decoders call `DisallowUnknownFields` (`main.go:1578`, `edit.go:30`, `integrations.go:333`, `reboots.go:291`, `backups_http.go:64`, `deletion.go:105`, `save.go:90`). Request types below are therefore exact: an extra key yields HTTP 400. In request types a field is required only when the server rejects its absence; otherwise it is optional with the default noted.
- Every `/api/*` response carries `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` (`main.go:1021-1024`). `204` responses set `Content-Type: application/json` but have an empty body (`main.go:1566-1572`); do not call `response.json()` on them.
- Unknown routes and wrong methods on `/api/servers/*` answer `404 {"error":"route not found"}`. Several other prefixes answer `405` with an `Allow` header.

## Authentication model (affects every call)

- **Token mode** (default, `RSDW_AUTH_MODE=token`): send `Authorization: Bearer <RSDW_ADMIN_TOKEN>` on every request. The only role is `admin` (`subject` is `"token-admin"`). `POST /api/session` merely validates a token; it creates no cookie and no server state (`auth.go:395-406`). In demo mode with no token configured every request is an admin (`auth.go:266`).
- **OIDC mode**: browser holds cookies `__Host-rsdw-session` and `__Host-rsdw-login` (HttpOnly, Secure, SameSite=Lax; `auth.go:102-103,332-338`). Roles are `admin` and `viewer`. **Any `Authorization` header makes the request unauthenticated** (`auth.go:271-273`); the SPA must not send one in OIDC mode (web/app.js:427 only attaches it when the mode is not `oidc`).
- **CSRF (OIDC only)**: every non-GET/HEAD request must carry `Origin` equal to the configured public origin and `X-CSRF-Token` equal to `AuthStatus.csrfToken`, else `403 {"error":"request verification failed"}` (`auth.go:316,328-330`).
- **`X-RSDW-Session` response header (OIDC only)**: set to the CSRF token on every response that passed authentication, including later `403`s (`auth.go:314-315`). It is not set on `401`, on `GET /api/auth`, or on the `/api/auth/*` route-handled responses. web/app.js:434 compares it with the remembered token to detect a session swap.
- **Viewer role** may call only `GET /api/bootstrap` and `GET /api/servers/:id/telemetry` (`auth.go:297-306,321`); everything else is `403 {"error":"permission denied"}`. Handlers for integrations, reboots, backups also re-check admin (`integrations.go:275`, `reboots.go:226`, `backups_http.go:76`).

## Endpoint table

Role column: `none` = answered before authentication; `any` = viewer or admin; `admin` = admin only. "Multipart" means `multipart/form-data` with exactly the named parts; unknown parts are rejected.

| Method | Path | Auth / role | Request type | Response type | Notes |
|---|---|---|---|---|---|
| GET | `/api/auth` | none | none | `AuthStatus` | 200 always (also when unauthenticated). 400 `ambiguous authentication cookies` if a cookie name repeats (OIDC). `auth.go:340-353` |
| POST | `/api/session` | none (token mode only) | `SessionRequest` | 204, empty body | 401 `invalid admin token`. In OIDC mode the route is not registered (falls through to 401/404). `auth.go:395-406` |
| GET | `/api/auth/login` | none (OIDC only) | none | 302 to IdP, or 303 to origin `/` if already signed in | Sets `__Host-rsdw-login`. 503 `{error}` when busy. Browser navigation, not `fetch`. `auth.go:410-444` |
| GET | `/api/auth/callback` | none (OIDC only) | query `AuthCallbackQuery` | 303 to origin `/` (sets session cookie) or `/?signin=failed` | Never returns JSON. `auth.go:446-572` |
| POST | `/api/auth/logout` | session + CSRF (OIDC only) | none | 204, empty body | Clears both cookies. 401 / 403 `{error}`. In token mode: 401/404. `auth.go:366-391` |
| GET | `/api/bootstrap` | any | none | `BootstrapAdmin` (admin) or `BootstrapViewer` (viewer) | Server list is sorted by world label. `main.go:1086-1110` |
| GET | `/api/servers/:id/logs?tail=` | admin | none | `ServerLogs` | `tail` 1..500, default 100 (invalid falls back to 100). 404 `server not found`, 409 `server is stopped...`, 502. `main.go:1354-1368` |
| GET | `/api/servers/:id/telemetry?range=` | any | none | `AdminTelemetry` (admin) or `ViewerTelemetry` (viewer) | `range` is `60s` (default), `5m`, `1h`; any other value silently means 60s. `main.go:1369-1377` |
| GET | `/api/servers/:id/deletion` | admin | none | `DeletionRecord` | 404 `deletion receipt not found`. Works after the server row is gone. `main.go:1341-1348` |
| POST | `/api/servers` | admin | `CreateServerRequest` (JSON) or `CreateServerMultipart` (parts `request` + `save`) | `Server` (201) | 400 validation, 409 lifecycle lock busy, 413 save too large, 502 deploy failure, 503. `main.go:1210-1327`, `save.go:40-125` |
| DELETE | `/api/servers/:id` | admin | `DeleteServerRequest` (JSON body on DELETE) | `DeletionRecord` (200 completed, 202 in progress) | `confirm` must equal the server id. 400/404/409/503. `deletion.go:99-247` |
| POST | `/api/servers/:id/restore` | admin | `RestoreRequest` (JSON) or `RestoreMultipart` (parts `request` + `save`) | `RestoreResult` (202) | Server must be `stopped`. 409 with `code: "restore_confirmation_required"` when the target already has save data. `backups_http.go:945-1037` |
| POST | `/api/servers/:id/actions/restart` | admin | none (body ignored; UI sends `{}`) | `Server` (200) or `RestartPending` (202) | 409 if stopped or a restart is already pending. `alerts.go:172-202` |
| POST | `/api/servers/:id/actions/stop` | admin | none (body ignored) | `Server` (200) | `lifecycle.go:113-131` |
| POST | `/api/servers/:id/actions/start` | admin | none (body ignored) | `Server` (200) | `lifecycle.go:133-174` |
| POST | `/api/servers/:id/actions/update` | admin | `UpdateServerRequest` | `Server` (200) | 400 bad tag, 409 if stopped. `main.go:1404-1447` |
| POST | `/api/servers/:id/actions/check-update` | admin | none (body ignored) | `Server` (200) | Reads `updateAvailable` from the returned server. `main.go:1449-1484` |
| POST | `/api/servers/:id/actions/edit-settings` | admin | `EditServerRequest` | `Server` (200) | Requires `confirm: true` and at least one setting. `edit.go:27-149` |
| GET | `/api/events` | admin | query `EventListQuery` | `EventList` | `events.go:176-183` |
| GET | `/api/users` | admin | none | `UsersResponse` | `main.go:1120-1132` |
| POST | `/api/users` | admin | `UserInput` | `User` (201) | 409 duplicate `playerId`. `main.go:1204-1207` |
| PUT | `/api/users/:id` | admin | `UserInput` | `User` (200) | 404 `saved ID not found`. |
| DELETE | `/api/users/:id` | admin | none | 204 | 404 `saved ID not found`. |
| GET | `/api/integrations` | admin | none | `IntegrationsResponse` | `integrations.go:281-303` |
| POST | `/api/integrations` | admin | `IntegrationInput` | `Integration` (201) | 400 body is `ApiError` with `fields` (`IntegrationFieldKey`). `integrations.go:332-390` |
| PUT | `/api/integrations/:id` | admin | `IntegrationInput` | `Integration` (200) | 404 `integration not found`. No DELETE route exists (405). |
| POST | `/api/integrations/:id/test` | admin | none (body ignored; UI sends `{}`) | `Delivery` (202) | Queues a test delivery even if the integration is disabled. `integrations.go:304-327` |
| GET | `/api/reboots` | admin | none | `RebootsResponse` | `reboots.go:268-288` |
| POST | `/api/reboots` | admin | `RebootRequest` | `RebootSchedule` (201) | 400 body is `ApiError` with `fields` (`RebootFieldKey`). `reboots.go:397-428` |
| PUT | `/api/reboots/:id` | admin | `RebootRequest` | `RebootSchedule` (200) | 404 `reboot schedule not found`. `reboots.go:430-460` |
| DELETE | `/api/reboots/:id` | admin | none | 204 | `reboots.go:462-480` |
| POST | `/api/reboots/preview` | admin | `RebootRequest` | `RebootPreview` (200) | `enabled` optional here (defaults false); `serverId` optional. `reboots.go:358-395` |
| GET | `/api/backups` | admin | none | `BackupsOverview` | Same payload for `/api/backups/profiles`, `/api/backups/runs`, `/api/backups/schedules` (all GET). `backups_http.go:81-131,182-217` |
| POST | `/api/backups` and `/api/backups/runs` | admin | `BackupRunRequest` | `BackupRun` (201) | Raw run, not `BackupRunView`. 404/409/503/507. `backups_http.go:286-308,474-486` |
| GET | `/api/backups/runs/:id` | admin | none | `BackupRunView` | 404 `backup run not found`. `backups_http.go:781-790` |
| GET | `/api/backups/runs/:id/bundle` | admin | none | `application/zip` stream | `Content-Disposition: attachment; filename="backup-<id>.zip"`. 404/503 JSON `ApiError`. `backups_http.go:792-821` |
| POST | `/api/backups/profiles` | admin | `BackupDefinitionInput` | `BackupDefinition` (200, not 201) | 409 duplicate id or built-in id. `backups_http.go:488-540` |
| PUT | `/api/backups/profiles/:id` | admin | `BackupDefinitionInput` | `BackupDefinition` (200) | `revision` must equal the stored revision; response has revision + 1. |
| DELETE | `/api/backups/profiles/:id` | admin | none | 204 | 409 if built-in or used by a schedule. `backups_http.go:542-568` |
| POST | `/api/backups/profiles/import` | admin | `BackupProfileImportRequest` (JSON) or raw YAML body | `BackupDefinition` (201) | Body limit 128 KiB. 409 duplicate. `backups_http.go:570-607` |
| GET | `/api/backups/profiles/:id/export` | admin | none | `application/yaml` | `Content-Disposition: attachment; filename="<id>.yaml"`. `backups_http.go:609-624` |
| POST | `/api/backups/schedules` | admin | `BackupScheduleRequest` | `BackupScheduleView` (200, not 201) | `backups_http.go:626-710` |
| PUT | `/api/backups/schedules/:id` | admin | `BackupScheduleRequest` | `BackupScheduleView` (200) | 404 `backup schedule not found`. |
| DELETE | `/api/backups/schedules/:id` | admin | none | 204 | `backups_http.go:739-752` |
| POST | `/api/backups/schedules/:id/run` | admin | `BackupScheduleRunRequest` | `BackupRun` (201) | Extra run; does not move the next scheduled run. `backups_http.go:754-779` |
| POST | `/api/backups/:runId/create-server` | admin | `CreateServerFromBackupRequest` | `Server` (201) | New server is created `stopped`. `backups_http.go:823-936` |
| GET | `/api/image-tags` | admin | none | `string[]` | Semver tags, newest first. 502 `Could not load published image tags. Try again.` `main.go:1048-1058,897-967` |

### Capabilities by role (`auth.go:292-295`)

`capabilities(role)` always returns all 15 keys. There is **no `users` key**; web/app.js:76 maps `users`, `add-user`, `edit-user`, `delete-user` and `add-server` to `create`.

| Key | admin | viewer | denied |
|---|---|---|---|
| `dashboard`, `telemetry` | true | true | false |
| `events`, `maintenance`, `integrations`, `reboots`, `backups`, `create`, `delete`, `restart`, `stop`, `start`, `update`, `logs`, `updateCheck` | true | false | false |

## Auth

```ts
/** Go: auth.go:23-40. 'denied' is only ever seen inside GET /api/auth for unauthenticated callers. */
export type Role = 'admin' | 'viewer' | 'denied';

/** Go: auth.go:292-295. Exactly these 15 keys for every role. */
export type CapabilityKey =
  | 'dashboard' | 'telemetry' | 'events' | 'maintenance' | 'integrations'
  | 'reboots' | 'backups' | 'create' | 'delete' | 'restart' | 'stop'
  | 'start' | 'update' | 'logs' | 'updateCheck';
export type Capabilities = Record<CapabilityKey, boolean>;

/** GET /api/auth. Go: auth.go:351 (map[string]any, exactly these keys). */
export interface AuthStatus {
  mode: 'token' | 'oidc';
  /** a == nil || OIDC || token configured || !demo. False only for demo without a token. */
  required: boolean;
  authenticated: boolean;
  /** "" when unauthenticated; "token-admin" in token mode; the OIDC `sub` claim otherwise. */
  subject: string;
  role: Role;
  /** "" in token mode and when unauthenticated; random hex in OIDC mode. Send back as X-CSRF-Token. */
  csrfToken: string;
  capabilities: Capabilities;
}

/** POST /api/session. Go: auth.go:396-398. Success is 204 with no body. */
export interface SessionRequest {
  token: string;
}

/**
 * GET /api/auth/callback query (OIDC). Go: auth.go:452-456. Exactly one `state`; exactly one of
 * `code` / `error`. Not called from application code; the browser follows the IdP redirect.
 */
export interface AuthCallbackQuery {
  state: string;
  code?: string;
  error?: string;
}
```

## Servers & Telemetry

```ts
/** Go: main.go:186-196 (embedded in Server and CreateServerRequest, so keys are flat). */
export interface ServerSettings {
  worldName: string;
  /** 0 on servers stored before this setting existed and on the demo seed server (main.go:460). UI falls back to 7777 (web/app.js:1638). */
  gamePort: number;
  /** 0 when unset; UI falls back to 40. */
  storageGiB: number;
  /** '' when unset; UI falls back to LoadBalancer. */
  serviceType: ServiceType | '';
  /** Comma-separated lowercase 32-hex EOS ids, or "". */
  adminIds: string;
  additionalArgs: string;
  /** 0..3 (validated on create only, main.go:1529). */
  debugLevel: number;
  autoStopOnUpdate: boolean;
  validateGameFiles: boolean;
}

/** Go: main.go:73-109 minus the metric-dependent scalars (see ServerLive / ServerStored). */
export interface ServerCommon extends ServerSettings {
  /** Kubernetes ownership annotation value. Admin responses only; never sent to viewers. See Open questions #1. */
  ownershipToken?: string;
  /** Present only for servers created from an uploaded save. */
  saveSeed?: SaveSeed;
  /** Name of the Kubernetes Secret holding passwords, when any password was set. Passwords themselves are never returned. */
  passwordSecret?: string;
  stopSource?: StopSource;
  id: string;
  /** Creator / owner display name (NOT the world label; see worldLabel() in deletion.go:70). */
  name: string;
  namespace: string;
  release: string;
  /** Always "" outside demo (set only in main.go:460). */
  region: string;
  ownerId: string;
  currentImage: string;
  desiredImage: string;
  status: ServerStatus;
  maxPlayers: number;
  memoryLimitMiB: number;
  cpuLimitMillis: number;
  metricsAvailable: boolean;
  /** RFC3339 / RFC3339Nano string or "". */
  lastRestart: string;
  /** RFC3339 / RFC3339Nano string, or "" when there is no fresh observation (telemetry.go:295). */
  lastSeen: string;
  updateAvailable: boolean;
  /** "" until observed (main.go:1273-1275). */
  endpoint: string;
}

/** Go: save.go:25-28 */
export interface SaveSeed {
  claim: string;
  path: string;
}

/** Go: telemetry.go:60-81 (metricCatalog); the exact key set of every metrics map. */
export interface MetricReading {
  /** Go *float64 without omitempty. */
  value: number | null;
  status: MetricStatus;
  source: string;
  unit: string;
  observedAt: ISODateTime | null;
  /** "" when status is 'available'. */
  reason: string;
}
/** Go type is map[string]MetricReading; emptyMetrics() (telemetry.go:83) seeds all 20 keys, so every key is present when non-null. */
export type MetricMap = Record<MetricKey, MetricReading>;

/**
 * Go: main.go:111-127 Server.MarshalJSON. Two wire shapes, discriminated by `metrics`:
 *  - ServerLive: `metrics` is a map; the eight legacy scalars are NOT emitted.
 *  - ServerStored: `metrics` is null; the eight legacy scalars ARE emitted (zero values when unobserved).
 * Test: telemetry_test.go:141 TestServerJSONOmitsLegacyMetricsOnlyWhenMetricsPresent.
 */
export interface ServerLive extends ServerCommon {
  metrics: MetricMap;
}
export interface ServerLegacyScalars {
  players: number;
  uptimeSeconds: number;
  tickRate: number;
  cpuPercent: number;
  memoryUsedBytes: number;
  memoryLimitBytes: number;
  diskPercent: number;
  networkBytesPerSecond: number;
}
export interface ServerStored extends ServerCommon, ServerLegacyScalars {
  metrics: null;
}
/**
 * Bootstrap and telemetry always return ServerLive (they pass through telemetryFor/joinObservation).
 * Create, restart, stop, start, update, check-update, edit-settings, create-server-from-backup and
 * the `server` inside logs return whatever is persisted, which may be either variant.
 */
export type Server = ServerLive | ServerStored;

/** Go: viewer.go:8-15. Same keys as MetricReading, narrower status vocabulary. */
export interface ViewerMetric {
  value: number | null;
  status: ViewerMetricStatus;
  source: string;
  unit: string;
  observedAt: ISODateTime | null;
  /** "Metric is not currently available" unless status is 'available' (viewer.go:68-70). */
  reason: string;
}

/** Go: viewer.go:30-39. Everything else on Server is withheld from viewers. */
export interface ViewerServer {
  /** Raw Server.WorldName (may be ""), not worldLabel(). */
  worldName: string;
  id: string;
  name: string;
  /** Unrecognised statuses are coerced to 'unknown' (viewer.go:42-46). */
  status: ServerStatus;
  maxPlayers: number;
  lastSeen: string;
  /** All 20 catalog keys. */
  metrics: Record<MetricKey, ViewerMetric>;
  metricsAvailable: boolean;
}

/** Go: main.go:1102 and main.go:1107 (map[string]any). */
export interface ClusterInfo {
  name: string;
  region: string;
  uptimeSeconds: number;
}

/** GET /api/bootstrap, admin. Go: main.go:1105-1109. */
export interface BootstrapAdmin {
  /** Go map without omitempty: nil map marshals as null. Keyed by server id. Includes completed receipts of deleted servers. */
  deletions: Record<string, DeletionRecord> | null;
  servers: ServerLive[];
  /** Newest 20. Nil slice marshals as null when there are no events (main.go:1587-1594). web/app.js never reads this field. */
  events: ServerEvent[] | null;
  mode: BootstrapMode;
  cluster: ClusterInfo;
  capabilities: Capabilities;
}

/** GET /api/bootstrap, viewer. Go: main.go:1102. Has no `deletions` and no `events` keys at all. */
export interface BootstrapViewer {
  servers: ViewerServer[];
  mode: BootstrapMode;
  cluster: ClusterInfo;
  capabilities: Capabilities;
}

/** Go: telemetry.go:32-35 */
export interface ConnectedPlayer {
  name: string;
  characterName: string;
}

/** Go: telemetry.go:45-50 */
export interface PlayerRoster {
  /** omitempty in Go, but telemetryFor() and viewerPlayerRoster() always set it. */
  status?: RosterStatus;
  reason?: string;
  /** Milliseconds the roster stays fresh; omitted when 0 (non-'available' rosters). Max 45000. */
  freshForMs?: number;
  /** null (no omitempty, nil slice) when status is 'unavailable' or 'error'. */
  players: ConnectedPlayer[] | null;
}

/** Go: main.go:220-224 */
export interface HealthCheck {
  /** Currently only "Game API engine readiness". */
  name: string;
  /** 'healthy' when engineReady == 1, 'starting' for any other engineReady value, otherwise the engineReady metric status (telemetry.go:419-428). */
  status: 'healthy' | 'starting' | MetricStatus;
  at: ISODateTime;
}
export interface ViewerHealthCheck {
  name: string;
  status: 'healthy' | 'starting' | ViewerMetricStatus;
  at: ISODateTime;
}

/** Go: main.go:226-229 */
export interface MetricDefinition {
  metric: MetricKey;
  description: string;
}

/**
 * Go: main.go:218 (map[string]any) built in telemetry.go:404-418 and viewer.go:129-153.
 * One point per retained observation (max 240, last hour). For every MetricKey the value is a
 * number or null; the three side maps carry one entry per MetricKey.
 */
export type MetricSample<S extends string = MetricStatus> = Record<MetricKey, number | null> & {
  timestamp: ISODateTime;
  observedAt: Record<MetricKey, ISODateTime | null>;
  status: Record<MetricKey, S>;
  reason: Record<MetricKey, string>;
};

/** GET /api/servers/:id/telemetry, admin. Go: main.go:208-216. */
export interface AdminTelemetry {
  server: ServerLive;
  playerRoster: PlayerRoster;
  metrics: MetricMap;
  metricsAvailable: boolean;
  /** Never null (initialised to []). Oldest first. */
  samples: MetricSample<MetricStatus>[];
  /** Never null. Zero or one entry. */
  healthChecks: HealthCheck[];
  /** Never null. 20 entries. */
  metricDefinitions: MetricDefinition[];
}

/** GET /api/servers/:id/telemetry, viewer. Go: viewer.go:76-84. */
export interface ViewerTelemetry {
  server: ViewerServer;
  /** Only name + characterName per player; status 'unavailable' / 'error' carry a fixed reason string. */
  playerRoster: PlayerRoster;
  metrics: Record<MetricKey, ViewerMetric>;
  metricsAvailable: boolean;
  samples: MetricSample<ViewerMetricStatus>[];
  metricDefinitions: MetricDefinition[];
  healthChecks: ViewerHealthCheck[];
}

/** Go: main.go:231-235 */
export interface LogLine {
  timestamp: ISODateTime;
  /** Derived from message text: contains ERROR -> ERROR, else WARN -> WARN, else INFO (main.go:1500-1506). */
  level: 'INFO' | 'WARN' | 'ERROR';
  message: string;
}

/** GET /api/servers/:id/logs. Go: main.go:1366. `server` is the persisted row, so either Server variant. */
export interface ServerLogs {
  server: Server;
  /** Never null. */
  lines: LogLine[];
}

/**
 * POST /api/servers (JSON). Go: main.go:173-184 (+ embedded ServerSettings, flat).
 * Required by validateCreate (main.go:1519-1564): name, ownerId, maxPlayers. Everything else optional.
 */
export interface CreateServerRequest {
  /** Creator / owner display name, 1..48 chars. The UI labels this "Owner name". */
  name: string;
  /** Exactly 32 hex chars (normalised to lowercase). */
  ownerId: string;
  /** 1..64 */
  maxPlayers: number;
  /** World label shown in the UI. Default: `name`. Single line, <= 2048. The UI labels this "Server name". */
  worldName?: string;
  /** Default "dragonwilds". Lowercase DNS label. */
  namespace?: string;
  /** Default RSDW_DEFAULT_IMAGE_TAG (0.1.1). */
  imageTag?: string;
  /** Default 7777. 1024..65535. */
  gamePort?: number;
  /** Default 40. 1..2048. */
  storageGiB?: number;
  /** Default 'LoadBalancer'. */
  serviceType?: ServiceType;
  /** Not normalised on create (only edit-settings normalises); UI normalises client-side. */
  adminIds?: string;
  additionalArgs?: string;
  /** 0..3. Default 0. */
  debugLevel?: number;
  autoStopOnUpdate?: boolean;
  validateGameFiles?: boolean;
  /** Default (2 + maxPlayers) * 1024. 256..67584. */
  memoryLimitMiB?: number;
  /** Default 500 * maxPlayers. 100..64000. */
  cpuLimitMillis?: number;
  /** Write-only, single line <= 2048. */
  serverPassword?: string;
  /** Write-only, single line <= 2048. */
  adminPassword?: string;
}

/**
 * POST /api/servers (multipart). Go: save.go:40-125. Build a FormData with exactly these two parts.
 * `request` is a JSON string (<= 32 KiB, no filename); `save` is one non-empty .sav file (<= 32 MiB,
 * plain filename, no leading '.' or '-', <= 128 bytes). Whole request <= 32 MiB + 64 KiB.
 */
export interface CreateServerMultipart {
  request: CreateServerRequest;
  save: File;
}

/** POST .../actions/update. Go: main.go:204-206, validated at main.go:1418. */
export interface UpdateServerRequest {
  /** /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/ */
  imageTag: string;
}

/**
 * POST .../actions/edit-settings. Go: edit.go:14-25. Pointers: absent or null means "leave unchanged".
 * An empty string for a password CLEARS it. At least one setting is required.
 */
export interface EditServerRequest {
  /** Must be literally true. */
  confirm: true;
  /** 1..48, single line. */
  name?: string;
  /** 1..2048, single line. Changing it also requires confirmWorldName. */
  worldName?: string;
  /** 1..64 */
  maxPlayers?: number;
  /** 256..67584 */
  memoryLimitMiB?: number;
  /** 100..64000 */
  cpuLimitMillis?: number;
  /** "" clears; omit to keep. */
  serverPassword?: string;
  /** "" clears; omit to keep. */
  adminPassword?: string;
  /** Comma-separated 32-hex ids; normalised (lowercased, de-duplicated). */
  adminIds?: string;
  confirmWorldName?: boolean;
}

/** 202 body of POST .../actions/restart when the command outcome is unknown. Go: alerts.go:198. */
export interface RestartPending {
  operationId: string;
  status: 'awaiting_reconciliation';
  message: string;
}

/** DELETE /api/servers/:id body. Go: deletion.go:100-104. */
export interface DeleteServerRequest {
  /** Must equal the server id. */
  confirm: string;
  /** Default 'keep'. */
  mode?: DeletionMode;
  /** Required and exactly `DELETE WORLD <serverId>` when mode is 'purge'. */
  purgeConfirm?: string;
}

/** Go: deletion.go:40-45 */
export interface ResourceIdentity {
  kind: string;
  namespace: string;
  name: string;
  uid: string;
}

/** Go: deletion.go:47-56. The four unconditional slices are null when empty (state.clone builds them from nil). */
export interface DeletionPlan {
  storage: ResourceIdentity;
  revision: number;
  digest: string;
  resources: ResourceIdentity[] | null;
  world: ResourceIdentity[] | null;
  secrets: ResourceIdentity[] | null;
  retainedSecrets?: ResourceIdentity[];
  seeds: ResourceIdentity[] | null;
}

/** GET .../deletion, DELETE response, and values of BootstrapAdmin.deletions. Go: deletion.go:58-68. */
export interface DeletionRecord {
  serverId: string;
  worldLabel: string;
  namespace: string;
  release: string;
  mode: DeletionMode;
  plan: DeletionPlan;
  /** Absent once completed or when never set. */
  deadlineAt?: ISODateTime;
  completed: boolean;
  lastError?: string;
}

/**
 * POST .../restore (JSON). Go: backups_http.go:938-943. Content-Type application/json.
 * source 'backup' requires manifestId (== BackupRun.id of a succeeded run). source 'custom' requires the multipart form.
 */
export interface RestoreRequest {
  source: 'backup' | 'custom';
  manifestId?: string;
  /** Must be true or the request is rejected (400). */
  confirmEmpty: boolean;
  /** Send true only after a 409 restore_confirmation_required. */
  destructiveConfirm?: boolean;
}
/** POST .../restore (multipart). Go: backups_http.go:1039-1105. `save` is required iff source === 'custom' and forbidden otherwise. */
export interface RestoreMultipart {
  request: RestoreRequest;
  save?: File;
}
/** 202 body. Go: backups_http.go:1036. */
export interface RestoreResult {
  serverId: string;
  source: 'backup' | 'custom';
  status: 'restored';
  /** Uploaded filename for 'custom'; "" for 'backup'. */
  file: string;
}
```

## Events

```ts
/** Go: alert_evidence.go:11-15 */
export interface PlayerCountEvidence {
  available: boolean;
  players: number;
  maxPlayers: number;
}
/** Go: alert_evidence.go:17-21 */
export interface RestartWarningEvidence {
  trigger: 'scheduled' | 'memory-pressure';
  restartAt: ISODateTime;
  minutes: number;
}
/** Go: alert_evidence.go:23-27. Every member is omitempty. */
export interface AlertEvidence {
  playerCount?: PlayerCountEvidence;
  joinedPlayers?: ConnectedPlayer[];
  restartWarning?: RestartWarningEvidence;
}

/**
 * Go: main.go:129-146 (`Event`; renamed to avoid the DOM `Event` type).
 * Appears in GET /api/events, BootstrapAdmin.events and Delivery.event.
 */
export interface ServerEvent {
  id: string;
  timestamp: ISODateTime;
  /** "" for `integration_test` events (they have no server). */
  serverId: string;
  /** worldLabel(server) for most events; server.Name for reboot audits (reboots.go:563). */
  serverName: string;
  /** '' only for integration_test events inside a Delivery. */
  category: EventCategory | '';
  /** '' only for integration_test events inside a Delivery. */
  severity: EventSeverity | '';
  message: string;
  details: string;
  /** Always present, `{}` when empty: omitempty has no effect on a struct value. */
  evidence: AlertEvidence;
  /** Absent on events not produced by emitAlertEvidence / appendRebootAudit (e.g. "Server created", demo events). */
  kind?: EventKind;
  source?: string;
  accuracy?: EventAccuracy;
  operationId?: string;
  scheduleId?: string;
  occurrenceId?: string;
  /** Set only on reboot_schedule_changed audits (the OIDC subject). */
  actor?: string;
}

/** GET /api/events query. Go: events.go:106-133. Send as URLSearchParams. */
export interface EventListQuery {
  /** Case-insensitive substring over message + details + serverName. */
  query?: string;
  /** '' or 'all' means no filter. */
  category?: EventCategory | 'all' | '';
  /** '' or 'all' means no filter. */
  serverId?: string;
  /** RFC3339; anything else is 400 "since must be an RFC3339 timestamp". */
  since?: ISODateTime;
  /** 1..2500, default 100; out-of-range or non-numeric silently becomes 100. */
  limit?: number;
  /** >= 0; invalid or negative silently becomes 0. */
  offset?: number;
}

/** GET /api/events. Go: events.go:36-41. */
export interface EventList {
  /** Newest first; never null. */
  events: ServerEvent[];
  /** Matches before pagination. */
  total: number;
  /** Matches with severity === 'warning' (before pagination). */
  warnings: number;
  /** Matches with severity 'critical' OR 'error' (events.go:160). */
  critical: number;
}
```

## Users

```ts
/** Go: main.go:167-171 */
export interface User {
  id: string;
  name: string;
  /** 32 lowercase hex chars. */
  playerId: string;
}

/** POST /api/users and PUT /api/users/:id body. Go: main.go:1143-1146. Both fields required. */
export interface UserInput {
  /** Trimmed, 1..48 chars, single line. */
  name: string;
  /** 32 hex chars (any case; stored lowercase). Unique across users, else 409 `this player ID is already saved`. */
  playerId: string;
}

/** GET /api/users. Go: main.go:1131. Sorted by name, then id. Never null. */
export interface UsersResponse {
  users: User[];
}
```

## Integrations

```ts
/** Go: integrations.go:63-66 */
export interface SecretReference {
  /** Kubernetes Secret name (DNS subdomain, <= 253). Never a token. */
  name: string;
  /** /^[A-Za-z0-9._-]{1,253}$/ */
  key: string;
}

/** Go: notification_targets.go:64-68 */
export interface QuietHours {
  /** HH:mm, must differ from end. */
  start: string;
  end: string;
  /** IANA zone name; 'Local' and '' rejected. */
  timezone: string;
}

/** Go: integrations.go:38-44 (alertRules, integrations.go:46-61). `available` is true for all 14 current rules. */
export interface AlertRule {
  kind: EventKind;
  label: string;
  available: boolean;
  source: string;
  accuracy: EventAccuracy;
}

/** Go: integrations.go:68-80. Used for list items and for POST / PUT responses. */
export interface Integration {
  /** omitempty in Go but always populated: normalised on load (integrations.go:121) and on save (:344). */
  provider: IntegrationProvider;
  /** Webhook provider only. Echoed in full. */
  webhookUrl?: string;
  /** Absent when quiet hours are off (a request value of null is stored as absent). */
  quietHours?: QuietHours;
  id: string;
  name: string;
  enabled: boolean;
  /** "" for webhook integrations. */
  guildId: string;
  /** "" for webhook integrations. */
  channelId: string;
  secretRef: SecretReference;
  /** Never null. */
  serverIds: string[];
  /** Never null. Only keys the client sent. */
  rules: Partial<Record<EventKind, boolean>>;
}

/**
 * POST /api/integrations and PUT /api/integrations/:id body. Go: integrations.go:332-356, validated at :232-272.
 * `id` is part of the Go struct but a non-empty value is rejected (400 "invalid integration JSON"); do not send it.
 */
export interface IntegrationInput {
  /** 1..80 chars, single line. */
  name: string;
  /** Omitted means false. */
  enabled?: boolean;
  /** Omitted means 'discord'. */
  provider?: IntegrationProvider;
  /** Discord: 1..20 digits. Must be empty / omitted for webhook. */
  guildId?: string;
  /** Discord: 1..20 digits. Must be empty / omitted for webhook. */
  channelId?: string;
  /** Webhook: public https URL, port 443, no credentials/query/fragment, <= 2048. Must be empty / omitted for discord. */
  webhookUrl?: string;
  quietHours?: QuietHours | null;
  secretRef: SecretReference;
  /** Each id must be an existing, non-deleting server; no duplicates. Omitted means []. */
  serverIds?: string[];
  /** Unknown kinds are rejected ("unknown alert rule"). Omitted means {}. */
  rules?: Partial<Record<EventKind, boolean>>;
}

/** Go: discord_messages.go:10-26 */
export interface DiscordEmbedField {
  name: string;
  value: string;
}
export interface DiscordEmbed {
  title: string;
  description: string;
  color: number;
  fields?: DiscordEmbedField[];
  footer: { text: string };
  timestamp?: ISODateTime;
}

/** Go: integrations.go:95-108. Also the 202 body of POST /api/integrations/:id/test. */
export interface Delivery {
  /** Absent on deliveries persisted before providers existed. */
  provider?: IntegrationProvider;
  webhookUrl?: string;
  id: string;
  integrationId: string;
  event: ServerEvent;
  guildId: string;
  channelId: string;
  status: DeliveryStatus;
  attempts: number;
  /** Zero time "0001-01-01T00:00:00Z" unless status is 'retry'. */
  nextAttempt: ISODateTime;
  updatedAt: ISODateTime;
  /** Human-readable outcome; "" while pending. */
  result: string;
}

/** Go: discord_messages.go:113-116. Present only for Discord deliveries whose event kind has a template. */
export interface DeliveryView extends Delivery {
  embed?: DiscordEmbed;
}

/** Go: alerts.go:13-18 */
export interface RestartOperation {
  id: string;
  runtime: string;
  requestedAt: ISODateTime;
  commandUncertain: boolean;
}

/** GET /api/integrations. Go: integrations.go:301 (map[string]any). */
export interface IntegrationsResponse {
  /** Sorted by id. Never null. */
  integrations: Integration[];
  /** Newest first, at most 100. Never null. */
  deliveries: DeliveryView[];
  rules: AlertRule[];
  /** Keyed by server id. Never null. */
  pendingRestarts: Record<string, RestartOperation>;
  demo: boolean;
}
```

## Reboots

```ts
/** Go: reboots.go:66-86 (rebootScheduleView). Used for list items and for POST / PUT responses. */
export interface RebootSchedule {
  /** 0..60; 0 disables warnings. Always emitted. */
  warningMinutes: number;
  id: string;
  serverId: string;
  /** worldLabel of the target server, "" if it no longer exists. */
  serverName: string;
  mode: RebootMode;
  /** Only for mode 'cron'. */
  cron?: string;
  /** Only for mode 'interval'. */
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  /** Only for mode 'daily'. Sorted, unique, zero-padded "HH:mm". */
  dailyTimes?: string[];
  /** IANA zone. */
  executionTimezone: string;
  enabled: boolean;
  revision: number;
  createdAt: ISODateTime;
  updatedAt: ISODateTime;
  /** Absent when disabled. */
  nextRun?: ISODateTime;
  lastOccurrenceAt?: ISODateTime;
  lastResult?: RebootResult;
  lastReason?: string;
  lastOperationId?: string;
}

/** Go: reboots.go:52-64 + 88-91 (rebootExecutionView embeds rebootExecution and adds serverName). */
export interface RebootExecution {
  /** omitempty in Go; always set for rows written by current code. UI falls back id <-> occurrenceId (web/app.js:1164). */
  id?: string;
  occurrenceId: string;
  scheduleId: string;
  revision: number;
  serverId: string;
  occurrenceAt: ISODateTime;
  recordedAt: ISODateTime;
  operationId?: string;
  result: RebootResult;
  reason?: string;
  /** Set for 'missed' rows when the downtime window is known. */
  missedThrough?: ISODateTime;
  /** worldLabel of the target server, "" if gone. */
  serverName: string;
}

/** GET /api/reboots. Go: reboots.go:281-287 (map[string]any). */
export interface RebootsResponse {
  /** Sorted by serverName, then id. Never null. */
  schedules: RebootSchedule[];
  /** Newest recordedAt first, at most 500. Never null. */
  history: RebootExecution[];
  demo: boolean;
  /** False when RSDW_REBOOTS_ENABLED=false or state is not persistent; enabled saves then fail 503. */
  available: boolean;
  now: ISODateTime;
}

/**
 * POST/PUT /api/reboots and POST /api/reboots/preview body. Go: reboots.go:93-104, normalised at :305-356.
 * Pointer members: null or absent both mean "not provided". Exactly the fields for `mode` may be present.
 */
export interface RebootRequest {
  /** Required (400 "serverId is required") except for preview. */
  serverId?: string;
  /** Required for create/update ("enabled is required"); preview defaults to false. */
  enabled?: boolean;
  /** Required. */
  mode: RebootMode;
  /** 'cron' only. Five-field standard cron, no descriptors / TZ prefix / '?'. */
  cron?: string | null;
  /** 'interval' only. hours 1..8760, days 1..365. */
  intervalValue?: number | null;
  /** 'interval' only. */
  intervalUnit?: IntervalUnit | null;
  /** 'daily' only. 1..1440 unique "HH:mm". */
  dailyTimes?: string[] | null;
  /** Required. IANA zone; 'Local', '' and offsets rejected. */
  executionTimezone: string;
  /** 0..60. Omitted means 0. */
  warningMinutes?: number;
  /** Must be true whenever enabled is true (400 with fields.acknowledgeDisconnect). Ignored by preview. */
  acknowledgeDisconnect?: boolean;
}

/** POST /api/reboots/preview. Go: reboots.go:106-111. Exactly five runs. */
export interface RebootPreview {
  asOf: ISODateTime;
  anchor: ISODateTime;
  executionTimezone: string;
  runs: ISODateTime[];
}
```

## Backups

```ts
/** Go: backups.go:60-64 */
export interface BackupSourceRule {
  serverState: BackupServerState;
  collector: 'server-files';
  /** Clean relative path, e.g. "RSDragonwilds/Saved/SaveGames". No '..', no absolute, no glob chars. */
  path: string;
}
/** Go: backups.go:66-71 */
export interface BackupItemSpec {
  /** Backup-id pattern: /^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$/ */
  name: string;
  /** Validator also accepts "" (backups.go:122). */
  kind: BackupItemKind | '';
  requirement: BackupItemRequirement;
  /** At least one; at most one rule per serverState. */
  sources: BackupSourceRule[];
}
/** Go: backups.go:73-82 (also the YAML document shape). Profile list items and profile CRUD responses. */
export interface BackupDefinition {
  apiVersion: 'rsdw-c2.petzko.dev/v1';
  kind: 'BackupDefinition';
  /** Backup-id pattern. 'dragonwilds-world-save' is the built-in and is read-only. */
  id: string;
  /** >= 1. Increments on every PUT. */
  revision: number;
  /** 1..128 chars. */
  name: string;
  /** Only 'dragonwilds' is accepted today. */
  serverType: string;
  strategy: BackupStrategy;
  items: BackupItemSpec[];
}
/**
 * POST /api/backups/profiles and PUT /api/backups/profiles/:id body. Go: backups_http.go:488-540.
 * apiVersion, kind and revision are defaulted when omitted. On PUT, `id` is forced from the path and
 * `revision` must equal the stored revision (409 "backup profile revision changed; reload before editing").
 */
export type BackupDefinitionInput =
  Omit<BackupDefinition, 'apiVersion' | 'kind' | 'revision'> &
  Partial<Pick<BackupDefinition, 'apiVersion' | 'kind' | 'revision'>>;

/** POST /api/backups/profiles/import when Content-Type is application/json. Go: backups_http.go:577-579. */
export interface BackupProfileImportRequest {
  yaml: string;
}

/** Go: backup_repository.go:41-45. In-progress and failed runs carry the zero value {key:"",size:0,sha256:""}. */
export interface BackupObject {
  key: string;
  size: number;
  sha256: string;
}
/** Go: backups.go:311-316 */
export interface IdempotencyRequest {
  actor: string;
  operation: string;
  key: string;
  requestDigest: string;
}

/** Go: backups.go:226-242. 201 body of POST /api/backups, /api/backups/runs and /api/backups/schedules/:id/run. */
export interface BackupRun {
  id: string;
  serverId: string;
  definitionId: string;
  scheduleId?: string;
  /** 'local' */
  backendId: string;
  source?: BackupSource;
  phase: BackupRunPhase;
  status: BackupRunStatus;
  /** Equals `id` for succeeded runs (backups_http.go:419). */
  manifestId?: string;
  /** Always present: omitempty has no effect on a struct. */
  bundle: BackupObject;
  /** Always present. */
  idempotency: IdempotencyRequest;
  itemCount?: number;
  error?: string;
  createdAt: ISODateTime;
  updatedAt: ISODateTime;
}

/** Go: backups.go:177-185 */
export interface BackupManifestItem {
  name: string;
  kind: BackupItemKind;
  sourcePath: string;
  size: number;
  sha256: string;
  objectKey: string;
  consistency: BackupItemConsistency;
}

/** Go: backups_http.go:44-54. List items and GET /api/backups/runs/:id. Overrides BackupRun.itemCount to always-present. */
export interface BackupRunView extends Omit<BackupRun, 'itemCount'> {
  itemCount: number;
  serverName: string;
  profileName: string;
  /** omitempty, but backupSourceLabel() never returns "" ("Unavailable" is the fallback). */
  sourceLabel?: string;
  /** Duplicate of bundle.size. */
  bundleSize: number;
  /** Duplicate of bundle.key; absent when "". */
  bundleKey?: string;
  /** Declared in Go but never populated (backups_http.go:52); will not appear. */
  createdAtLabel?: string;
  /** Manifest items for succeeded runs; absent otherwise. Included in the list payload. */
  items?: BackupManifestItem[];
}

/** Go: backups.go:244-263 */
export interface BackupSchedule {
  id: string;
  serverId: string;
  definitionId: string;
  backendId: string;
  enabled: boolean;
  /** Execution timezone (note: reboots call this executionTimezone). */
  timezone: string;
  /** Legacy single-string form of the timing: daily "HH:mm,HH:mm", interval "N unit", or cron text. */
  expression: string;
  /** Absent on schedules saved before modes existed; treat as 'cron' with `expression` (backups_http.go:460-463). */
  mode?: RebootMode;
  cron?: string;
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  dailyTimes?: string[];
  intervalAnchor?: ISODateTime;
  nextRun?: ISODateTime;
  lastRun?: ISODateTime;
  lastResult?: BackupRunStatus;
  lastError?: string;
  revision: number;
}
/** Go: backups_http.go:56-61. List items and POST/PUT response. */
export interface BackupScheduleView extends BackupSchedule {
  serverName: string;
  profileName: string;
  /** Display string such as "Daily at 02:00" or "Every 6 hours". */
  timing: string;
}

/** Go: backups.go:276-282. Always exactly one element, id 'local'. */
export interface StorageBackend {
  id: string;
  name: string;
  kind: 'local';
  status: 'connected' | 'disconnected';
  removable: boolean;
}

/** Go: backups_http.go:215 (map[string]any). */
export interface BackupLimits {
  repositoryBytes: number;
  /** null when storage is unavailable. */
  usedBytes: number | null;
  maxItemBytes: number;
  maxBundleBytes: number;
}

/** GET /api/backups (also /profiles, /runs, /schedules). Go: backups_http.go:207-216. */
export interface BackupsOverview {
  available: boolean;
  /** "" when available. */
  storageError: string;
  demo: boolean;
  storage: StorageBackend[];
  /** Sorted by name; includes the built-in profile. Never null. */
  profiles: BackupDefinition[];
  /** Sorted by serverName. Never null. */
  schedules: BackupScheduleView[];
  /** Newest first. Never null. */
  runs: BackupRunView[];
  limits: BackupLimits;
}

/**
 * POST /api/backups and /api/backups/runs body. Go: backups_http.go:19-28.
 * `actor` and `scheduled` exist on the Go struct and are accepted by the decoder, but the handler overwrites
 * them (backups_http.go:300-301); do not send them.
 */
export interface BackupRunRequest {
  serverId: string;
  definitionId: string;
  /** Default 'local'. */
  backendId?: string;
  scheduleId?: string;
  /** Default: server-generated per request. Reuse to make retries idempotent. */
  idempotencyKey?: string;
  /** Must be true (400 "confirm that C2 may read the server world storage"). */
  acknowledge: boolean;
}

/** POST /api/backups/schedules and PUT /api/backups/schedules/:id body. Go: backups_http.go:30-42, :626-726. */
export interface BackupScheduleRequest {
  serverId: string;
  definitionId: string;
  /** Default 'local'. */
  backendId?: string;
  /** Default true. */
  enabled?: boolean;
  /** Default 'daily'. */
  mode?: RebootMode;
  cron?: string;
  intervalValue?: number;
  intervalUnit?: IntervalUnit;
  dailyTimes?: string[];
  /** Default 'UTC'. */
  executionTimezone?: string;
  /** Must be true when the schedule is enabled. */
  acknowledge?: boolean;
}

/** POST /api/backups/schedules/:id/run body. Go: backups_http.go:760-762. */
export interface BackupScheduleRunRequest {
  acknowledge: true;
}

/** POST /api/backups/:runId/create-server body. Go: backups_http.go:833-838. */
export interface CreateServerFromBackupRequest {
  /** New world label, 1..48 chars. Becomes Server.worldName. */
  serverName: string;
  /** Owner display name, 1..48 chars. Becomes Server.name. */
  ownerName: string;
  /** 32 hex chars. */
  ownerId: string;
  /** Must be true. */
  confirmCreate: boolean;
}
```

## Errors / Enums

```ts
/** RFC 3339 / RFC3339Nano timestamp as produced by Go's time.Time JSON encoding. */
export type ISODateTime = string;

/**
 * Error envelope. Go: main.go:1574-1576 writeError emits ONLY {"error": string} (map[string]string).
 * `code` appears in exactly one place (backups_http.go:1018) and `fields` in two (integrations.go:380,
 * reboots.go:596,600), always together with HTTP 400 for `fields` and HTTP 409 for `code`.
 * web/app.js:445-450 reads error / code / fields exactly this way.
 */
export interface ApiError {
  error: string;
  /** Only 'restore_confirmation_required', on 409 from POST /api/servers/:id/restore. */
  code?: ApiErrorCode;
  /** Field key -> message. Only on 400 from integrations (IntegrationFieldKey) and reboots (RebootFieldKey). */
  fields?: Record<string, string>;
}
export type ApiErrorCode = 'restore_confirmation_required';

/** Go: notification_targets.go:70-94. Keys the integrations 400 `fields` map can contain. */
export type IntegrationFieldKey =
  | 'form' | 'name' | 'provider' | 'webhookUrl' | 'guildId' | 'channelId'
  | 'secretName' | 'secretKey' | 'quietStart' | 'quietEnd' | 'quietTimezone';

/**
 * Go: reboots.go:577-601. Keys the reboot 400 `fields` map can contain. Chosen by substring match on the
 * message, so attribution is approximate (see Open questions #19). 'intervalUnit' is never emitted.
 */
export type RebootFieldKey =
  | 'mode' | 'warningMinutes' | 'executionTimezone' | 'cron' | 'intervalValue'
  | 'dailyTimes' | 'serverId' | 'enabled' | 'acknowledgeDisconnect';

/** Go: main.go:61-71. 'checking' is synthesised client-side only (web/app.js:285-289). */
export type ServerStatus =
  'online' | 'starting' | 'attention' | 'stopped' | 'deleting' | 'stale' | 'unknown';
/** Go: lifecycle.go:12-15 */
export type StopSource = 'operator' | 'observed';
/** Go: main.go:1526 */
export type ServiceType = 'ClusterIP' | 'NodePort' | 'LoadBalancer';
/** Go: main.go:1093-1096 */
export type BootstrapMode = 'kubernetes' | 'demo';

/** Go: telemetry.go:60-81 (metricCatalog order). */
export type MetricKey =
  | 'players' | 'uptimeSeconds' | 'engineReady'
  | 'cpuCores' | 'cpuPercent' | 'cpuLimitCores'
  | 'memoryUsedBytes' | 'memoryLimitBytes'
  | 'diskUsedBytes' | 'diskCapacityBytes' | 'diskPercent'
  | 'inboundBytesPerSecond' | 'outboundBytesPerSecond' | 'networkBytesPerSecond'
  | 'tickRate' | 'tickP50Ms' | 'tickP95Ms' | 'tickP99Ms' | 'tickWindowSeconds' | 'tickSampleCount';
/**
 * Union of every status string assigned in telemetry.go / kube_telemetry.go (failReadings, setReading,
 * freshMetrics, applyTicks). Go type is plain string.
 */
export type MetricStatus =
  | 'available' | 'unavailable' | 'stale' | 'error' | 'unsupported'
  | 'warming_up' | 'warming' | 'faulted' | 'stopped' | 'starting';
/** Go: viewer.go:50-57. 'error' and 'faulted' are collapsed to 'unavailable'. */
export type ViewerMetricStatus =
  'available' | 'stale' | 'unsupported' | 'warming_up' | 'warming' | 'starting' | 'stopped' | 'unavailable';
/** Go: telemetry.go:37-43 */
export type RosterStatus = 'available' | 'unavailable' | 'error';

/** Go: integrations.go:17-36 (16 kinds). */
export type EventKind =
  | 'restart_requested' | 'restart_warning' | 'memory_pressure_restart_requested'
  | 'restart_completed' | 'restart_failed' | 'player_joined' | 'player_limit_reached'
  | 'server_down' | 'server_recovered' | 'backup_started' | 'backup_completed'
  | 'backup_failed' | 'integration_test' | 'reboot_schedule_changed'
  | 'server_stopped' | 'server_started';
/**
 * Values emitted by current code: main.go:1313,1439,1477, integrations.go:201-206, reboots.go:563.
 * Go type is plain string; older persisted state could hold other values (UNVERIFIED).
 */
export type EventCategory = 'system' | 'player' | 'health' | 'update' | 'maintenance';
/**
 * Values emitted: 'success', 'info', 'warning', 'error' (integrations.go:201-212, main.go:1672, edit.go:141).
 * 'critical' is never emitted but is counted by EventList.critical (events.go:160).
 */
export type EventSeverity = 'success' | 'info' | 'warning' | 'error';
/** Go: integrations.go:46-61 and :314 */
export type EventAccuracy = 'observed' | 'approximate';

/** Go: notification_targets.go:13-16 */
export type IntegrationProvider = 'discord' | 'webhook';
/** Go: integrations.go:82-91 */
export type DeliveryStatus = 'pending' | 'sending' | 'sent' | 'retry' | 'failed' | 'uncertain';

/** Go: reboot_time.go:14-20. Also used by backup schedules. */
export type RebootMode = 'cron' | 'interval' | 'daily';
/** Go: reboot_time.go:77 */
export type IntervalUnit = 'hours' | 'days';
/** Go: reboots.go:25-34 */
export type RebootResult =
  'awaiting_reconciliation' | 'completed' | 'failed' | 'skipped' | 'missed' | 'uncertain';

/** Go: deletion.go:33-38 */
export type DeletionMode = 'keep' | 'purge';

/** Go: backups.go:25-31. Only 'logical-files' passes validation. */
export type BackupStrategy = 'logical-files' | 'volume-snapshot' | 'log-bundle';
/** Go: backups.go:39-44 */
export type BackupServerState = 'running' | 'stopped';
/** Go: backups.go:46-51 */
export type BackupItemRequirement = 'required' | 'optional';
/** Go: backups.go:53-58 */
export type BackupItemKind = 'file' | 'directory';
/** Go: backups.go:163-167. Stored tag; labels are 'Running .sav.backup' / 'Stopped .sav' / 'Unavailable'. */
export type BackupSource = 'running-bak' | 'stopped-sav';
/** Go: backups.go:169-175 */
export type BackupItemConsistency = 'atomic-publish' | 'stable-read' | 'stopped-world';
/** Go: backups.go:209-215 */
export type BackupRunPhase = 'queued' | 'capturing' | 'publishing';
/** Go: backups.go:217-224. 'interrupted' is set at startup recovery (backups_app.go:592). */
export type BackupRunStatus = 'running' | 'succeeded' | 'failed' | 'interrupted';
```

### HTTP status cheat sheet (from handlers; not exhaustive)

| Code | Where |
|---|---|
| 200 | most reads and updates; profile and schedule saves also return 200 on create |
| 201 | `POST /api/servers`, users, integrations, reboots, backup runs, profile import, create-server |
| 202 | delete in progress, restart with unknown outcome, restore result, integration test, `DeletionRecord` retry |
| 204 | `POST /api/session`, `POST /api/auth/logout`, all DELETE of users / reboots / backup profiles / backup schedules |
| 400 | validation, malformed JSON, `fields` envelope (integrations, reboots) |
| 401 / 403 | `authentication required` / `permission denied` / `request verification failed` |
| 404 | unknown server, schedule, run, profile, route |
| 405 | wrong method on users, integrations, reboots, backups (sets `Allow`) |
| 409 | lifecycle lock busy, server stopped, restart pending, duplicate, revision conflict, `restore_confirmation_required` |
| 413 | create upload too large |
| 502 / 503 / 507 | orchestrator or registry failure / state or backup storage unavailable / backup quota exceeded (`ErrBackupQuotaExceeded`, `backups_http.go:477`) |

## Open questions / ambiguities found

1. **Secrets-adjacent fields in admin Server JSON.** `ownershipToken` (`main.go:74`), `passwordSecret` (`main.go:80`) and `saveSeed` (`main.go:78`) are serialised in every admin `Server` (bootstrap, create, actions, logs). `ownershipToken` is the value matched against Kubernetes ownership annotations before deletion (`deletion.go:724,974`, `passwords.go:85`). web/app.js reads none of the three (the string `saveSeed` appears only in help text). Decide in the migration whether the TS `Server` should expose them or a Go change should drop them; add a contract test that viewers never receive them (they do not today, `viewer.go:30-39`).
2. **`Server` has two wire shapes.** `metrics: null` plus eight scalar fields, or a populated `metrics` map and no scalars (`main.go:111-127`, guarded by `telemetry_test.go:141`). Bootstrap and telemetry are always the live shape; create, create-from-backup and any persisted row may be the stored shape. web/app.js:639 reads `server[key]` only when `state.mode === 'demo'` and no `metrics[key]` exists, which in practice happens only for the record built from the `POST /api/servers` response (web/app.js:370-377). The React client should read only `metrics` and ignore the scalars.
3. **Timestamps typed `string`.** `Server.lastSeen` and `Server.lastRestart` are Go strings (RFC3339 or RFC3339Nano, or `""`), not `time.Time`. `lastSeen` is `""` whenever there is no fresh observation (`telemetry.go:295`). `Delivery.nextAttempt` is `0001-01-01T00:00:00Z` unless retrying (`integrations.go:105,180`). Parsers must tolerate both.
4. **`region` and `endpoint`.** `region` is only ever set by the demo seed (`main.go:460`) so it is `""` in Kubernetes mode, and no UI code reads it. `endpoint` is `""` until an observation resolves it (`main.go:1273-1275`, `telemetry.go:309-311`).
5. **Legacy zero values in `ServerSettings`.** Demo-seeded and pre-settings servers have `gamePort: 0`, `storageGiB: 0`, `serviceType: ""`; the UI substitutes 7777 / 40 / LoadBalancer (web/app.js:1638). UNVERIFIED: whether any other consumer relies on these being non-zero.
6. **`Event.evidence` is always present** (`{}` when empty) because `omitempty` does not apply to structs (`main.go:130`). Nothing in web/app.js reads `evidence`, `actor`, `operationId`, `scheduleId`, `occurrenceId`, `accuracy` or `source` on events; only `kind` is read (delivery cards, web/app.js:1027).
7. **Event enums vs UI.** `maintenance` events exist (`reboots.go:563`) but web/app.js:11 `categoryLabels` and the category chips at web/app.js:862 only know system / player / health / update, so maintenance events can only be found under "All". Severity `info` is emitted (`edit.go:141`, `backups_http.go:917`) but the UI `status()` known list (web/app.js:276) lacks `info`, so it renders with the `unknown` style. `critical` is counted by the API but never emitted.
8. **Bootstrap nullables.** In admin bootstrap `events` is `null` when empty (`append([]Event(nil), ...)`, `main.go:1587-1588`) and `deletions` is `null` when there are none (a nil map placed into `map[string]any`, no `omitempty`, `main.go:1106`). The viewer response omits both keys. web/app.js:1335 guards `deletions` with `|| {}` and never reads `bootstrap.events` (it uses `/api/events`).
9. **UI reads fields Go never emits.** `bootstrap.clusterName` (web/app.js:1344, Go emits `cluster.name`); telemetry `points`, `current`, `latest` (web/app.js:625,785; Go emits `samples` and `server`); `result.logs` for logs (web/app.js:1299; Go emits `lines`); `result.server.updateAvailable` after check-update (web/app.js:2142; Go returns the server itself, `updateAvailable` at top level); reboot error control `intervalUnit` (web/app.js:1174; Go never keys an error to it). These are dead fallbacks and should not be ported.
10. **Go emits fields the UI never reads.** Server: `ownershipToken`, `passwordSecret`, `saveSeed`, `stopSource`, `region`, `release` (read once), the eight legacy scalars. Bootstrap: `cluster.region`, `cluster.uptimeSeconds`, `events`. Backups: `limits.maxItemBytes`, `limits.maxBundleBytes`, `storageError`, run `phase` / `updatedAt` / `idempotency` / `bundle.key` / `bundle.sha256`. Integrations: `deliveries[].guildId` / `channelId` / `webhookUrl`, `rules[].accuracy`.
11. **`users` capability does not exist.** The UI asks `can('users')` but `capabilities` has no such key; web/app.js:76 aliases it (and `add-server`, `restore-server`, etc.) to `create` / `maintenance` / `reboots` / `backups`. The React permission helper must keep the same alias table or the Go side must add keys.
12. **DELETE with a JSON body.** `DELETE /api/servers/:id` requires a JSON body (`confirm`, optional `mode` / `purgeConfirm`) and rejects an empty body with 400 (`deletion.go:105-110`). Other DELETEs (users, reboots, profiles, schedules) take no body. Some HTTP clients and proxies drop DELETE bodies.
13. **Body-less actions ignore their body.** restart / stop / start / check-update never decode a body, so the UI's `{}` is harmless; `update` and `edit-settings` use `DisallowUnknownFields` and reject extra keys (`main.go:1414`, `edit.go:30`).
14. **`name` versus `worldName` on create.** `CreateServerRequest.name` is the owner/creator display name and `worldName` is the world label; the create form's "Server name" field posts `worldName` and "Owner name" posts `name` (web/app.js:1649). The same inversion exists for create-from-backup (`serverName` -> `worldName`, `ownerName` -> `name`, `backups_http.go:895-896`). `adminIds` is normalised only by edit-settings (`edit.go:63`), not by create (`main.go:1519-1564` only length-checks it).
15. **Different response shapes for the same entity.** `POST /api/backups` and `/runs` and `POST /schedules/:id/run` return a raw `BackupRun` (no `serverName`, `profileName`, `items`, `bundleSize`), while list and `GET /runs/:id` return `BackupRunView`. `POST /api/backups/profiles` and `POST /api/backups/schedules` return 200, not 201, unlike every other create. `GET /api/backups`, `/profiles`, `/runs`, `/schedules` all return the same combined overview, there is no per-collection payload.
16. **`BackupRunView.createdAtLabel` is dead.** Declared (`backups_http.go:52`) but never assigned, so it never appears. `sourceLabel` has `omitempty` but is always non-empty.
17. **Backup schedule `mode` is optional.** Schedules saved before modes existed have only `expression` (and `timezone`); the server treats them as cron (`backups_http.go:460-463`) but the UI edit form defaults a missing `mode` to `daily` (web/app.js:1477). UNVERIFIED whether any such persisted schedules exist in practice.
18. **Integration identity rules.** POST and PUT reject a non-empty `id` in the body (`integrations.go:339`); there is no DELETE route (405 message at `integrations.go:329`) although the UI has no delete either. `quietHours: null` in a request is stored as absent and the response omits the key. `provider` is `omitempty` on `Integration` but always present in practice; on `Delivery` it can genuinely be absent for old rows.
19. **Reboot validation field attribution is substring-based** (`reboots.go:577-597`). For example the invalid-mode message ("mode must be cron, interval, or daily") contains "cron", so it is keyed `fields.cron`, and "interval schedules must not include cron or daily fields" is also keyed `cron`. `fields.mode` is effectively unreachable from `normalizeRebootRequest`. Contract tests should capture real payloads before the React form maps keys to controls.
20. **Telemetry range and tail clamp silently.** An unknown `range` yields a 60-second window (`telemetry.go:360-367`); an out-of-range `tail` becomes 100 (`main.go:1358`). Neither returns 400.
21. **Auth edge cases.** `GET /api/auth` is the only way to learn `mode` and `csrfToken`; if a stale `Authorization` header is sent in OIDC mode it reports `authenticated: false` and the UI retries without it (web/app.js:534-542). `X-RSDW-Session` is also present on 403 responses but absent on 401. UNVERIFIED: behaviour behind a reverse proxy that rewrites `Origin` (OIDC CSRF requires an exact match, `auth.go:329`).
22. **Restart returns two shapes.** `200 Server` or `202 RestartPending` (`alerts.go:198,201`); the UI ignores the body. `stop` persists the stopped state before the scale call, so a 502 from stop leaves the server recorded as stopped (`lifecycle.go:119-129`).
23. **Integration-test events are not in `/api/events`.** They exist only inside `Delivery.event`, with empty `serverId`, `serverName`, `category`, `severity` and `details` (`integrations.go:313-314`, `events.go:44-52` classifies them as routine).
24. **Log and telemetry `server` payloads.** `ServerLogs.server` is the persisted row (either Server variant, possibly stale `status`), while telemetry's `server` is freshly joined with the latest observation. UNVERIFIED: whether any consumer should prefer one over the other.
