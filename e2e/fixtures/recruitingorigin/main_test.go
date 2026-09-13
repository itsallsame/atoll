package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseJourneyDay(t *testing.T) {
	for day := 0; day <= 6; day++ {
		got, err := parseJourneyDay("D" + string(rune('0'+day)))
		if err != nil || got != day {
			t.Fatalf("parse D%d = %d, %v", day, got, err)
		}
	}
	for _, raw := range []string{"", "D7", "1", "D01", "x1"} {
		if _, err := parseJourneyDay(raw); err == nil {
			t.Fatalf("parseJourneyDay(%q) succeeded", raw)
		}
	}
}

func TestDailyJourneySnapshotsPreserveIncrementalCases(t *testing.T) {
	wants := [][]string{
		{"a", "b", "c"},
		{"a", "b", "c"},
		{"d", "a", "b", "c"},
		{"c", "d", "a", "b"},
		{"e", "f", "c", "d", "a", "b"},
		{"g", "h", "i", "j", "k", "l", "m"},
		{"g", "h", "e", "f", "c", "d"},
	}
	for day, want := range wants {
		items := dailyJourneyItems(day)
		if len(items) != len(want) {
			t.Fatalf("D%d items=%d want=%d", day, len(items), len(want))
		}
		for index, key := range want {
			if items[index].id != key {
				t.Fatalf("D%d item %d=%s want=%s", day, index, items[index].id, key)
			}
			if index > 0 && items[index].activityDelta > items[index-1].activityDelta {
				t.Fatalf("D%d is not newest-first at %d", day, index)
			}
		}
	}
	if dailyJourneyItems(4)[0].activityDelta != dailyJourneyItems(4)[1].activityDelta {
		t.Fatal("D4 must split one equal-time frontier group across pages")
	}
	for _, item := range dailyJourneyItems(5)[:6] {
		if item.activityDelta <= dailyJourneyItems(4)[0].activityDelta {
			t.Fatal("D5 must miss the old D4 boundary for six pages")
		}
	}
}

func TestDailyJourneyListingResponsePagesAndDetailVersion(t *testing.T) {
	base := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	body, count := dailyJourneyListingResponse("jobs.test", 4, base, 1, 2)
	if count != 2 {
		t.Fatalf("page count=%d", count)
	}
	var page struct {
		Offset int `json:"offset"`
		Total  int `json:"totalFound"`
		Items  []struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Offset != 1 || page.Total != 6 || len(page.Items) != 2 || page.Items[0].ID != "f" || page.Items[1].ID != "c" {
		t.Fatalf("unexpected page: %+v", page)
	}
	d2Detail := dailyJourneyResponseBody("c", 2, 256)
	d3Detail := dailyJourneyResponseBody("c", 3, 256)
	if !json.Valid(d2Detail) || !json.Valid(d3Detail) {
		t.Fatal("journey Detail responses must be valid JSON")
	}
	if string(d2Detail) == string(d3Detail) {
		t.Fatal("D3 retopped historical job must expose changed detail content")
	}
	if len(dailyJourneyResponseBody("a", 1, 256)) != 256 {
		t.Fatal("journey response must honor the configured payload size")
	}
}
