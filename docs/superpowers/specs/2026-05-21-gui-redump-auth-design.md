# GUI Redump Authentication Design

## Problem

The GUI currently checks Redump by sending unauthenticated SHA-1 quicksearch
requests. Some discs and hashes are only visible to logged-in Redump users, so
an anonymous miss can be false. Users need a way to provide Redump forum
credentials so hash lookups run as an authenticated user.

## Goals

- Add a top-level `Redump` tab to the GUI.
- Let the user enter, save, test, and clear Redump credentials.
- Store the Redump username and password in the GUI SQLite database.
- Show a small cautionary note on the Redump tab stating that credentials are
  stored in plaintext in the local SQLite database.
- Use saved credentials automatically for future Redump hash lookups.
- Keep anonymous lookup behavior when credentials are absent.
- Avoid letting old anonymous cache misses suppress authenticated lookups.

## Non-Goals

- Do not add OS keychain integration in this version.
- Do not encrypt credentials in SQLite in this version.
- Do not maintain a long-lived in-memory login session across app runs.
- Do not add a separate manual "refresh current disc" workflow in this version.

## UI Design

The top bar gains a third tab, `Redump`, next to `Inspect` and `Stats`.

The `Redump` tab contains a compact settings form:

- Username field.
- Password field.
- `Save credentials` button.
- `Test login` button.
- `Clear credentials` button.
- Status line showing one of:
  - `Not configured`
  - `Saved`
  - `Login OK`
  - `Login failed`
  - `Session expired`
- Small muted caution text:
  `Caution: Redump credentials are stored in plaintext in the local SQLite database.`

The tab should feel like the existing Stats/Inspect surfaces: dense, restrained,
and functional. It should not add a landing page or explanatory panel.

## Storage Design

Add a single-row credential table:

```sql
CREATE TABLE IF NOT EXISTS redump_auth (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    username     TEXT NOT NULL,
    password     TEXT NOT NULL,
    updated_unix INTEGER NOT NULL
);
```

Add helpers:

- `redumpAuthGet(db) (redumpAuth, bool)`
- `redumpAuthPut(db, username, password string)`
- `redumpAuthClear(db)`

The password is intentionally stored in plaintext because that is the selected
product behavior for this version. The UI caution text must stay visible on the
Redump tab.

## Cache Design

Authenticated lookups must not reuse anonymous `miss` rows. The existing
`redump_cache` table is keyed only by `hash`, so it needs an auth dimension.

Use a migration-safe replacement table:

```sql
CREATE TABLE IF NOT EXISTS redump_cache_v2 (
    hash         TEXT NOT NULL,
    auth_mode    TEXT NOT NULL,
    state        TEXT NOT NULL,
    url          TEXT,
    title        TEXT,
    checked_unix INTEGER NOT NULL,
    PRIMARY KEY (hash, auth_mode)
);
```

At startup, copy existing rows from `redump_cache` into `redump_cache_v2` with
`auth_mode = 'anon'`. New code reads and writes `redump_cache_v2`.

Auth modes:

- `anon`: no saved credentials were used.
- `auth`: saved credentials were used.

When credentials exist, `lookup` first checks `auth` cache and performs an
authenticated network lookup on miss. When credentials do not exist, it checks
and writes `anon`.

## Network Design

Add a small Redump client that can:

1. Fetch `http://forum.redump.org/login/`.
2. Parse the `csrf_token` hidden input.
3. POST username/password to `http://forum.redump.org/login/`.
4. Confirm the response/session is logged in by detecting either the username
   or logout link.
5. Use the same cookie jar to request
   `http://redump.org/discs/quicksearch/<sha1>/`.

For v1, log in once per lookup batch. This keeps state simple and avoids stale
cookie/session storage. If login fails, each affected hash should get an `err`
entry so the UI shows `redump lookup failed` rather than `not on redump`.

## Data Flow

Existing flow:

```text
hashCueBins / inspect metadata -> lookup([]sha1) -> redump_cache -> redumpFetch -> redump_cache -> UI
```

New flow:

```text
hashCueBins / inspect metadata
  -> lookup([]sha1)
  -> redumpAuthGet
  -> redump_cache_v2(hash, auth_mode)
  -> redumpFetch(hash, optional auth)
  -> redump_cache_v2(hash, auth_mode)
  -> UI
```

The `redumpEntry` model can stay unchanged for the track rows. The auth mode is
only a cache/fetch concern.

## Error Handling

- Empty username or password: do not save; show a validation status on the tab.
- Login page fetch fails: `Test login` shows `Login failed`.
- CSRF token missing: `Test login` shows `Login failed`.
- Login rejected: `Test login` shows `Login failed`.
- Authenticated quicksearch request fails: cache `err` for `auth` mode.
- Clearing credentials does not delete existing cache rows. Future lookups use
  anonymous cache mode again.

## Testing

Add focused tests for:

- SQLite credential save/load/clear.
- Cache separation: same hash can have `anon` miss and `auth` found.
- Login CSRF parsing.
- Redump client login success/failure against local `httptest` servers.
- `lookup` uses `auth` cache mode when credentials exist.
- The Redump tab caution text is produced by the view helper.

Existing GUI tests should keep passing.
