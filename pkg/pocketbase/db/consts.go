package db

type FeedType = string

const (
	FeedTypeRSS     FeedType = "rss"
	FeedTypeAtom    FeedType = "atom"
	FeedTypeReddit  FeedType = "reddit"
	FeedTypeYoutube FeedType = "youtube"
	FeedTypePodcast FeedType = "podcast"
	FeedTypeGithub  FeedType = "github"
	FeedTypeOther   FeedType = "other"
)

// FeedTypes is the predefined list of allowed feed types. Add new sources here as they are
// supported; this slice is the single source of truth consumed by the feeds migration.
var FeedTypes = []string{
	FeedTypeRSS,
	FeedTypeAtom,
	FeedTypeReddit,
	FeedTypeYoutube,
	FeedTypePodcast,
	FeedTypeGithub,
	FeedTypeOther,
}
