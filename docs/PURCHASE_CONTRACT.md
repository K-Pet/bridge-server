# Purchase Contract

Stable interface between:

- **Storefront** — React Native app (iOS tab + web) in a separate repo. Owns
  catalog browsing, cart, Stripe checkout, and writing purchase rows to Supabase.
- **Home server** — this repo (`bridge-server`). Owns delivery: receive
  purchase events, download tracks, trigger Navidrome scans, serve playback.

Both sides are loosely coupled via **Supabase** (source of truth) and a
signed HTTP webhook (or Supabase poll fallback).

Nothing outside the shapes documented here is part of the contract. If the
storefront needs a field we don't declare here, it lives in a non-shared table.

---

## 1. Identity

| Concept | Where | Notes |
|---|---|---|
| User | `auth.users.id` (UUID) | Same Supabase project as catalog |
| Home server | `purchases.server_id` (text) | Stable per physical install; see §5 |
| Track/Album | `tracks.id` / `albums.id` (UUID) | Catalog rows; storefront reads, server reads |

There is no separate "customer" table. Auth identity *is* purchase identity.

---

## 2. Database (owned by this contract)

These tables live in the shared Supabase project. Both the storefront and this
server read/write them through PostgREST or the Supabase client libs. Migration
ownership: **this repo** (under `supabase/migrations/`). The storefront repo
**must not** issue schema changes to these tables; it can only issue PRs here.

### `albums.price_cents int null`
`null` = not for sale. Added column on an otherwise pre-existing catalog table.

### `tracks` (catalog additions)
```
album_id        uuid references albums(id)   -- FK for expansion
storage_path    text                         -- path inside Supabase Storage 'tracks' bucket
file_size_bytes bigint                       -- for range checks + UI display
sha256          text                         -- integrity check during download
format          text                         -- 'flac' | 'mp3' | 'wav' | ...
price_cents     int                          -- null = only sold as part of album
```

### `purchases`
```
id          uuid pk
user_id     uuid not null references auth.users(id) on delete cascade
total_cents int not null
payment_ref text                 -- Stripe PaymentIntent id, or 'dev-test-<ts>' in dev
status      text not null check (status in
              ('pending', 'delivering', 'delivered', 'failed'))
server_id   text                 -- which home server should receive this (§5)
created_at  timestamptz not null default now()
updated_at  timestamptz not null default now()
```

**Status state machine** (owned by this repo — storefront only writes `pending`):

```
pending ──► delivering ──► delivered
             │
             └────────────► failed
```

Only the home server (or the `deliver-purchase` edge function on its behalf)
ever transitions out of `pending`.

### `purchase_items`
```
id           uuid pk
purchase_id  uuid not null references purchases(id) on delete cascade
track_id     uuid null references tracks(id)
album_id     uuid null references albums(id)
price_cents  int not null
-- constraint: exactly one of track_id / album_id is set
```

### `purchase_tracks` (view — for home server, read-only)

Flattens `purchases × purchase_items` into per-track rows. Expands
album-level purchases into one row per track in that album. Used by the edge
function and by poll-mode queries to drive downloads.

### RLS summary

- `purchases` / `purchase_items`: `user_id = auth.uid()` for select;
  **insert/update only via service role** (storefront does this through an
  authenticated edge function; home server only reads).
- `tracks` / `albums`: public select for catalog; no user writes.
- `tracks.storage_path` and the `tracks` storage bucket: private. Downloads
  happen via short-lived signed URLs issued only after an entitlement check.

---

## 3. Purchase lifecycle (storefront side)

The storefront repo's contract obligations in order:

1. **User taps Buy.** Create (or reuse) a Stripe PaymentIntent.
2. **Payment succeeds.** Storefront backend (an edge function or its own
   server) receives the Stripe webhook.
3. **Storefront inserts a row:**
   ```
   INSERT INTO purchases(user_id, total_cents, payment_ref, status, server_id)
   VALUES ($user, $cents, $stripe_payment_intent, 'pending', $server_id);
   INSERT INTO purchase_items(purchase_id, track_id|album_id, price_cents) ...;
   ```
   `server_id` is read from the user's profile/settings (§5).
4. **Storefront invokes** `POST /functions/v1/deliver-purchase` with
   `{ "purchase_id": "<uuid>" }` (or, in poll mode, does nothing and lets the
   server pick it up on its next tick).
5. **Storefront shows delivery progress** to the user by subscribing to
   Supabase realtime on the `purchases` row (`status` transitions).

After step 3 the storefront is done. It never writes a terminal status; it
never calls the home server directly.

---

## 4. Delivery (home server side)

Two modes, chosen per-install via `BRIDGE_DELIVERY_MODE`:

### 4a. Webhook mode

`deliver-purchase` (edge function, lives in `supabase/functions/deliver-purchase/`)
generates signed download URLs and POSTs this JSON to
`BRIDGE_WEBHOOK_URL` with header `X-Bridge-Signature: <hex-hmac-sha256>`:

```jsonc
{
  "purchase_id": "uuid",
  "user_id": "uuid",
  "tracks": [
    {
      "track_id": "uuid",
      "artist": "Artist",
      "album": "Album",
      "title": "Track",
      "format": "flac",
      "download_url": "https://<project>.supabase.co/storage/v1/object/sign/tracks/...",
      "size_bytes": 45000000,
      "sha256": "a1b2c3..."
    }
  ]
}
```

HMAC is computed over the **raw request body** using `BRIDGE_WEBHOOK_SECRET`
(shared between the edge function and the home server; see §6).

The home server MUST:
- Reject unsigned or mismatched-signature requests (`401`).
- Respond `2xx` as soon as the job is persisted; downloading happens async.
- Eventually patch `purchases.status` to `delivered` or `failed` via
  the service role.

### 4b. Poll mode

The home server polls Supabase every `BRIDGE_POLL_INTERVAL`:

```
GET /rest/v1/purchases
      ?server_id=eq.<this-server-id>
      &status=eq.pending
      &order=created_at
```

For each returned purchase:
1. Fetch its rows from the `purchase_tracks` view.
2. For each row, call `POST /storage/v1/object/sign/tracks/<path>`
   (service role) to mint a signed URL.
3. Flip the purchase to `delivering` (so the storefront UI advances).
4. Proceed through the same download pipeline as webhook mode.

Poll mode is required when the home server has no public URL (NAT, no tunnel).

---

## 5. `server_id` — how a home server claims its purchases

`server_id` on `purchases` tells the system **which home server should deliver
this purchase**. It must be stable per install and unique per user (a user can
legitimately own multiple home servers — work vs home — and the storefront
must let them pick which one a purchase goes to).

**Who owns `server_id`:** the storefront repo, but the value originates from
this server.

**Provisioning flow** (to be implemented — see M6):

1. User installs this server. On first boot it generates a random
   128-bit `server_id` (base32, lowercase) and stores it in
   `/data/bridge/server-id`. Displayed as a QR code / pairing code in the
   embedded frontend's Settings page.
2. User opens the RN/web storefront → Settings → Add a home server → scans QR
   or types the code.
3. Storefront writes a `user_home_servers(user_id, server_id, label, is_default)`
   row in Supabase (schema lives in the storefront repo for now; promote here
   if both sides need it).
4. At checkout, the storefront chooses the default `server_id` unless the user
   overrides.

Until this flow lands, both sides hardcode `server_id = "local-dev"` for
development.

---

## 6. Secrets and trust boundaries

| Secret | Who holds it | Purpose |
|---|---|---|
| Supabase anon key | Everyone | Client-side auth + RLS-gated reads |
| Supabase service role key | `deliver-purchase` edge function + home server | Bypass RLS to read purchases, mint signed URLs |
| `BRIDGE_WEBHOOK_SECRET` | `deliver-purchase` + home server | HMAC webhook integrity |
| `BRIDGE_SECRET` (optional) | Home server | Derive deterministic Navidrome admin password |
| Stripe secret key | Storefront backend only | Payment processing |

Neither the storefront nor this home server ever sees the other's secrets
directly. `BRIDGE_WEBHOOK_SECRET` is the shared secret, but it's shared between
the **edge function** and the **home server** — not between the storefront app
and the home server.

**Open: HMAC secret provisioning.** In M6 we need to decide how a freshly
installed home server obtains `BRIDGE_WEBHOOK_SECRET`. Options:
- Generated on install, written to an edge-function env var when the user
  links the server (requires admin API access).
- Per-server row in Supabase (secret hashed at rest), looked up by `server_id`
  inside the edge function.
- Same secret for all installs (simple but bad — a leak compromises everyone).

The second option (per-server secrets) is the target. Pending design.

---

## 7. Versioning

This doc is v1. Changes that break the webhook payload or column semantics
require a version bump and a migration plan for already-deployed home servers.
Additive changes (new nullable columns, new optional payload fields) do not.

---

## 8. What lives in the storefront repo (not here)

- Stripe integration (PaymentIntent creation, webhook handler).
- Cart / checkout UI.
- User-home-server pairing UI (§5).
- The storefront's own schema (e.g., `user_home_servers`, cart state).
- Subscription / recurring billing (not in scope for v1).

---

## 9. What lives here (not in the storefront repo)

- `purchases`, `purchase_items` schema + `purchase_tracks` view.
- `deliver-purchase` edge function (may migrate to the storefront repo later).
- The download/scan pipeline, the `/api/webhook/purchase` endpoint, and
  everything under `internal/`.
- The embedded dev-mode marketplace (to be feature-flagged off once the RN
  storefront ships).
