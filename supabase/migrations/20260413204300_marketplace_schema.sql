-- Bridge Music Database Schema
-- Matches production Supabase tables + marketplace additions (purchases, purchase items)
-- Run via: supabase db reset

-- =============================================================================
-- CORE TABLES (matching production schema exactly)
-- =============================================================================

-- User profiles (extended auth)
create table public.user_profiles (
  id uuid not null,
  updated_at timestamptz,
  username text unique check (char_length(username) >= 3),
  full_name text,
  avatar_url text,
  constraint user_profiles_pkey primary key (id),
  constraint user_profiles_id_fkey foreign key (id) references auth.users(id)
);

-- Albums catalog
create table public.albums (
  id uuid not null default gen_random_uuid(),
  title text not null,
  artist text not null,
  cover_art_url text,
  provider text not null default 'bridge'::text,
  provider_link text,
  explicit boolean default false,
  library_id text default ''::text,
  catalog_id text default ''::text,
  release_date timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  -- Marketplace addition: price for full album purchase (cents USD), null = not for sale
  price_cents int,
  constraint albums_pkey primary key (id)
);

-- Tracks catalog
create table public.tracks (
  id uuid not null default gen_random_uuid(),
  title text not null,
  artist text not null,
  stream_url text not null,
  album_art_url text,
  provider text not null,
  track_duration double precision,
  provider_link text,
  explicit boolean default false,
  album_index integer,
  album_provider_id text,
  created_at timestamptz not null default timezone('utc'::text, now()),
  updated_at timestamptz not null default timezone('utc'::text, now()),
  disc_number integer,
  release_date timestamptz,
  unavailable boolean default false,
  -- Marketplace additions
  album_id uuid references public.albums(id),   -- FK for marketplace purchase expansion
  storage_path text,                             -- path in Supabase Storage tracks bucket
  file_size_bytes bigint,                        -- for download verification
  sha256 text,                                   -- for integrity verification
  format text,                                   -- audio format: flac, mp3, wav, etc.
  price_cents int,                               -- individual track price (cents USD)
  constraint tracks_pkey primary key (id)
);

-- Featured/editorial content
create table public.featured_content (
  id uuid not null default gen_random_uuid(),
  created_at timestamptz default now(),
  updated_at timestamptz default now(),
  title text not null,
  subtitle text,
  content_type text not null check (content_type = any (array['track'::text, 'playlist'::text, 'album'::text, 'user'::text])),
  content_id uuid not null,
  image_url text,
  gradient_colors jsonb,
  is_active boolean default true,
  display_order integer default 0,
  metadata jsonb,
  navigation_id text,
  provider text default 'bridge'::text,
  constraint featured_content_pkey primary key (id)
);

-- User playlists
create table public.playlists (
  id uuid not null,
  user_id uuid not null,
  title text not null,
  cover_art_path text,
  is_album boolean default false,
  explicit boolean default false,
  library_id text,
  catalog_id text,
  provider text,
  provider_link text,
  is_public boolean not null default false,
  shareable_id text unique,
  created_at timestamptz not null default timezone('utc'::text, now()),
  updated_at timestamptz not null default timezone('utc'::text, now()),
  deleted_at timestamptz,
  creator text,
  constraint playlists_pkey primary key (id),
  constraint playlists_user_id_fkey foreign key (user_id) references auth.users(id)
);

-- Playlist track items
create table public.playlist_items (
  id uuid not null,
  user_id uuid not null,
  playlist_id uuid not null,
  track_id uuid not null,
  ordering double precision not null,
  created_at timestamptz not null default timezone('utc'::text, now()),
  updated_at timestamptz not null default timezone('utc'::text, now()),
  deleted_at timestamptz,
  constraint playlist_items_pkey primary key (id),
  constraint playlist_items_user_id_fkey foreign key (user_id) references auth.users(id),
  constraint playlist_items_playlist_id_fkey foreign key (playlist_id) references public.playlists(id),
  constraint playlist_items_track_id_fkey foreign key (track_id) references public.tracks(id)
);

-- Track listen history / analytics
create table public.track_listens (
  id uuid not null default gen_random_uuid(),
  user_id uuid not null,
  track_id uuid not null,
  started_at timestamptz not null,
  ended_at timestamptz not null,
  listened_ms integer not null check (listened_ms >= 0),
  completed boolean not null default false,
  source text not null default 'unknown'::text,
  playlist_id uuid,
  device_id text,
  session_id uuid,
  created_at timestamptz not null default timezone('utc'::text, now()),
  constraint track_listens_pkey primary key (id),
  constraint track_listens_user_id_fkey foreign key (user_id) references auth.users(id),
  constraint track_listens_track_id_fkey foreign key (track_id) references public.tracks(id),
  constraint track_listens_playlist_id_fkey foreign key (playlist_id) references public.playlists(id)
);

-- User library: saved albums
create table public.user_library_albums (
  user_id uuid not null,
  album_id uuid not null,
  created_at timestamptz not null default now(),
  deleted_at timestamptz,
  constraint user_library_albums_pkey primary key (user_id, album_id),
  constraint user_library_albums_user_id_fkey foreign key (user_id) references auth.users(id),
  constraint user_library_albums_album_id_fkey foreign key (album_id) references public.albums(id)
);

-- User library: saved tracks
create table public.user_library_tracks (
  user_id uuid not null,
  track_id uuid not null,
  created_at timestamptz not null default timezone('utc'::text, now()),
  deleted_at timestamptz,
  constraint user_library_tracks_pkey primary key (user_id, track_id),
  constraint user_library_tracks_user_id_fkey foreign key (user_id) references auth.users(id),
  constraint user_library_tracks_track_id_fkey foreign key (track_id) references public.tracks(id)
);

-- =============================================================================
-- MARKETPLACE TABLES (new — to be added to production when shipping)
-- =============================================================================

-- A purchase represents a completed payment transaction
create table public.purchases (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  total_cents int not null,
  payment_ref text,
  status text not null default 'pending'
    check (status in ('pending', 'delivering', 'delivered', 'failed')),
  server_id text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- Individual items within a purchase (tracks or albums)
create table public.purchase_items (
  id uuid primary key default gen_random_uuid(),
  purchase_id uuid not null references public.purchases(id) on delete cascade,
  track_id uuid references public.tracks(id),
  album_id uuid references public.albums(id),
  price_cents int not null,
  created_at timestamptz not null default now(),
  check (
    (track_id is not null and album_id is null) or
    (track_id is null and album_id is not null)
  )
);

-- =============================================================================
-- INDEXES
-- =============================================================================

create index idx_tracks_album_id on public.tracks(album_id);
create index idx_purchases_user_id on public.purchases(user_id);
create index idx_purchases_status on public.purchases(status);
create index idx_purchases_server_id on public.purchases(server_id) where server_id is not null;
create index idx_purchase_items_purchase_id on public.purchase_items(purchase_id);

-- =============================================================================
-- VIEW: Flattened purchase → tracks (used by Edge Function / poll delivery)
-- =============================================================================

-- Each row is one track that needs to be delivered to a bridge-server.
-- Handles both single-track and album purchases (album expands to all tracks).
create view public.purchase_tracks as
select
  p.id as purchase_id,
  p.user_id,
  p.status as purchase_status,
  p.server_id,
  coalesce(t.id, at.id) as track_id,
  coalesce(t.artist, at.artist) as artist,
  coalesce(a1.title, a2.title) as album,
  coalesce(t.title, at.title) as title,
  coalesce(t.format, at.format) as format,
  coalesce(t.storage_path, at.storage_path) as storage_path,
  coalesce(t.file_size_bytes, at.file_size_bytes) as size_bytes,
  coalesce(t.sha256, at.sha256) as sha256,
  p.created_at
from public.purchases p
join public.purchase_items pi on pi.purchase_id = p.id
-- Single track purchase
left join public.tracks t on t.id = pi.track_id
left join public.albums a1 on a1.id = t.album_id
-- Album purchase: expand to all tracks in that album
left join public.tracks at on at.album_id = pi.album_id
left join public.albums a2 on a2.id = pi.album_id
where coalesce(t.id, at.id) is not null;

-- =============================================================================
-- ROW LEVEL SECURITY
-- =============================================================================

alter table public.user_profiles enable row level security;
alter table public.albums enable row level security;
alter table public.tracks enable row level security;
alter table public.featured_content enable row level security;
alter table public.playlists enable row level security;
alter table public.playlist_items enable row level security;
alter table public.track_listens enable row level security;
alter table public.user_library_albums enable row level security;
alter table public.user_library_tracks enable row level security;
alter table public.purchases enable row level security;
alter table public.purchase_items enable row level security;

-- Profiles: users can read all, update own
create policy "Profiles are publicly readable"
  on public.user_profiles for select using (true);
create policy "Users can update own profile"
  on public.user_profiles for update using (auth.uid() = id);
create policy "Users can insert own profile"
  on public.user_profiles for insert with check (auth.uid() = id);

-- Catalog is readable by everyone (anon + authenticated)
create policy "Albums are publicly readable"
  on public.albums for select using (true);
create policy "Tracks are publicly readable"
  on public.tracks for select using (true);
create policy "Featured content is publicly readable"
  on public.featured_content for select using (true);

-- Playlists: owner can CRUD, public playlists readable by all
create policy "Users can view own playlists"
  on public.playlists for select using (auth.uid() = user_id or is_public = true);
create policy "Users can insert own playlists"
  on public.playlists for insert with check (auth.uid() = user_id);
create policy "Users can update own playlists"
  on public.playlists for update using (auth.uid() = user_id);
create policy "Users can delete own playlists"
  on public.playlists for delete using (auth.uid() = user_id);

-- Playlist items: owner can CRUD
create policy "Users can view own playlist items"
  on public.playlist_items for select
  using (auth.uid() = user_id);
create policy "Users can insert own playlist items"
  on public.playlist_items for insert
  with check (auth.uid() = user_id);
create policy "Users can update own playlist items"
  on public.playlist_items for update
  using (auth.uid() = user_id);
create policy "Users can delete own playlist items"
  on public.playlist_items for delete
  using (auth.uid() = user_id);

-- Track listens: user-scoped
create policy "Users can view own listens"
  on public.track_listens for select using (auth.uid() = user_id);
create policy "Users can insert own listens"
  on public.track_listens for insert with check (auth.uid() = user_id);

-- User library: user-scoped
create policy "Users can view own library albums"
  on public.user_library_albums for select using (auth.uid() = user_id);
create policy "Users can insert own library albums"
  on public.user_library_albums for insert with check (auth.uid() = user_id);
create policy "Users can delete own library albums"
  on public.user_library_albums for delete using (auth.uid() = user_id);

create policy "Users can view own library tracks"
  on public.user_library_tracks for select using (auth.uid() = user_id);
create policy "Users can insert own library tracks"
  on public.user_library_tracks for insert with check (auth.uid() = user_id);
create policy "Users can delete own library tracks"
  on public.user_library_tracks for delete using (auth.uid() = user_id);

-- Purchases: users see own, service_role can insert/update
create policy "Users can view own purchases"
  on public.purchases for select
  using (auth.uid() = user_id);
create policy "Service role can insert purchases"
  on public.purchases for insert
  with check (true);
create policy "Service role can update purchases"
  on public.purchases for update
  using (true);

create policy "Users can view own purchase items"
  on public.purchase_items for select
  using (
    purchase_id in (
      select id from public.purchases where user_id = auth.uid()
    )
  );
create policy "Service role can insert purchase items"
  on public.purchase_items for insert
  with check (true);

-- =============================================================================
-- STORAGE POLICIES
-- =============================================================================

create policy "Artwork is publicly accessible"
  on storage.objects for select
  using (bucket_id = 'artwork');
create policy "Service role manages artwork"
  on storage.objects for insert
  with check (bucket_id = 'artwork');

create policy "Service role manages tracks"
  on storage.objects for insert
  with check (bucket_id = 'tracks');
create policy "Service role can read tracks"
  on storage.objects for select
  using (bucket_id = 'tracks');

-- =============================================================================
-- UPDATED_AT TRIGGER
-- =============================================================================

create or replace function public.set_updated_at()
returns trigger as $$
begin
  new.updated_at = now();
  return new;
end;
$$ language plpgsql;

create trigger set_albums_updated_at
  before update on public.albums
  for each row execute function public.set_updated_at();

create trigger set_tracks_updated_at
  before update on public.tracks
  for each row execute function public.set_updated_at();

create trigger set_purchases_updated_at
  before update on public.purchases
  for each row execute function public.set_updated_at();
