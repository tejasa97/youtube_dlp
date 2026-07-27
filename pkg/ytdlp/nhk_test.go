package ytdlp

import (
	"testing"
)

func TestNHKFamilyRegistryRouting(t *testing.T) {
	registry := productRegistry()
	cases := []struct {
		raw  string
		name string
	}{
		{"https://www3.nhk.or.jp/nhkworld/en/shows/2049165/", "nhk_vod"},
		{"https://www3.nhk.or.jp/nhkworld/en/shows/sumo/", "nhk_vod_program"},
		{"https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000", "nhk_for_school_bangumi"},
		{"https://www.nhk.or.jp/school/rika/", "nhk_for_school_subject"},
		{"https://www.nhk.or.jp/school/rika/program-a/", "nhk_for_school_program_list"},
		{"https://www.nhk.or.jp/radio/player/?ch=r1", "nhk_radiru_live"},
		{"https://www.nhk.or.jp/radionews/", "nhk_radio_news_page"},
		{"https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01", "nhk_radiru"},
		{"https://www.youtube.com/watch?v=fixture0001", "youtube"},
	}
	for _, tc := range cases {
		selected, err := registry.Select(tc.raw)
		if err != nil || selected.Name() != tc.name {
			t.Fatalf("Select(%q) = %v (%v), want %q", tc.raw, selected, err, tc.name)
		}
	}
}

func TestNHKOptionsValidation(t *testing.T) {
	if err := validateRequestOptions(Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1",
		NHK: NHKOptions{RadiruArea: "tokyo"},
	}); err != nil {
		t.Fatalf("valid area rejected: %v", err)
	}
	if err := validateRequestOptions(Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1",
		NHK: NHKOptions{RadiruArea: "tokyo/../evil"},
	}); err == nil {
		t.Fatal("hostile area accepted")
	}
	if err := validateRequestOptions(Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1",
		NHK: NHKOptions{RadiruArea: "area with spaces"},
	}); err == nil {
		t.Fatal("spaced area accepted")
	}
}
