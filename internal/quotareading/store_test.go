package quotareading

import (
	"testing"
	"time"
)

var storeTestNow = time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)

func sevenDayReading(shareLeft float64, learnedAt time.Time, source Source) Reading {
	return Reading{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: shareLeft, ResetAt: storeTestNow.Add(48 * time.Hour), LearnedAt: learnedAt, Source: source}
}

func TestStoreNewerReadingReplacesOlderWhateverTheSource(t *testing.T) {
	store := NewStore()
	store.Record("claude-a", sevenDayReading(0.9, storeTestNow, SourcePoll))
	accepted := store.Record("claude-a", sevenDayReading(0.6, storeTestNow.Add(time.Minute), SourceHeader))
	assertReadings(t, accepted, []Reading{sevenDayReading(0.6, storeTestNow.Add(time.Minute), SourceHeader)})

	assertReadings(t, store.Readings("claude-a", "claude-sonnet-4-5"), []Reading{sevenDayReading(0.6, storeTestNow.Add(time.Minute), SourceHeader)})
}

func TestStoreOlderReadingDoesNotReplaceNewer(t *testing.T) {
	store := NewStore()
	store.Record("claude-a", sevenDayReading(0.6, storeTestNow, SourceHeader))
	if accepted := store.Record("claude-a", sevenDayReading(0.9, storeTestNow.Add(-time.Minute), SourcePoll)); len(accepted) != 0 {
		t.Fatalf("accepted = %+v, want the older reading ignored", accepted)
	}

	assertReadings(t, store.Readings("claude-a", ""), []Reading{sevenDayReading(0.6, storeTestNow, SourceHeader)})
}

func TestStoreKeepsWindowsThatANewerReadingDoesNotMention(t *testing.T) {
	store := NewStore()
	opus := Reading{Window: "seven_day_opus", Kind: KindPerModel, Model: "claude-opus", Length: 7 * 24 * time.Hour, ShareLeft: 0.3, ResetAt: storeTestNow.Add(24 * time.Hour), LearnedAt: storeTestNow, Source: SourcePoll}
	store.Record("claude-a", opus, sevenDayReading(0.9, storeTestNow, SourcePoll))
	store.Record("claude-a", sevenDayReading(0.7, storeTestNow.Add(time.Minute), SourceHeader))

	assertReadings(t, store.Readings("claude-a", "claude-opus-4-5"), []Reading{sevenDayReading(0.7, storeTestNow.Add(time.Minute), SourceHeader), opus})
}

func TestStorePerModelWindowAppliesOnlyToItsModel(t *testing.T) {
	store := NewStore()
	opus := Reading{Window: "seven_day_opus", Kind: KindPerModel, Model: "claude-opus", Length: 7 * 24 * time.Hour, ShareLeft: 0.3, ResetAt: storeTestNow.Add(24 * time.Hour), LearnedAt: storeTestNow, Source: SourcePoll}
	store.Record("claude-a", opus, sevenDayReading(0.9, storeTestNow, SourcePoll))

	assertReadings(t, store.Readings("claude-a", "claude-opus-4-5-20251101"), []Reading{sevenDayReading(0.9, storeTestNow, SourcePoll), opus})
	assertReadings(t, store.Readings("claude-a", "claude-sonnet-4-5"), []Reading{sevenDayReading(0.9, storeTestNow, SourcePoll)})
	assertReadings(t, store.Readings("claude-a", "claude-opusx"), []Reading{sevenDayReading(0.9, storeTestNow, SourcePoll)})
}

func TestStoreReadingsAreKeptPerCredential(t *testing.T) {
	store := NewStore()
	store.Record("claude-a", sevenDayReading(0.9, storeTestNow, SourceHeader))

	if got := store.Readings("claude-b", ""); len(got) != 0 {
		t.Fatalf("Readings(claude-b) = %+v, want none", got)
	}
	snapshot := store.Snapshot()
	if len(snapshot) != 1 || len(snapshot["claude-a"]) != 1 {
		t.Fatalf("Snapshot() = %+v, want one credential with one window", snapshot)
	}
}

func TestStoreIgnoresReadingsWithoutCredentialOrWindow(t *testing.T) {
	store := NewStore()
	store.Record("", sevenDayReading(0.9, storeTestNow, SourceHeader))
	store.Record("claude-a", Reading{Kind: KindRanking, ShareLeft: 0.5, LearnedAt: storeTestNow})

	if snapshot := store.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("Snapshot() = %+v, want empty", snapshot)
	}
}
