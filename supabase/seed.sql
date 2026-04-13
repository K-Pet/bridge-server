-- Bridge Music Marketplace Seed Data
-- Populates catalog with test albums and tracks (matching production schema shape)
-- Note: Actual audio files are uploaded separately via seed.sh

-- =============================================================================
-- ALBUMS
-- =============================================================================

-- Chance the Rapper - Star Line
insert into public.albums (id, title, artist, provider, release_date, price_cents) values
  ('b1000000-0000-0000-0000-000000000001',
   'Star Line', 'Chance the Rapper', 'bridge',
   '2025-01-01T00:00:00Z', 999);

-- J. Cole - The Fall-Off
insert into public.albums (id, title, artist, provider, release_date, price_cents) values
  ('b1000000-0000-0000-0000-000000000002',
   'The Fall-Off', 'J. Cole', 'bridge',
   '2026-01-01T00:00:00Z', 1499);

-- J. Cole - 4 Your Eyez Only
insert into public.albums (id, title, artist, provider, release_date, price_cents) values
  ('b1000000-0000-0000-0000-000000000003',
   '4 Your Eyez Only', 'J. Cole', 'bridge',
   '2016-12-09T00:00:00Z', 999);

-- =============================================================================
-- TRACKS
-- =============================================================================

-- Chance the Rapper - Star Line (disc 1)
insert into public.tracks (id, title, artist, stream_url, provider, album_index, disc_number, album_id, album_provider_id, storage_path, file_size_bytes, sha256, format, price_cents) values
  ('c1000000-0000-0000-0000-000000000001',
   'Star Side Intro', 'Chance the Rapper',
   'storage:tracks/chance-the-rapper/star-line/01-star-side-intro.mp3',
   'bridge', 1, 1,
   'b1000000-0000-0000-0000-000000000001',
   'b1000000-0000-0000-0000-000000000001',
   'chance-the-rapper/star-line/01-star-side-intro.mp3',
   10238911, 'd569aa1cac03170b75f7c326e5002cbb76e958c6a24e08300a872d8d8b8db795', 'mp3', 129),

  ('c1000000-0000-0000-0000-000000000002',
   'Ride (feat. Do or Die)', 'Chance the Rapper',
   'storage:tracks/chance-the-rapper/star-line/02-ride.mp3',
   'bridge', 2, 1,
   'b1000000-0000-0000-0000-000000000001',
   'b1000000-0000-0000-0000-000000000001',
   'chance-the-rapper/star-line/02-ride.mp3',
   8463641, '0d5e6cb5004b1fa577ac1a507bbd510edd97631c10716aa413fd0ee97a4681ac', 'mp3', 129),

  ('c1000000-0000-0000-0000-000000000003',
   'No More Old Men (feat. Jamila Woods)', 'Chance the Rapper',
   'storage:tracks/chance-the-rapper/star-line/03-no-more-old-men.mp3',
   'bridge', 3, 1,
   'b1000000-0000-0000-0000-000000000001',
   'b1000000-0000-0000-0000-000000000001',
   'chance-the-rapper/star-line/03-no-more-old-men.mp3',
   13011051, '6d6849f650a0d488aee4da78bd49c965ae13e6ac34ceafd0f1827be67017b589', 'mp3', 129);

-- J. Cole - The Fall-Off, CD 1
insert into public.tracks (id, title, artist, stream_url, provider, album_index, disc_number, album_id, album_provider_id, storage_path, file_size_bytes, sha256, format, price_cents) values
  ('c1000000-0000-0000-0000-000000000010',
   '29 Intro', 'J. Cole',
   'storage:tracks/j-cole/the-fall-off/cd01/01-29-intro.flac',
   'bridge', 1, 1,
   'b1000000-0000-0000-0000-000000000002',
   'b1000000-0000-0000-0000-000000000002',
   'j-cole/the-fall-off/cd01/01-29-intro.flac',
   16051922, 'f3a9fcf1b8459b29dbf44241bb6b461dbdd7b1079fe557031e8c0b0bc2687356', 'flac', 129),

  ('c1000000-0000-0000-0000-000000000011',
   'Two Six', 'J. Cole',
   'storage:tracks/j-cole/the-fall-off/cd01/02-two-six.flac',
   'bridge', 2, 1,
   'b1000000-0000-0000-0000-000000000002',
   'b1000000-0000-0000-0000-000000000002',
   'j-cole/the-fall-off/cd01/02-two-six.flac',
   77166629, 'be6d76f28fa80d96d1c197aaad4fad89c427075af9bd9252eade89b86e3e810d', 'flac', 129);

-- J. Cole - The Fall-Off, CD 2
insert into public.tracks (id, title, artist, stream_url, provider, album_index, disc_number, album_id, album_provider_id, storage_path, file_size_bytes, sha256, format, price_cents) values
  ('c1000000-0000-0000-0000-000000000020',
   '39 Intro', 'J. Cole',
   'storage:tracks/j-cole/the-fall-off/cd02/01-39-intro.flac',
   'bridge', 1, 2,
   'b1000000-0000-0000-0000-000000000002',
   'b1000000-0000-0000-0000-000000000002',
   'j-cole/the-fall-off/cd02/01-39-intro.flac',
   132265465, 'e9055d8f6b91ea38a45625153948b8ac57534b6ddb2b24796aebca036e676f8b', 'flac', 129);

-- J. Cole - 4 Your Eyez Only
insert into public.tracks (id, title, artist, stream_url, provider, album_index, disc_number, album_id, album_provider_id, storage_path, file_size_bytes, sha256, format, price_cents) values
  ('c1000000-0000-0000-0000-000000000030',
   'For Whom the Bell Tolls', 'J. Cole',
   'storage:tracks/j-cole/4-your-eyez-only/01-for-whom-the-bell-tolls.flac',
   'bridge', 1, 1,
   'b1000000-0000-0000-0000-000000000003',
   'b1000000-0000-0000-0000-000000000003',
   'j-cole/4-your-eyez-only/01-for-whom-the-bell-tolls.flac',
   15167273, '6fb1e8763b3f77c901fb869155b9cc02226fa81e1fa2f067c4023720bf87bebb', 'flac', 129),

  ('c1000000-0000-0000-0000-000000000031',
   'Immortal', 'J. Cole',
   'storage:tracks/j-cole/4-your-eyez-only/02-immortal.flac',
   'bridge', 2, 1,
   'b1000000-0000-0000-0000-000000000003',
   'b1000000-0000-0000-0000-000000000003',
   'j-cole/4-your-eyez-only/02-immortal.flac',
   19914035, 'ced1b6aee3395d32bf045fdd3bf89bf891bfea0b5dec883dea91242cca433275', 'flac', 129);

-- =============================================================================
-- TEST USER PURCHASE (will be created by seed script after auth user exists)
-- =============================================================================
-- The seed script (seed.sh) creates an auth user and then inserts a purchase
-- to simulate the full flow. This SQL only handles catalog data.
