# Production Setup — Bridge Music Server

End-to-end runbook for spinning up a Bridge Music Server instance for
ecosystem testing (or a real production install). When you finish this
runbook you'll have:

- A Bridge Music Server reachable at a public URL.
- A user signed in to that server's frontend whose Supabase account is
  paired with the server (no manual code entry).
- A working delivery pipeline: buy in the marketplace → file lands in
  `/data/music` → Navidrome scans → playable from frontend & iOS app.
- The iOS app auto-connecting to the paired server the first time the
  user signs in to Supabase on a fresh device.

If anything in this runbook gets out of date, fix it here — this file
*is* the supported install path.

---

## 0. Architecture refresher

```
┌────────────────────────────────────────────────────────────────────┐
│                                                                    │
│   Supabase project (shared with the marketplace)                   │
│   ────────────────────────────────────────────                     │
│   • auth.users             — identity                              │
│   • public.purchases       — purchase rows                         │
│   • public.user_home_servers — (user_id) → (webhook_url, secret)   │
│   • Edge Functions:                                                │
│       - deliver-purchase   — fans purchase → home server webhook   │
│       - get-home-server    — read/delete the user's pairing        │
│                                                                    │
└──┬─────────────────────────────┬───────────────────────────────────┘
   │  webhook (HMAC-signed)      │  service-role REST (auto-pair)
   ▼                             ▼
┌──────────────────────────────────────────────────────────────────┐
│   Bridge Music Server (this repo)                                │
│   ─────────────────────────                                      │
│   :8080  bridge-server (frontend SPA + API + SSE + proxy)        │
│   :4533  navidrome (localhost only)                              │
│                                                                  │
│   Volumes:                                                       │
│     /data/music      ← downloads land here                       │
│     /data/navidrome  ← Navidrome database                        │
│     /data/bridge     ← sidecar state (Navidrome creds, queue)    │
└──────────────────────────────────────────────────────────────────┘
                ▲
                │ /rest/* + /api/*  (HTTPS)
                │
   ┌────────────┴────────────┐
   │  Bridge iOS app         │
   │  (Self Hosted card →    │
   │   Bridge Server URL)    │
   └─────────────────────────┘
```

Reading order before you start:

1. [`PROJECT.md`](../PROJECT.md) — full architecture
2. [`docs/PURCHASE_CONTRACT.md`](PURCHASE_CONTRACT.md) — table shapes
   and security model. Do not duplicate any of it here.

---

## 1. Prerequisites

| Item | Why | How to get it |
|------|-----|---------------|
| Docker (or Podman) ≥ 24 with Compose v2 | Single-container deploy | docker.com / `brew install docker` |
| A Linux/macOS host with ≥ 2 GB RAM, ≥ 50 GB disk | Music files live on disk | DigitalOcean, Hetzner, an old laptop, an existing NAS |
| A Supabase project the marketplace is already pointed at | Source of truth for users + purchases | Marketplace repo's `supabase/` is the schema |
| A way to expose port 8080 on a public HTTPS URL | Marketplace webhooks must reach the server | Real domain + reverse proxy, **or** a Cloudflare Tunnel for testing |

**Decision: webhook vs poll mode.** If you can't expose the server
publicly (locked-down ISP, no domain), set
`BRIDGE_DELIVERY_MODE=poll` — the server will pull purchases from
Supabase on a schedule. The user experience is identical except for
delivery latency (default 5 min).

---

## 2. Configure `.env`

Clone the repo on the deploy host, then:

```bash
cp .env.example .env
$EDITOR .env
```

You only need to set **four lines**:

| Var | What it is |
|-----|------------|
| `MUSIC_DIR` | Where your music lives on disk. Defaults to `./data/music` inside the repo if blank. |
| `BRIDGE_LABEL` | Friendly name shown in the marketplace UI ("Living Room", etc.). |
| `BRIDGE_EXTERNAL_URL` | The HTTPS URL you'll expose in step 3. |
| `BRIDGE_SUPABASE_SERVICE_KEY` | Operator-supplied (transitional — see callout below). |

Auto-managed (don't set unless you know why):

- **`BRIDGE_SERVER_ID`** and **`BRIDGE_WEBHOOK_SECRET`** mint themselves
  on first boot and persist to `/data/bridge/credentials.json` (mode
  0600). To rotate either, delete that file and restart — the
  marketplace re-pairs on the next user sign-in.
- **`BRIDGE_SUPABASE_URL`** and **`BRIDGE_SUPABASE_ANON_KEY`** are baked
  into the public image. Operators forking for a different Supabase
  project pass `--build-arg` at `docker build` time.
- **JWT verification** uses `${SUPABASE_URL}/auth/v1/user` — no shared
  HMAC secret. `BRIDGE_SUPABASE_JWT_SECRET` is no longer required.

> **Phase 2b will eliminate `SERVICE_KEY` too.** It's currently still
> required because bridge-server makes service-role writes (auto-pair,
> mark-delivered, signed storage URLs) via PostgREST. Phase 2b moves
> these behind Edge Functions authenticated by the auto-minted
> webhook_secret, after which the user-facing `.env` is just three
> lines.

> **Never commit `.env`.** `BRIDGE_SUPABASE_SERVICE_KEY` grants
> god-mode access to the Bridge Music Supabase project.

---

## 3. Expose the server publicly

The marketplace's `deliver-purchase` Edge Function calls
`${BRIDGE_EXTERNAL_URL}/api/webhook/purchase` from Supabase's edge
network. Pick **one** of the following.

### 3a. Real domain + reverse proxy (production)

1. Point an A record at the host (`music.example.com → 1.2.3.4`).
2. Terminate TLS in front of the container — Caddy is the simplest:

   ```caddyfile
   music.example.com {
     reverse_proxy 127.0.0.1:8080
   }
   ```

   nginx + Let's Encrypt also works fine.

3. Set `BRIDGE_EXTERNAL_URL=https://music.example.com` in `.env`.

### 3b. Cloudflare Tunnel (ecosystem testing)

No DNS, no port-forwarding, no firewall changes. Free for personal
use.

```bash
# One-time setup
brew install cloudflared           # or download from cloudflare.com
cloudflared tunnel login
cloudflared tunnel create bridge

# Each session
cloudflared tunnel run --url http://localhost:8080 bridge
```

Cloudflare prints a `https://*.trycloudflare.com` URL. Paste it into
`BRIDGE_EXTERNAL_URL` in `.env` and restart the container.

> **Heads-up for Tunnel URLs:** the auto-generated subdomain rotates
> every time you restart `cloudflared` unless you bind it to a Named
> Tunnel. Either use a Named Tunnel for stability, or expect to rerun
> auto-pair each time the URL changes.

---

## 4. Boot the stack

```bash
docker compose up -d
docker compose logs -f bridge-music
```

Wait for the log line:

```
bridge server starting port=8080
```

Sanity check from the deploy host:

```bash
curl -s http://localhost:8080/api/health
# → {"status":"ok"}

curl -s "$BRIDGE_EXTERNAL_URL/api/health"
# → {"status":"ok"}     ← if this fails, step 4 isn't done yet
```

---

## 5. First user — sign in & auto-pair

1. Open `$BRIDGE_EXTERNAL_URL` in a browser.
2. **Sign Up** with the email you use on the marketplace iOS app, or
   **Sign In** if the account already exists.
3. The onboarding wizard launches:
   - **Profile step**: pick a username (skipped if the account already
     has one set on the marketplace).
   - **Pair step**: the frontend auto-calls `POST /api/auto-pair` with
     the user's Supabase JWT. The server upserts a `user_home_servers`
     row containing this server's webhook URL + secret.
4. You'll see "Server linked" → "Continue to your music library."

Verify the pairing landed:

```sql
-- In Supabase Dashboard → SQL Editor (use the marketplace project)
select user_id, label, server_id, webhook_url, last_paired_at
from public.user_home_servers
where user_id = '<your-auth-user-id>';
```

You should see exactly one row with `webhook_url` matching
`$BRIDGE_EXTERNAL_URL/api/webhook/purchase`.

> **No "Pair step" appeared?** That means
> `auto_pair_available = false`. `BRIDGE_SERVER_ID` and
> `BRIDGE_WEBHOOK_SECRET` should auto-mint at first boot — confirm
> they did by inspecting `/data/bridge/credentials.json`. The other
> requirement is `BRIDGE_EXTERNAL_URL` — confirm it's reaching the
> container:
>
> ```bash
> docker compose exec bridge-music sh -c 'cat /data/bridge/credentials.json && env | grep BRIDGE_EXTERNAL_URL'
> ```

---

## 6. End-to-end purchase test

With the marketplace iOS app or web frontend (on the same Supabase
project, signed in as the same user):

1. Pick any album with `price_cents != null`. Buy it through Stripe
   (use Stripe test mode — `4242 4242 4242 4242`).
2. Watch the bridge-server logs:

   ```bash
   docker compose logs -f bridge-music
   ```

   You should see:
   ```
   purchase enqueued purchase_id=...
   downloading task=... bytes=...
   scanning navidrome
   complete task=...
   ```

3. From the bridge-server frontend (still in your browser), refresh
   the Library page — the album should appear.
4. Confirm files landed:

   ```bash
   ls /data/music/Bridge/<Artist>/<Album>/
   ```

If the purchase is stuck on `delivering` after 60 s, jump to
**Troubleshooting** below.

---

## 7. iOS app auto-connect

With the user paired in step 6, fresh-installing Bridge Music on iOS:

1. Sign in to Supabase (same account).
2. The app calls the marketplace's `get-home-server` Edge Function
   inside `AuthManager`'s sign-in handler. The pairing comes back
   pre-populated — no Self Hosted card interaction needed.
3. The `NavidromeClient` is configured in `.bridgeServer` mode
   pointing at `BRIDGE_EXTERNAL_URL`, and the Library tab starts
   showing the user's catalog.

If a previous device's bridge-server is paired and you want to switch:
sign in to the new server's frontend and complete onboarding — the
`user_home_servers` row is upserted (one row per user), and the iOS
app will pick up the new URL on its next sign-in (or on app foreground
if you've already signed in).

---

## 8. Hardening checklist (before sharing the URL)

- [ ] TLS in front of the container (step 4a or Tunnel — never expose
      port 8080 over plain HTTP).
- [ ] `.env` has 0600 permissions and is not in version control.
- [ ] Volumes (`./data/music`, `./data/navidrome`, `./data/bridge`) are
      backed up — `/data/bridge` in particular contains the Navidrome
      admin credentials (recoverable but disruptive to lose).
- [ ] Container restart policy is `unless-stopped` (already in
      `docker-compose.yml`).
- [ ] Supabase project's allowed redirect URLs include
      `$BRIDGE_EXTERNAL_URL` (otherwise email confirmation links from
      sign-up will land on the wrong host).
- [ ] If using Cloudflare Tunnel for ecosystem testing, the tunnel is
      a Named Tunnel — random `trycloudflare.com` subdomains rotate.

---

## 9. Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `auto_pair_available: false` in onboarding | One of `BRIDGE_EXTERNAL_URL` / `BRIDGE_SERVER_ID` / `BRIDGE_WEBHOOK_SECRET` is empty | `docker compose exec bridge-music env \| grep ^BRIDGE_` |
| Onboarding stuck on "Linking your server…" | `/api/auto-pair` is hitting Supabase but the service-key is wrong / JWT secret mismatch | Check container logs for `auto-pair failed`. Re-paste keys from Supabase Dashboard. |
| Purchase webhook never arrives | `BRIDGE_EXTERNAL_URL` not reachable from Supabase's edge network | `curl -X POST $BRIDGE_EXTERNAL_URL/api/webhook/purchase` from a public host — should respond with 401 (signature missing). If it times out, public ingress is broken. |
| Webhook arrives but is rejected with `unauthorized` | Webhook secret rotated after pairing (e.g. `/data/bridge/credentials.json` was deleted) | The marketplace is still signing with the old secret. Have the user re-run onboarding (auto-pair upserts the new secret) — or restore the old `credentials.json` from backup. |
| `webhook too old` | Host clock skew > 5 minutes from Supabase | `timedatectl` / `chronyd` — webhooks are timestamp-signed against a 5-min window. |
| Library page is empty after a successful download | Navidrome scan didn't trigger — check `/data/navidrome/navidrome.log` | Restart container. If persistent, file an issue with the scan log. |
| iOS auto-connect doesn't pick up the pairing | iOS app is still signed in with a stale session, or in `.navidromeDirect` mode | Settings → Self Hosted → Disconnect, then sign out and back in. |

---

## 10. Multi-user / team installs

A single bridge-server instance is single-user by design today
(`user_home_servers` rows are 1:1 with users; the frontend doesn't yet
distinguish whose library is being browsed). If two users sign in to
the same instance, the *second* one's auto-pair will overwrite the
first's row — the first user's purchases will start trying to deliver
to the first user's last-paired URL.

For shared households today: have everyone sign in with the same
Supabase account. Multi-user is on the roadmap (`PROJECT.md` →
"Future Considerations").

---

## 11. Updating

```bash
git pull
docker compose pull        # if using a registry image instead of build
docker compose up -d --build
```

Migrations to the bridge-server's *own* state (`/data/bridge/queue.db`)
are handled in-process at boot. Migrations to the *Supabase* schema
ship from the marketplace repo (`supabase/migrations/` and
`supabase/rollouts/` over there) — bridge-server only reads.
