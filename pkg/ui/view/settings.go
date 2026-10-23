package view

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/cksidharthan/lavender/pkg/ui/components"
	"github.com/cksidharthan/lavender/pkg/ui/pages"
	"github.com/pocketbase/pocketbase/core"
)

// Storage is what the server keeps on disk, for the overview.
type Storage struct {
	Icons         int   // icons that are kept
	IconBytes     int64 // the room they take
	DatabaseBytes int64
}

// Settings builds the settings page. serverRefresh is the default refresh interval of the server.
func Settings(user *core.Record, stats store.Stats, serverRefresh time.Duration, storage Storage, now time.Time) pages.SettingsPage {
	name := DisplayName(user)
	server := int(serverRefresh / time.Minute)

	page := pages.SettingsPage{
		Name:             user.GetString("name"), // as it is stored, the field is edited
		Email:            user.Email(),
		Initials:         components.Initials(name),
		ServerRefresh:    server,
		EffectiveRefresh: server,
	}
	if stats.DefaultRefreshMinutes > 0 {
		page.DefaultRefresh = strconv.Itoa(stats.DefaultRefreshMinutes)
		page.EffectiveRefresh = stats.DefaultRefreshMinutes
	}

	page.Tiles = []pages.StatTile{
		{Label: "Feeds", Value: number(stats.Feeds), Hint: count(stats.FeedsWithIcon, "with an icon", "with an icon")},
		{Label: "Folders", Value: number(stats.Folders), Hint: "to sort the feeds"},
		{Label: "Articles", Value: number(stats.Articles), Hint: average(stats.Articles, stats.Feeds)},
		{Label: "Pictures", Value: number(stats.Images), Hint: share(stats.Images, stats.Articles)},
		{Label: "Favourites", Value: number(stats.Favourites), Hint: "marked with a star"},
		{Label: "Read later", Value: number(stats.ReadLater), Hint: "saved for later"},
		{Label: "Icons kept", Value: number(storage.Icons), Hint: fileSize(storage.IconBytes) + " on this server"},
		{Label: "Database", Value: fileSize(storage.DatabaseBytes), Hint: "of the whole server"},
	}

	for _, f := range stats.PerFeed {
		row := pages.FeedRow{
			ID:          f.ID,
			Title:       plainText(f.Title),
			Folder:      plainText(f.Folder),
			IconURL:     IconPath(f.ID, f.IconURL),
			Articles:    number(f.Articles),
			Images:      number(f.Images),
			Refresh:     minutes(f.RefreshMinutes),
			LastFetched: ago(f.LastFetched, now),
			Badges:      badges(f),
		}
		if f.IconURL == "" {
			row.IconURL = ""
		}
		page.Feeds = append(page.Feeds, row)
	}

	return page
}

func badges(f store.FeedStat) []pages.Badge {
	var badges []pages.Badge

	switch {
	case f.LastError != "":
		badges = append(badges, pages.Badge{Label: "Error", Tone: "warning", Title: f.LastError})
	case f.LastFetched.IsZero():
		badges = append(badges, pages.Badge{Label: "Waiting", Tone: "muted", Title: "Its articles are not fetched yet"})
	case !f.Paused:
		badges = append(badges, pages.Badge{Label: "Up to date", Tone: "ok", Title: "The last refresh worked"})
	}
	if f.Paused {
		badges = append(badges, pages.Badge{Label: "Paused", Tone: "muted", Title: "Not refreshed automatically"})
	}
	if f.Hidden {
		badges = append(badges, pages.Badge{Label: "Hidden", Tone: "muted", Title: "Not shown on the home page"})
	}

	return badges
}

// number writes a number with the thousands apart: 12,345.
func number(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}

	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}

	return string(out)
}

func count(n int, one, several string) string {
	if n == 1 {
		return "1 " + one
	}

	return number(n) + " " + several
}

func average(articles, feeds int) string {
	if feeds == 0 {
		return "in no feeds yet"
	}

	return fmt.Sprintf("%.0f per feed on average", float64(articles)/float64(feeds))
}

func share(part, whole int) string {
	if whole == 0 {
		return "no articles yet"
	}

	return fmt.Sprintf("%d%% of the articles", part*100/whole)
}

// fileSize writes a size: 1.5 MB.
func fileSize(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	size, exp := float64(n), 0
	for size >= unit && exp < 4 {
		size /= unit
		exp++
	}

	return fmt.Sprintf("%.1f %s", size, []string{"B", "KB", "MB", "GB", "TB"}[exp])
}

// minutes writes an interval: 10 minutes, 2 hours.
func minutes(n int) string {
	switch {
	case n%60 == 0 && n >= 60:
		hours := n / 60
		if hours == 1 {
			return "1 hour"
		}

		return fmt.Sprintf("%d hours", hours)
	case n == 1:
		return "1 minute"
	default:
		return fmt.Sprintf("%d minutes", n)
	}
}

// ago writes how long ago a time is: 5 minutes ago. Never for a time that is not set.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "Never"
	}

	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "Just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	default:
		return t.Format(dateLayout)
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}

	return fmt.Sprintf("%d %ss", n, unit)
}
