package pages

// SettingsPage is what the settings page shows. The numbers are written as they are shown.
type SettingsPage struct {
	Name     string
	Email    string
	Initials string

	// DefaultRefresh is the value of the field of the default refresh interval, empty for the default of the server.
	DefaultRefresh string
	// ServerRefresh is the default refresh interval of the server, and EffectiveRefresh the one that is used for the
	// feeds that have none of their own, both in minutes.
	ServerRefresh    int
	EffectiveRefresh int

	TwoFactor TwoFactorState

	Tiles []StatTile
	Feeds []FeedRow
}

// StatTile is a number of the overview.
type StatTile struct {
	Label string
	Value string
	Hint  string
}

// FeedRow is a feed in the table of the feeds.
type FeedRow struct {
	ID          string
	Title       string
	URL         string
	Folder      string
	IconURL     string
	Articles    string
	Images      string
	Refresh     string
	LastFetched string
	Badges      []Badge
}

// Badge is a status of a feed.
type Badge struct {
	Label string
	// Tone is ok, warning or muted.
	Tone string
	// Title is the longer text, for a hover.
	Title string
}

func badgeClass(tone string) string {
	base := "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium "

	switch tone {
	case "ok":
		return base + "bg-emerald-50 text-emerald-700"
	case "warning":
		return base + "bg-red-50 text-red-700"
	default:
		return base + "bg-slate-100 text-slate-600"
	}
}

// currentPage is the value of aria-current of a link: page for the one that is open, and nothing for the others.
func currentPage(open bool) string {
	if open {
		return "page"
	}

	return "false"
}
