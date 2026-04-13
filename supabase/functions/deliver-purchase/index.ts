// deliver-purchase: Generates signed download URLs for purchased tracks and
// sends the webhook payload to the buyer's bridge-server.
//
// Invoke: POST /functions/v1/deliver-purchase  { "purchase_id": "uuid" }
//
// Env vars (injected by Supabase runtime):
//   SUPABASE_URL, SUPABASE_SERVICE_ROLE_KEY  — automatic
//   BRIDGE_WEBHOOK_SECRET                    — HMAC key for X-Bridge-Signature
//   BRIDGE_WEBHOOK_URL                       — bridge-server webhook endpoint

import { createClient } from "jsr:@supabase/supabase-js@2";

const SIGNED_URL_EXPIRY = 3600; // 1 hour

Deno.serve(async (req: Request) => {
  if (req.method === "OPTIONS") {
    return new Response(null, {
      headers: {
        "Access-Control-Allow-Origin": "*",
        "Access-Control-Allow-Methods": "POST",
        "Access-Control-Allow-Headers": "Content-Type, Authorization",
      },
    });
  }

  if (req.method !== "POST") {
    return jsonResponse({ error: "method not allowed" }, 405);
  }

  // ── Parse input ──────────────────────────────────────────────────────
  let purchaseId: string;
  try {
    const body = await req.json();
    purchaseId = body.purchase_id;
  } catch {
    return jsonResponse({ error: "invalid JSON body" }, 400);
  }

  if (!purchaseId) {
    return jsonResponse({ error: "purchase_id is required" }, 400);
  }

  // ── Config ───────────────────────────────────────────────────────────
  const supabaseUrl = Deno.env.get("SUPABASE_URL")!;
  const serviceKey = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!;
  const webhookSecret = Deno.env.get("BRIDGE_WEBHOOK_SECRET");
  const webhookUrl = Deno.env.get("BRIDGE_WEBHOOK_URL");
  // In local dev, the Supabase JS client generates signed URLs with the
  // Docker-internal hostname (kong:8000). The bridge-server runs on the host,
  // so we rewrite these to the external Supabase URL.
  const publicUrl = Deno.env.get("BRIDGE_PUBLIC_SUPABASE_URL") || supabaseUrl;

  if (!webhookSecret || !webhookUrl) {
    return jsonResponse(
      { error: "BRIDGE_WEBHOOK_SECRET and BRIDGE_WEBHOOK_URL must be set" },
      500,
    );
  }

  const supabase = createClient(supabaseUrl, serviceKey);

  // ── Fetch tracks for this purchase ───────────────────────────────────
  const { data: tracks, error: fetchErr } = await supabase
    .from("purchase_tracks")
    .select("*")
    .eq("purchase_id", purchaseId);

  if (fetchErr) {
    console.error("purchase_tracks query failed:", fetchErr);
    return jsonResponse({ error: fetchErr.message }, 500);
  }

  if (!tracks || tracks.length === 0) {
    return jsonResponse({ error: "no tracks found for purchase" }, 404);
  }

  // ── Generate signed download URLs ────────────────────────────────────
  const trackPayloads = await Promise.all(
    tracks.map(async (t: Record<string, unknown>) => {
      const { data: signed, error: signErr } = await supabase.storage
        .from("tracks")
        .createSignedUrl(t.storage_path as string, SIGNED_URL_EXPIRY);

      if (signErr) {
        console.error(`signed URL failed for ${t.storage_path}:`, signErr);
      }

      // Rewrite internal Docker URL to external URL for host-side download
      let downloadUrl = signed?.signedUrl ?? "";
      if (publicUrl !== supabaseUrl && downloadUrl) {
        downloadUrl = downloadUrl.replace(supabaseUrl, publicUrl);
      }

      return {
        track_id: t.track_id,
        artist: t.artist,
        album: t.album,
        title: t.title,
        format: t.format,
        download_url: downloadUrl,
        size_bytes: t.size_bytes,
        sha256: t.sha256,
      };
    }),
  );

  // Abort if any track is missing a download URL
  const missing = trackPayloads.filter((t) => !t.download_url);
  if (missing.length > 0) {
    return jsonResponse(
      {
        error: "failed to generate signed URLs",
        missing: missing.map((t) => t.track_id),
      },
      500,
    );
  }

  // ── Build webhook payload (matches store.Purchase in Go) ─────────────
  const payload = {
    purchase_id: purchaseId,
    user_id: tracks[0].user_id as string,
    tracks: trackPayloads,
  };

  const body = JSON.stringify(payload);

  // ── HMAC-SHA256 signature ────────────────────────────────────────────
  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(webhookSecret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const sigBuf = await crypto.subtle.sign("HMAC", key, encoder.encode(body));
  const signature = Array.from(new Uint8Array(sigBuf))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");

  // ── Mark purchase as delivering ──────────────────────────────────────
  await supabase
    .from("purchases")
    .update({ status: "delivering" })
    .eq("id", purchaseId);

  // ── Send webhook to bridge-server ────────────────────────────────────
  let webhookResp: Response;
  try {
    webhookResp = await fetch(webhookUrl, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Bridge-Signature": signature,
      },
      body,
    });
  } catch (err) {
    console.error("webhook fetch failed:", err);
    await supabase
      .from("purchases")
      .update({ status: "failed" })
      .eq("id", purchaseId);
    return jsonResponse({ error: `webhook unreachable: ${err}` }, 502);
  }

  if (!webhookResp.ok) {
    const errText = await webhookResp.text();
    console.error("webhook rejected:", webhookResp.status, errText);
    await supabase
      .from("purchases")
      .update({ status: "failed" })
      .eq("id", purchaseId);
    return jsonResponse(
      { error: `webhook failed: ${webhookResp.status} ${errText}` },
      502,
    );
  }

  return jsonResponse({
    status: "delivered",
    tracks: trackPayloads.length,
  });
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
