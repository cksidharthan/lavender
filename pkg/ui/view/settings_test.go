package view

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNumbersAndSizes(t *testing.T) {
	for n, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 12345: "12,345", 1234567: "1,234,567"} {
		require.Equal(t, want, number(n))
	}

	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"} {
		require.Equal(t, want, fileSize(n))
	}

	for n, want := range map[int]string{5: "5 minutes", 1: "1 minute", 10: "10 minutes", 60: "1 hour", 120: "2 hours", 90: "90 minutes", 1440: "24 hours"} {
		require.Equal(t, want, minutes(n))
	}

	require.Equal(t, "no articles yet", share(0, 0))
	require.Equal(t, "50% of the articles", share(5, 10))
	require.Equal(t, "in no feeds yet", average(0, 0))
	require.Equal(t, "3 per feed on average", average(9, 3))
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	for d, want := range map[time.Duration]string{
		10 * time.Second: "Just now",
		time.Minute:      "1 minute ago",
		5 * time.Minute:  "5 minutes ago",
		time.Hour:        "1 hour ago",
		3 * time.Hour:    "3 hours ago",
		24 * time.Hour:   "1 day ago",
		72 * time.Hour:   "3 days ago",
	} {
		require.Equal(t, want, ago(now.Add(-d), now))
	}

	require.Equal(t, "Sep 1, 2026", ago(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), now), "a date after a month")
	require.Equal(t, "Never", ago(time.Time{}, now))
}
