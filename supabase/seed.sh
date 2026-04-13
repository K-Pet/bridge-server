#!/usr/bin/env bash
# Upload seed audio files to local Supabase Storage and create a test user + purchase.
# Run after: supabase db reset (which applies migrations + seed.sql)
#
# Usage: ./supabase/seed.sh

set -euo pipefail

API_URL="http://127.0.0.1:54321"
SERVICE_KEY="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZS1kZW1vIiwicm9sZSI6InNlcnZpY2Vfcm9sZSIsImV4cCI6MTk4MzgxMjk5Nn0.EGIM96RAZx35lJzdJsyH-qQwv8Hdp7fsn3W0YpN81IU"
MUSIC_DIR="./data/music"

echo "=== Bridge Music Seed Script ==="

# --------------------------------------------------------------------------
# 1. Upload audio files to Supabase Storage (tracks bucket)
# --------------------------------------------------------------------------
upload_track() {
  local local_path="$1"
  local storage_path="$2"
  local mime="$3"

  if [ ! -f "$local_path" ]; then
    echo "  SKIP (file not found): $local_path"
    return
  fi

  echo "  Uploading: $storage_path"
  curl -s -o /dev/null -w "    -> HTTP %{http_code}\n" \
    -X POST "${API_URL}/storage/v1/object/tracks/${storage_path}" \
    -H "Authorization: Bearer ${SERVICE_KEY}" \
    -H "Content-Type: ${mime}" \
    --data-binary "@${local_path}"
}

echo ""
echo "Uploading tracks to storage..."

# Chance the Rapper - Star Line
upload_track \
  "${MUSIC_DIR}/Chance the Rapper/Star Line (2025)/01-star-side-intro.mp3" \
  "chance-the-rapper/star-line/01-star-side-intro.mp3" \
  "audio/mpeg"

upload_track \
  "${MUSIC_DIR}/Chance the Rapper/Star Line (2025)/02-ride-(featuring-do-or-die).mp3" \
  "chance-the-rapper/star-line/02-ride.mp3" \
  "audio/mpeg"

upload_track \
  "${MUSIC_DIR}/Chance the Rapper/Star Line (2025)/03-no-more-old-men-(featuring-jamila-woods).mp3" \
  "chance-the-rapper/star-line/03-no-more-old-men.mp3" \
  "audio/mpeg"

# J. Cole - The Fall-Off CD 1
upload_track \
  "${MUSIC_DIR}/J. Cole/The Fall-Off (2026)/CD 01/J. Cole - The Fall-Off - 01 - 29 Intro.flac" \
  "j-cole/the-fall-off/cd01/01-29-intro.flac" \
  "audio/flac"

upload_track \
  "${MUSIC_DIR}/J. Cole/The Fall-Off (2026)/CD 01/J. Cole - The Fall-Off - 02 - Two Six.flac" \
  "j-cole/the-fall-off/cd01/02-two-six.flac" \
  "audio/flac"

# J. Cole - The Fall-Off CD 2
upload_track \
  "${MUSIC_DIR}/J. Cole/The Fall-Off (2026)/CD 02/J. Cole - The Fall-Off - 01 - 39 Intro.flac" \
  "j-cole/the-fall-off/cd02/01-39-intro.flac" \
  "audio/flac"

# J. Cole - 4 Your Eyez Only
upload_track \
  "${MUSIC_DIR}/J. Cole/4 Your Eyez Only (2016)/J. Cole - 4 Your Eyez Only - 01 - For Whom the Bell Tolls.flac" \
  "j-cole/4-your-eyez-only/01-for-whom-the-bell-tolls.flac" \
  "audio/flac"

upload_track \
  "${MUSIC_DIR}/J. Cole/4 Your Eyez Only (2016)/J. Cole - 4 Your Eyez Only - 02 - Immortal.flac" \
  "j-cole/4-your-eyez-only/02-immortal.flac" \
  "audio/flac"

# --------------------------------------------------------------------------
# 2. Create a test auth user
# --------------------------------------------------------------------------
echo ""
echo "Creating test user (test@bridge.music / testpass123)..."

USER_RESP=$(curl -s -X POST "${API_URL}/auth/v1/admin/users" \
  -H "Authorization: Bearer ${SERVICE_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@bridge.music",
    "password": "testpass123",
    "email_confirm": true
  }')

USER_ID=$(echo "$USER_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

if [ -z "$USER_ID" ]; then
  echo "  User may already exist, trying to fetch..."
  USER_ID=$(curl -s "${API_URL}/auth/v1/admin/users" \
    -H "Authorization: Bearer ${SERVICE_KEY}" | \
    python3 -c "import sys,json; users=json.load(sys.stdin).get('users',[]); print(next((u['id'] for u in users if u.get('email')=='test@bridge.music'),''))" 2>/dev/null || echo "")
fi

if [ -z "$USER_ID" ]; then
  echo "  ERROR: Could not create or find test user"
  exit 1
fi

echo "  Test user ID: ${USER_ID}"

# --------------------------------------------------------------------------
# 3. Create a test purchase (simulates buying 4 Your Eyez Only album)
# --------------------------------------------------------------------------
echo ""
echo "Creating test purchase (4 Your Eyez Only album)..."

# Insert purchase
curl -s -o /dev/null -w "  Purchase insert: HTTP %{http_code}\n" \
  -X POST "${API_URL}/rest/v1/purchases" \
  -H "apikey: ${SERVICE_KEY}" \
  -H "Authorization: Bearer ${SERVICE_KEY}" \
  -H "Content-Type: application/json" \
  -H "Prefer: return=representation" \
  -d "[{
    \"id\": \"d1000000-0000-0000-0000-000000000001\",
    \"user_id\": \"${USER_ID}\",
    \"total_cents\": 999,
    \"payment_ref\": \"test-storekit-txn-001\",
    \"status\": \"pending\",
    \"server_id\": \"local-dev\"
  }]"

# Insert purchase items (whole album purchase)
curl -s -o /dev/null -w "  Purchase item insert: HTTP %{http_code}\n" \
  -X POST "${API_URL}/rest/v1/purchase_items" \
  -H "apikey: ${SERVICE_KEY}" \
  -H "Authorization: Bearer ${SERVICE_KEY}" \
  -H "Content-Type: application/json" \
  -d "[{
    \"purchase_id\": \"d1000000-0000-0000-0000-000000000001\",
    \"album_id\": \"b1000000-0000-0000-0000-000000000003\",
    \"price_cents\": 999
  }]"

# --------------------------------------------------------------------------
# 4. Verify
# --------------------------------------------------------------------------
echo ""
echo "Verifying seed data..."

for table in albums tracks purchases purchase_items; do
  count=$(curl -s "${API_URL}/rest/v1/${table}?select=id" \
    -H "apikey: ${SERVICE_KEY}" \
    -H "Authorization: Bearer ${SERVICE_KEY}" \
    -H "Prefer: count=exact" \
    -o /dev/null -w "%{http_code}" \
    -D - 2>/dev/null | grep -i content-range | sed 's/.*\///' | tr -d '\r')
  echo "  ${table}: ${count} rows"
done

echo ""
echo "=== Seed complete ==="
echo ""
echo "Test credentials:"
echo "  Email:    test@bridge.music"
echo "  Password: testpass123"
echo ""
echo "Local Supabase:"
echo "  Studio:   http://127.0.0.1:54323"
echo "  API:      http://127.0.0.1:54321"
echo "  Mailpit:  http://127.0.0.1:54324"
