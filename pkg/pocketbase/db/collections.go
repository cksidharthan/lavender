package db

const (
	UsersCollection         = "users"
	FeedsCollection         = "feeds"
	FoldersCollection       = "folders"
	ArticlesCollection      = "articles"
	SavedArticlesCollection = "saved_articles"
)

// the kinds a user saves an article as.
const (
	SavedFavourite = "favourite"
	SavedReadLater = "read_later"
)

var SavedKinds = []string{SavedFavourite, SavedReadLater}
