package sidecar

import (
	"testing"
	"time"

	"github.com/marcus/nightshift/internal/codexapp"
)

func TestClassifyCodexWindowsByDuration(t *testing.T) {
	five := &codexapp.Window{WindowDurationMins: 300}
	week := &codexapp.Window{WindowDurationMins: 10080}
	gotFive, gotWeek := classifyCodexWindows([]codexapp.Snapshot{{Primary: week, Secondary: five}})
	if gotFive != five || gotWeek != week {
		t.Fatalf("classified five=%p weekly=%p", gotFive, gotWeek)
	}
}

func TestParseResetTimeClock(t *testing.T) {
	location, _ := time.LoadLocation("Australia/Sydney")
	now := time.Date(2026, 9, 16, 19, 0, 0, 0, location)
	got, err := ParseResetTime("8:30pm (Australia/Sydney)", now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := time.Date(2026, 9, 16, 20, 30, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseResetTimeDated(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	got, err := ParseResetTime("02:50 on 8 Feb", now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Day() != 8 || got.Month() != time.February || got.Hour() != 2 || got.Minute() != 50 {
		t.Fatalf("unexpected parsed time: %v", got)
	}
}
