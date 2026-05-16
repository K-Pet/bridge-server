// Package autotag identifies audio files against the MusicBrainz
// database via Chromaprint fingerprinting and the AcoustID web
// service. The flow is:
//
//  1. Run `fpcalc` (libchromaprint) on the file → (duration, fingerprint)
//  2. POST the pair to AcoustID → recording_id candidates with scores
//  3. GET each recording from MusicBrainz → canonical title/artist/album
//
// The caller (the HTTP handler) shows candidates to the user; nothing
// is written to disk by this package. Writing happens later via the
// regular tagwriter PUT endpoint with the user's selection.
//
// Rate-limit notes: MusicBrainz asks clients to stay under 1 req/sec
// per host. We respect that with a small per-process delay between
// recording fetches. AcoustID's free tier is 3 req/sec.
package autotag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Candidate is one possible identification for a track. Score is the
// AcoustID confidence (0–1); higher is a better match. Title/Artist/
// Album are the canonical values from MusicBrainz, suitable for the
// user to preview before applying via the tag edit endpoint.
type Candidate struct {
	Score        float64 `json:"score"`
	RecordingID  string  `json:"recording_id"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	AlbumArtist  string  `json:"album_artist,omitempty"`
	Album        string  `json:"album,omitempty"`
	Year         int     `json:"year,omitempty"`
	TrackNumber  int     `json:"track_number,omitempty"`
	DiscNumber   int     `json:"disc_number,omitempty"`
	MusicBrainzURL string `json:"musicbrainz_url,omitempty"`
}

// ErrFingerprinterMissing is returned when the fpcalc binary isn't on
// PATH. The handler should surface this as a 503 with a clear
// remediation message — operators install Chromaprint differently per
// distro, so the binary's absence is an environmental rather than a
// programming concern.
var ErrFingerprinterMissing = errors.New("fpcalc binary not found on PATH (install libchromaprint-tools)")

// ErrNoMatches signals that AcoustID returned no candidates with
// non-zero score. Distinct from a transport error so the handler can
// return 404 instead of 500 — the file just isn't in the database.
var ErrNoMatches = errors.New("no AcoustID matches for this fingerprint")

// Client wraps the per-request configuration for the autotag flow.
// The HTTP client is shared so connection reuse cuts MusicBrainz
// latency across multiple recording-detail fetches.
type Client struct {
	AcoustIDKey string
	UserAgent   string
	HTTP        *http.Client

	// musicbrainzDelay enforces the 1-req-sec courtesy rate limit
	// between MusicBrainz recording fetches inside a single Identify
	// call. Configurable so tests can drive it to zero.
	musicbrainzDelay time.Duration
}

// New builds a Client with sensible defaults. UserAgent is required
// by MusicBrainz's policy — they reject blank-UA requests. Pass a
// project-identifying string like "bridge-server/1.0".
func New(acoustIDKey, userAgent string) *Client {
	return &Client{
		AcoustIDKey:      acoustIDKey,
		UserAgent:        userAgent,
		HTTP:             &http.Client{Timeout: 30 * time.Second},
		musicbrainzDelay: 1100 * time.Millisecond,
	}
}

// Identify runs the full fingerprint → AcoustID → MusicBrainz
// pipeline against an audio file and returns up to maxCandidates
// match candidates ordered by AcoustID score (highest first). The
// returned slice is empty (and err is ErrNoMatches) when no match is
// found above the minimum score threshold.
func (c *Client) Identify(ctx context.Context, audioPath string, maxCandidates int) ([]Candidate, error) {
	if c.AcoustIDKey == "" {
		return nil, fmt.Errorf("AcoustID key not configured")
	}
	if maxCandidates <= 0 {
		maxCandidates = 5
	}

	duration, fingerprint, err := fingerprint(ctx, audioPath)
	if err != nil {
		return nil, err
	}

	acoustIDMatches, err := c.lookupAcoustID(ctx, duration, fingerprint)
	if err != nil {
		return nil, err
	}
	if len(acoustIDMatches) == 0 {
		return nil, ErrNoMatches
	}

	candidates := make([]Candidate, 0, maxCandidates)
	for i, m := range acoustIDMatches {
		if i >= maxCandidates {
			break
		}
		// Pick the first recording with a usable title. AcoustID
		// can return multiple recordings per match (same fingerprint
		// → multiple MBz recordings, e.g. compilation vs original
		// release); we pull metadata for the first one and let the
		// user pick a different release manually if needed.
		if len(m.Recordings) == 0 {
			continue
		}
		recordingID := m.Recordings[0].ID
		if recordingID == "" {
			continue
		}

		if i > 0 {
			// MusicBrainz courtesy delay between successive fetches.
			select {
			case <-ctx.Done():
				return candidates, ctx.Err()
			case <-time.After(c.musicbrainzDelay):
			}
		}

		details, err := c.lookupMusicBrainzRecording(ctx, recordingID)
		if err != nil {
			// One bad fetch shouldn't kill the whole identify call —
			// the caller still benefits from the remaining matches.
			continue
		}
		candidates = append(candidates, buildCandidate(m.Score, recordingID, details))
	}

	if len(candidates) == 0 {
		return nil, ErrNoMatches
	}
	return candidates, nil
}

// fingerprint runs fpcalc and parses its JSON output. fpcalc is the
// reference Chromaprint CLI; bundling its library directly via cgo
// would be the alternative but adds a build-time C dependency we
// don't want at the moment.
func fingerprint(ctx context.Context, audioPath string) (durationSec int, fp string, err error) {
	cmd := exec.CommandContext(ctx, "fpcalc", "-json", audioPath)
	out, err := cmd.Output()
	if err != nil {
		if execErr, ok := err.(*exec.Error); ok && errors.Is(execErr.Err, exec.ErrNotFound) {
			return 0, "", ErrFingerprinterMissing
		}
		// Surface stderr if fpcalc exited non-zero (typically an
		// unreadable file or a format Chromaprint doesn't decode).
		if exitErr, ok := err.(*exec.ExitError); ok {
			return 0, "", fmt.Errorf("fpcalc failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return 0, "", fmt.Errorf("fpcalc: %w", err)
	}
	var parsed struct {
		Duration    float64 `json:"duration"`
		Fingerprint string  `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0, "", fmt.Errorf("parse fpcalc output: %w", err)
	}
	if parsed.Fingerprint == "" {
		return 0, "", fmt.Errorf("fpcalc returned empty fingerprint")
	}
	return int(parsed.Duration), parsed.Fingerprint, nil
}

// acoustIDMatch mirrors the relevant subset of AcoustID's /v2/lookup
// response. The full response has more fields (releases, artists at
// the AcoustID level) but we re-fetch from MusicBrainz for canonical
// values so we only need the recording link here.
type acoustIDMatch struct {
	Score      float64 `json:"score"`
	Recordings []struct {
		ID string `json:"id"`
	} `json:"recordings"`
}

func (c *Client) lookupAcoustID(ctx context.Context, duration int, fp string) ([]acoustIDMatch, error) {
	params := url.Values{
		"client":      {c.AcoustIDKey},
		"meta":        {"recordings"},
		"duration":    {fmt.Sprintf("%d", duration)},
		"fingerprint": {fp},
	}
	endpoint := "https://api.acoustid.org/v2/lookup?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acoustid request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("acoustid returned %d: %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Status  string          `json:"status"`
		Error   *struct{ Message string } `json:"error"`
		Results []acoustIDMatch `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode acoustid response: %w", err)
	}
	if envelope.Status != "ok" {
		msg := "unknown error"
		if envelope.Error != nil {
			msg = envelope.Error.Message
		}
		return nil, fmt.Errorf("acoustid status %q: %s", envelope.Status, msg)
	}
	return envelope.Results, nil
}

// musicBrainzRecording mirrors the slice of MBz's recording-lookup
// response that the candidate builder actually reads. The releases
// list lets us pick an album name and year; the artist-credit list
// lets us assemble the display artist (handles collaborations like
// "Artist A & Artist B").
type musicBrainzRecording struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	ArtistCredit  []struct {
		Name string `json:"name"`
	} `json:"artist-credit"`
	Releases []struct {
		Title  string `json:"title"`
		Date   string `json:"date"`
		Media  []struct {
			Position int `json:"position"`
			Track    []struct {
				Position int `json:"position"`
			} `json:"track"`
		} `json:"media"`
	} `json:"releases"`
}

func (c *Client) lookupMusicBrainzRecording(ctx context.Context, recordingID string) (*musicBrainzRecording, error) {
	endpoint := fmt.Sprintf(
		"https://musicbrainz.org/ws/2/recording/%s?inc=artist-credits+releases+media&fmt=json",
		url.PathEscape(recordingID),
	)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	// MusicBrainz REJECTS requests without a User-Agent. Send the
	// project identifier so they can blacklist us cleanly if we
	// misbehave — better than getting silently rate-limited.
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("musicbrainz returned %d: %s", resp.StatusCode, string(body))
	}

	var rec musicBrainzRecording
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, fmt.Errorf("decode musicbrainz: %w", err)
	}
	return &rec, nil
}

func buildCandidate(score float64, recordingID string, rec *musicBrainzRecording) Candidate {
	c := Candidate{
		Score:          score,
		RecordingID:    recordingID,
		Title:          rec.Title,
		MusicBrainzURL: "https://musicbrainz.org/recording/" + recordingID,
	}
	if len(rec.ArtistCredit) > 0 {
		names := make([]string, 0, len(rec.ArtistCredit))
		for _, a := range rec.ArtistCredit {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		c.Artist = strings.Join(names, ", ")
	}
	if len(rec.Releases) > 0 {
		r := rec.Releases[0]
		c.Album = r.Title
		if len(r.Date) >= 4 {
			// MBz dates can be "2024", "2024-05", or "2024-05-16".
			// First four chars are always the year when present.
			fmt.Sscanf(r.Date[:4], "%d", &c.Year)
		}
		// Track/disc position from the first medium that lists this
		// recording. MBz's structure is one Track per release-medium
		// occurrence, so the first medium is "good enough" for a
		// suggestion the user will eyeball anyway.
		if len(r.Media) > 0 {
			c.DiscNumber = r.Media[0].Position
			if len(r.Media[0].Track) > 0 {
				c.TrackNumber = r.Media[0].Track[0].Position
			}
		}
	}
	return c
}
