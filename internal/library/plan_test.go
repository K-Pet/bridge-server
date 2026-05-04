package library

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanDestination_HappyPath(t *testing.T) {
	plan := PlanDestination(TrackTags{
		Title:       "Money",
		Artist:      "Pink Floyd",
		AlbumArtist: "Pink Floyd",
		Album:       "The Dark Side of the Moon",
		TrackNumber: 6,
		DiscNumber:  1,
		DiscTotal:   1,
		Extension:   ".flac",
	})

	want := filepath.Join("Imported", "Pink Floyd", "The Dark Side of the Moon", "06 - Money.flac")
	if plan.RelPath != want {
		t.Fatalf("RelPath = %q, want %q", plan.RelPath, want)
	}
	if plan.MissingArtist || plan.MissingAlbum || plan.MissingTitle || plan.MissingTrackNumber {
		t.Fatalf("unexpected Missing flags: %+v", plan)
	}
}

func TestPlanDestination_MultiDiscPrefix(t *testing.T) {
	plan := PlanDestination(TrackTags{
		Title:       "Birthday",
		AlbumArtist: "The Beatles",
		Album:       "The Beatles (White Album)",
		TrackNumber: 4,
		DiscNumber:  2,
		DiscTotal:   2,
		Extension:   ".mp3",
	})

	if !strings.Contains(plan.RelPath, "2-04 - Birthday.mp3") {
		t.Fatalf("expected disc-track prefix, got %q", plan.RelPath)
	}
}

func TestPlanDestination_AlbumArtistPreferredOverArtist(t *testing.T) {
	plan := PlanDestination(TrackTags{
		Title:       "Something",
		Artist:      "George Harrison",
		AlbumArtist: "The Beatles",
		Album:       "Abbey Road",
		TrackNumber: 2,
		Extension:   ".flac",
	})

	if !strings.HasPrefix(plan.RelPath, filepath.Join("Imported", "The Beatles")) {
		t.Fatalf("expected AlbumArtist to win, got %q", plan.RelPath)
	}
}

func TestPlanDestination_MissingTagsFallBack(t *testing.T) {
	plan := PlanDestination(TrackTags{Extension: ".m4a"})

	if !plan.MissingArtist || !plan.MissingAlbum || !plan.MissingTitle || !plan.MissingTrackNumber {
		t.Fatalf("expected all Missing flags set, got %+v", plan)
	}
	if !strings.Contains(plan.RelPath, "Unknown Artist") {
		t.Fatalf("expected Unknown Artist in path, got %q", plan.RelPath)
	}
	if !strings.Contains(plan.RelPath, "Unknown Album") {
		t.Fatalf("expected Unknown Album in path, got %q", plan.RelPath)
	}
	// No track number → no leading "NN - " prefix
	if strings.Contains(filepath.Base(plan.RelPath), " - ") {
		t.Fatalf("expected no track prefix when missing, got %q", filepath.Base(plan.RelPath))
	}
}

func TestPlanDestination_TitleFallbackKeepsExtension(t *testing.T) {
	plan := PlanDestination(TrackTags{
		Artist:      "Artist",
		Album:       "Album",
		TrackNumber: 3,
		Extension:   ".opus",
	})
	if !strings.HasSuffix(plan.RelPath, ".opus") {
		t.Fatalf("expected .opus extension, got %q", plan.RelPath)
	}
	if !plan.MissingTitle {
		t.Fatalf("expected MissingTitle=true")
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Hello":               "Hello",
		"  spaced  ":          "spaced",
		"AC/DC":               "AC_DC",
		"slash\\and:colon":    "slash_and_colon",
		"":                    "Unknown",
		"....":                "Unknown",
		"name<with>chars?*|":  "name_with_chars___",
		"name\x00with\x01ctrl": "name_with_ctrl",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitize_LongInputCapped(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := Sanitize(long)
	if len(got) != 200 {
		t.Errorf("Sanitize cap = %d, want 200", len(got))
	}
}

func TestIsSupportedAudio(t *testing.T) {
	supported := []string{".mp3", ".FLAC", ".m4a", ".OGG"}
	for _, ext := range supported {
		if !IsSupportedAudio(ext) {
			t.Errorf("IsSupportedAudio(%q) = false, want true", ext)
		}
	}
	unsupported := []string{".txt", ".jpg", "", ".mp4", ".pdf"}
	for _, ext := range unsupported {
		if IsSupportedAudio(ext) {
			t.Errorf("IsSupportedAudio(%q) = true, want false", ext)
		}
	}
}
