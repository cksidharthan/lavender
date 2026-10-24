// Package store reads and writes the folders and feeds of a user. Every method is scoped to the user it is
// given, a record of another user is reported as not found.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	MaxNameLength = 64
	maxTitleLen   = 255
	// maxItemsPerFetch bounds the articles stored from one fetch, a feed can be very long.
	maxItemsPerFetch = 200

	// MinRefreshMinutes and MaxRefreshMinutes bound the refresh interval a user can set on a feed.
	MinRefreshMinutes = 5
	MaxRefreshMinutes = 1440
)

var (
	ErrNotFound = errors.New("not found")
)

// InvalidError is a user input problem, its message is meant to be shown to the user.
type InvalidError struct{ Message string }

func (e InvalidError) Error() string { return e.Message }

type Feed struct {
	ID    string
	Title string
	// URL is the address of the feed itself.
	URL string
	// IconURL is the logo of the feed, or the favicon of its site.
	IconURL  string
	Settings FeedSettings
	// IconCandidates are the icons to try, in order: the logo of the feed, the logo without the slash some feeds
	// end it with, and the favicons of the sites. It is only set by FindFeed.
	IconCandidates []string
	// LastError is why the last fetch failed, empty when it worked. It is only set by FindFeed.
	LastError string
}

// Folder holds the feeds of a folder. The feeds that are in no folder are returned as a Folder without ID and name.
type Article struct {
	ID          string
	Title       string
	URL         string
	Summary     string // as in the feed, it can hold html
	Content     string // as in the feed, it can hold html
	Author      string
	ImageURL    string
	AudioURL    string
	PublishedAt time.Time // zero when the feed has no date
}

// FeedSettings are the settings of a feed that the user changes.
type FeedSettings struct {
	// RefreshMinutes is how often the feed is refreshed, 0 for the default of the application.
	RefreshMinutes int
	HideFromHome   bool
	// IconOverride is the address of an icon the user chose, empty to use the icon of the feed.
	IconOverride string
	// Paused feeds are not refreshed automatically.
	Paused bool
}

// FeedArticle is an article with the feed it is of. The feed has no LastError and no IconCandidates.
type FeedArticle struct {
	Article
	Feed Feed
}

type Folder struct {
	ID    string
	Name  string
	Feeds []Feed
}

type Store struct {
	app core.App
}

func New(app core.App) *Store {
	return &Store{app: app}
}

// Tree returns the folders of the user sorted by name, with the feeds that are in no folder last.
func (s *Store) Tree(userID string) ([]Folder, error) {
	folderRecords, err := s.app.FindAllRecords(db.FoldersCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return nil, fmt.Errorf("error finding folders: %w", err)
	}

	feedRecords, err := s.app.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return nil, fmt.Errorf("error finding feeds: %w", err)
	}

	slices.SortFunc(folderRecords, func(a, b *core.Record) int {
		return strings.Compare(strings.ToLower(a.GetString("name")), strings.ToLower(b.GetString("name")))
	})
	slices.SortFunc(feedRecords, func(a, b *core.Record) int {
		return strings.Compare(strings.ToLower(feedTitle(a)), strings.ToLower(feedTitle(b)))
	})

	byFolder := make(map[string][]Feed)
	for _, r := range feedRecords {
		folderID := r.GetString("folder")
		byFolder[folderID] = append(byFolder[folderID], Feed{ID: r.Id, Title: feedTitle(r), URL: r.GetString("feed_url"), IconURL: feedIcon(r), Settings: feedSettings(r)})
	}

	tree := make([]Folder, 0, len(folderRecords)+1)
	for _, r := range folderRecords {
		tree = append(tree, Folder{ID: r.Id, Name: r.GetString("name"), Feeds: byFolder[r.Id]})
	}
	if unfiled := byFolder[""]; len(unfiled) > 0 {
		tree = append(tree, Folder{Feeds: unfiled})
	}

	return tree, nil
}

func feedTitle(r *core.Record) string {
	if title := r.GetString("title"); title != "" {
		return title
	}

	return r.GetString("feed_url")
}

func feedSettings(r *core.Record) FeedSettings {
	return FeedSettings{
		RefreshMinutes: r.GetInt("refresh_minutes"),
		HideFromHome:   r.GetBool("hide_from_home"),
		IconOverride:   r.GetString("icon_override"),
		Paused:         !r.GetBool("is_active"),
	}
}

// UpdateFeedSettings saves the settings of a feed of the user.
func (s *Store) UpdateFeedSettings(userID, id string, settings FeedSettings) error {
	if settings.RefreshMinutes != 0 && (settings.RefreshMinutes < MinRefreshMinutes || settings.RefreshMinutes > MaxRefreshMinutes) {
		return InvalidError{Message: fmt.Sprintf("The refresh interval must be between %d and %d minutes.", MinRefreshMinutes, MaxRefreshMinutes)}
	}

	settings.IconOverride = strings.TrimSpace(settings.IconOverride)
	if settings.IconOverride != "" {
		if len(settings.IconOverride) > 512 {
			return InvalidError{Message: "The icon address is too long."}
		}
		if _, err := feedfetch.ParseURL(settings.IconOverride); err != nil {
			return InvalidError{Message: "The icon must be an http or https address."}
		}
	}

	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return err
	}

	r.Set("refresh_minutes", settings.RefreshMinutes)
	r.Set("hide_from_home", settings.HideFromHome)
	r.Set("icon_override", settings.IconOverride)
	r.Set("is_active", !settings.Paused)

	return s.save(r, "")
}

// feedIcon is the logo of the feed. Without one it is the favicon of the site of the feed, which the page falls
// back to a letter for when the site has none.
func feedIcon(r *core.Record) string {
	if icon := r.GetString("icon_override"); icon != "" {
		return icon
	}

	if icon := r.GetString("icon_url"); icon != "" {
		return icon
	}

	for _, field := range []string{"site_url", "feed_url"} {
		if u, err := url.Parse(r.GetString(field)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return u.Scheme + "://" + u.Host + "/favicon.ico"
		}
	}

	return ""
}

func iconCandidates(r *core.Record) []string {
	var candidates []string
	add := func(candidate string) {
		if candidate != "" && !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}

	add(r.GetString("icon_override"))

	if icon := r.GetString("icon_url"); icon != "" {
		add(icon)
		// reddit gives its logo as https://www.redditstatic.com/icon.png/, which only works without the slash.
		if trimmed := strings.TrimRight(icon, "/"); strings.Contains(path.Base(trimmed), ".") {
			add(trimmed)
		}
	}

	for _, field := range []string{"site_url", "feed_url"} {
		if u, err := url.Parse(r.GetString(field)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			add(u.Scheme + "://" + u.Host + "/favicon.ico")
		}
	}

	return candidates
}

// FindFeed returns the feed of the user.
func (s *Store) FindFeed(userID, id string) (Feed, error) {
	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return Feed{}, err
	}

	return Feed{ID: r.Id, Title: feedTitle(r), URL: r.GetString("feed_url"), IconURL: feedIcon(r), Settings: feedSettings(r), IconCandidates: iconCandidates(r), LastError: r.GetString("last_error")}, nil
}

// FeedIDs returns the ids of the feeds of the user.
func (s *Store) FeedIDs(userID string) ([]string, error) {
	records, err := s.app.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return nil, fmt.Errorf("error finding feeds: %w", err)
	}

	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.Id)
	}

	return ids, nil
}

func (s *Store) CreateFolder(userID, name string) error {
	name, err := validName(name)
	if err != nil {
		return err
	}

	collection, err := s.app.FindCollectionByNameOrId(db.FoldersCollection)
	if err != nil {
		return fmt.Errorf("error finding folders collection: %w", err)
	}

	r := core.NewRecord(collection)
	r.Set("user", userID)
	r.Set("name", name)

	return s.save(r, "A folder with this name already exists.")
}

func (s *Store) RenameFolder(userID, id, name string) error {
	name, err := validName(name)
	if err != nil {
		return err
	}

	r, err := s.owned(db.FoldersCollection, userID, id)
	if err != nil {
		return err
	}
	r.Set("name", name)

	return s.save(r, "A folder with this name already exists.")
}

// DeleteFolder deletes the folder, its feeds are kept and end up in no folder.
func (s *Store) DeleteFolder(userID, id string) error {
	r, err := s.owned(db.FoldersCollection, userID, id)
	if err != nil {
		return err
	}

	if err := s.app.Delete(r); err != nil {
		return fmt.Errorf("error deleting folder: %w", err)
	}

	return nil
}

// AddFeed adds the feed that was fetched from rawURL. An empty folderID adds it to no folder.
func (s *Store) AddFeed(userID, rawURL, folderID string, meta feedfetch.Meta) (Feed, error) {
	u, err := feedfetch.ParseURL(rawURL)
	if err != nil {
		return Feed{}, InvalidError{Message: err.Error()}
	}

	if folderID != "" {
		if _, err := s.owned(db.FoldersCollection, userID, folderID); err != nil {
			return Feed{}, InvalidError{Message: "The selected folder does not exist."}
		}
	}

	collection, err := s.app.FindCollectionByNameOrId(db.FeedsCollection)
	if err != nil {
		return Feed{}, fmt.Errorf("error finding feeds collection: %w", err)
	}

	title := meta.Title
	if title == "" {
		title = u.Hostname()
	}

	r := core.NewRecord(collection)
	r.Set("user", userID)
	r.Set("folder", folderID)
	r.Set("feed_url", u.String())
	r.Set("site_url", meta.SiteURL)
	r.Set("title", truncate(title, maxTitleLen))
	r.Set("description", meta.Description)
	r.Set("feed_type", meta.Type)
	r.Set("icon_url", truncate(meta.IconURL, 512))
	r.Set("is_active", true)
	// last_fetched_at stays empty, so the feed is due for the first fetch of its articles.

	if err := s.save(r, "You already follow this feed."); err != nil {
		return Feed{}, err
	}

	return Feed{ID: r.Id, Title: feedTitle(r)}, nil
}

func (s *Store) RenameFeed(userID, id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > maxTitleLen {
		return InvalidError{Message: fmt.Sprintf("The name must be between 1 and %d characters.", maxTitleLen)}
	}

	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return err
	}
	r.Set("title", title)

	return s.save(r, "")
}

// UpdateFeedURL changes the address of a feed of the user. The feed is fetched again from the new address, and the
// error of the last fetch is cleared.
func (s *Store) UpdateFeedURL(userID, id, rawURL string) error {
	u, err := feedfetch.ParseURL(strings.TrimSpace(rawURL))
	if err != nil {
		return InvalidError{Message: err.Error()}
	}
	if len(u.String()) > 512 {
		return InvalidError{Message: "The feed address is too long."}
	}

	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return err
	}

	if r.GetString("feed_url") == u.String() {
		return nil
	}

	r.Set("feed_url", u.String())
	r.Set("last_error", "")
	r.Set("last_fetched_at", "")

	return s.save(r, "You already follow this feed.")
}

func (s *Store) DeleteFeed(userID, id string) error {
	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return err
	}

	if err := s.app.Delete(r); err != nil {
		return fmt.Errorf("error deleting feed: %w", err)
	}

	return nil
}

// Articles returns a page of the articles of the feed of the user, the newest first, and whether there are more.
func (s *Store) Articles(userID, feedID string, limit, offset int) ([]Article, bool, error) {
	if _, err := s.owned(db.FeedsCollection, userID, feedID); err != nil {
		return nil, false, err
	}

	// one more than asked, to know if there is a next page.
	records, err := s.app.FindRecordsByFilter(db.ArticlesCollection, "feed = {:feed}", "-published_at,-created", limit+1, offset, dbx.Params{"feed": feedID})
	if err != nil {
		return nil, false, fmt.Errorf("error finding articles: %w", err)
	}

	hasMore := len(records) > limit
	records = records[:min(len(records), limit)]

	articles := make([]Article, 0, len(records))
	for _, r := range records {
		articles = append(articles, toArticle(r))
	}

	return articles, hasMore, nil
}

func toArticle(r *core.Record) Article {
	return Article{
		ID:          r.Id,
		Title:       r.GetString("title"),
		URL:         r.GetString("url"),
		Summary:     r.GetString("summary"),
		Content:     r.GetString("content"),
		Author:      r.GetString("author"),
		ImageURL:    r.GetString("image_url"),
		AudioURL:    r.GetString("audio_url"),
		PublishedAt: r.GetDateTime("published_at").Time(),
	}
}

// AllArticles returns a page of the articles of all the feeds of the user, the newest first, and whether there are more.
func (s *Store) AllArticles(userID string, limit, offset int) ([]FeedArticle, bool, error) {
	return s.homeArticles(userID, "", nil, limit, offset)
}

// RecentArticles returns a page of the articles of the feeds of the user that were published since the time, the
// newest first, and whether there are more. Like the home page it leaves out the feeds that are hidden from it.
func (s *Store) RecentArticles(userID string, since time.Time, limit, offset int) ([]FeedArticle, bool, error) {
	return s.homeArticles(userID, " && published_at >= {:since}", dbx.Params{"since": since.UTC().Format(types.DefaultDateLayout)}, limit, offset)
}

func (s *Store) homeArticles(userID, extraFilter string, extra dbx.Params, limit, offset int) ([]FeedArticle, bool, error) {
	params := dbx.Params{"user": userID}
	for k, v := range extra {
		params[k] = v
	}

	// one more than asked, to know if there is a next page.
	records, err := s.app.FindRecordsByFilter(db.ArticlesCollection, "feed.user = {:user} && feed.hide_from_home = false"+extraFilter, "-published_at,-created", limit+1, offset, params)
	if err != nil {
		return nil, false, fmt.Errorf("error finding articles: %w", err)
	}

	hasMore := len(records) > limit
	records = records[:min(len(records), limit)]

	if errs := s.app.ExpandRecords(records, []string{"feed"}, nil); len(errs) > 0 {
		return nil, false, fmt.Errorf("error finding the feeds of the articles: %v", errs)
	}

	return feedArticles(records), hasMore, nil
}

// feedArticles converts articles that have their feed expanded.
func feedArticles(records []*core.Record) []FeedArticle {
	articles := make([]FeedArticle, 0, len(records))
	for _, r := range records {
		feed := r.ExpandedOne("feed")
		if feed == nil {
			continue
		}

		articles = append(articles, FeedArticle{
			Article: toArticle(r),
			Feed:    Feed{ID: feed.Id, Title: feedTitle(feed), IconURL: feedIcon(feed)},
		})
	}

	return articles
}

// SavedFlags tell how a user saved an article.
type SavedFlags struct {
	Favourite bool
	ReadLater bool
}

// ToggleSaved saves the article of the user as the kind (db.SavedFavourite, db.SavedReadLater), or takes it back when
// it already is. It returns whether the article is saved now.
func (s *Store) ToggleSaved(userID, articleID, kind string) (bool, error) {
	if !slices.Contains(db.SavedKinds, kind) {
		return false, InvalidError{Message: "Unknown way to save an article."}
	}

	article, err := s.app.FindRecordById(db.ArticlesCollection, articleID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("error finding article: %w", err)
	}
	// an article is only the user's when its feed is.
	if _, err := s.owned(db.FeedsCollection, userID, article.GetString("feed")); err != nil {
		return false, err
	}

	existing, err := s.app.FindAllRecords(db.SavedArticlesCollection, dbx.HashExp{"user": userID, "article": articleID, "kind": kind})
	if err != nil {
		return false, fmt.Errorf("error finding saved article: %w", err)
	}
	if len(existing) > 0 {
		for _, r := range existing {
			if err := s.app.Delete(r); err != nil {
				return false, fmt.Errorf("error removing saved article: %w", err)
			}
		}

		return false, nil
	}

	collection, err := s.app.FindCollectionByNameOrId(db.SavedArticlesCollection)
	if err != nil {
		return false, fmt.Errorf("error finding saved articles collection: %w", err)
	}

	r := core.NewRecord(collection)
	r.Set("user", userID)
	r.Set("article", articleID)
	r.Set("kind", kind)

	if err := s.app.Save(r); err != nil {
		return false, fmt.Errorf("error saving article: %w", err)
	}

	return true, nil
}

// SaveAudioProgress stores how far the user is in the audio of an article. Zero removes it, as for an episode that is
// finished.
func (s *Store) SaveAudioProgress(userID, articleID string, seconds float64) error {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return InvalidError{Message: "Invalid position."}
	}

	article, err := s.app.FindRecordById(db.ArticlesCollection, articleID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("error finding article: %w", err)
	}
	// an article is only the user's when its feed is.
	if _, err := s.owned(db.FeedsCollection, userID, article.GetString("feed")); err != nil {
		return err
	}

	existing, err := s.app.FindAllRecords(db.AudioProgressCollection, dbx.HashExp{"user": userID, "article": articleID})
	if err != nil {
		return fmt.Errorf("error finding audio progress: %w", err)
	}
	if seconds == 0 {
		for _, r := range existing {
			if err := s.app.Delete(r); err != nil {
				return fmt.Errorf("error removing audio progress: %w", err)
			}
		}

		return nil
	}

	var r *core.Record
	if len(existing) > 0 {
		r = existing[0]
	} else {
		collection, err := s.app.FindCollectionByNameOrId(db.AudioProgressCollection)
		if err != nil {
			return fmt.Errorf("error finding audio progress collection: %w", err)
		}
		r = core.NewRecord(collection)
		r.Set("user", userID)
		r.Set("article", articleID)
	}
	r.Set("seconds", seconds)

	return s.save(r, "")
}

// AudioProgress returns how far the user is in the audio of an article, 0 for the start.
func (s *Store) AudioProgress(userID, articleID string) (float64, error) {
	existing, err := s.app.FindAllRecords(db.AudioProgressCollection, dbx.HashExp{"user": userID, "article": articleID})
	if err != nil {
		return 0, fmt.Errorf("error finding audio progress: %w", err)
	}
	if len(existing) == 0 {
		return 0, nil
	}

	return existing[0].GetFloat("seconds"), nil
}

// SavedState returns how the user saved each of the articles, by article id. Articles that are not saved are not in it.
func (s *Store) SavedState(userID string, articleIDs []string) (map[string]SavedFlags, error) {
	state := make(map[string]SavedFlags, len(articleIDs))
	if len(articleIDs) == 0 {
		return state, nil
	}

	ids := make([]any, 0, len(articleIDs))
	for _, id := range articleIDs {
		ids = append(ids, id)
	}

	records, err := s.app.FindAllRecords(db.SavedArticlesCollection, dbx.HashExp{"user": userID}, dbx.In("article", ids...))
	if err != nil {
		return nil, fmt.Errorf("error finding saved articles: %w", err)
	}

	for _, r := range records {
		flags := state[r.GetString("article")]
		switch r.GetString("kind") {
		case db.SavedFavourite:
			flags.Favourite = true
		case db.SavedReadLater:
			flags.ReadLater = true
		}
		state[r.GetString("article")] = flags
	}

	return state, nil
}

// SavedArticles returns a page of the articles the user saved as the kind, the last saved first, and whether there
// are more.
func (s *Store) SavedArticles(userID, kind string, limit, offset int) ([]FeedArticle, bool, error) {
	if !slices.Contains(db.SavedKinds, kind) {
		return nil, false, InvalidError{Message: "Unknown way to save an article."}
	}

	// one more than asked, to know if there is a next page.
	saved, err := s.app.FindRecordsByFilter(db.SavedArticlesCollection, "user = {:user} && kind = {:kind}", "-created", limit+1, offset, dbx.Params{"user": userID, "kind": kind})
	if err != nil {
		return nil, false, fmt.Errorf("error finding saved articles: %w", err)
	}

	hasMore := len(saved) > limit
	saved = saved[:min(len(saved), limit)]

	if errs := s.app.ExpandRecords(saved, []string{"article.feed"}, nil); len(errs) > 0 {
		return nil, false, fmt.Errorf("error finding the saved articles: %v", errs)
	}

	articles := make([]*core.Record, 0, len(saved))
	for _, r := range saved {
		if article := r.ExpandedOne("article"); article != nil {
			articles = append(articles, article)
		}
	}

	return feedArticles(articles), hasMore, nil
}

// Article returns one article of a feed of the user.
func (s *Store) Article(userID, feedID, articleID string) (Article, error) {
	if _, err := s.owned(db.FeedsCollection, userID, feedID); err != nil {
		return Article{}, err
	}

	r, err := s.app.FindRecordById(db.ArticlesCollection, articleID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.GetString("feed") != feedID) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("error finding article: %w", err)
	}

	return toArticle(r), nil
}

// ClearArticles deletes the stored articles of a feed of the user, with what the user saved of them. The feed stays, its
// articles are stored again by the next refresh. It returns how many articles were deleted.
func (s *Store) ClearArticles(userID, feedID string) (int, error) {
	if _, err := s.owned(db.FeedsCollection, userID, feedID); err != nil {
		return 0, err
	}

	deleted := 0

	err := s.app.RunInTransaction(func(tx core.App) error {
		// not through the records: PocketBase removes what is related to a record when the record is deleted, which is
		// a record at a time, and the articles of a feed can be many.
		if _, err := tx.DB().NewQuery("DELETE FROM saved_articles WHERE article IN (SELECT id FROM articles WHERE feed = {:feed})").Bind(dbx.Params{"feed": feedID}).Execute(); err != nil {
			return err
		}

		result, err := tx.DB().NewQuery("DELETE FROM articles WHERE feed = {:feed}").Bind(dbx.Params{"feed": feedID}).Execute()
		if err != nil {
			return err
		}

		count, err := result.RowsAffected()
		deleted = int(count)

		return err
	})
	if err != nil {
		return 0, fmt.Errorf("error deleting articles: %w", err)
	}

	return deleted, nil
}

// DueFeeds returns the ids of the active feeds that are due for a refresh at now, the ones that were never fetched
// first. A feed is due when it was not fetched for its own refresh interval, or the default interval of its user, or
// defaultInterval when neither is set.
func (s *Store) DueFeeds(now time.Time, defaultInterval time.Duration, limit int) ([]string, error) {
	// the shortest interval any feed can have, so the feeds that can not be due are not read
	shortest := min(defaultInterval, MinRefreshMinutes*time.Minute)

	records, err := s.app.FindRecordsByFilter(
		db.FeedsCollection,
		"is_active = true && (last_fetched_at = '' || last_fetched_at < {:before})",
		"last_fetched_at",
		limit,
		0,
		dbx.Params{"before": now.Add(-shortest).UTC().Format(types.DefaultDateLayout)},
	)
	if err != nil {
		return nil, fmt.Errorf("error finding due feeds: %w", err)
	}

	if errs := s.app.ExpandRecords(records, []string{"user"}, nil); len(errs) > 0 {
		return nil, fmt.Errorf("error finding the users of the feeds: %v", errs)
	}

	ids := make([]string, 0, len(records))
	for _, r := range records {
		// the interval of the feed, else the one of its user, else the one of the application
		interval := defaultInterval
		if user := r.ExpandedOne("user"); user != nil && user.GetInt("default_refresh_minutes") > 0 {
			interval = time.Duration(user.GetInt("default_refresh_minutes")) * time.Minute
		}
		if minutes := r.GetInt("refresh_minutes"); minutes > 0 {
			interval = time.Duration(minutes) * time.Minute
		}

		fetched := r.GetDateTime("last_fetched_at")
		if fetched.IsZero() || fetched.Time().Before(now.Add(-interval)) {
			ids = append(ids, r.Id)
		}
	}

	return ids, nil
}

// FeedURL returns the url of any feed, for the workers that fetch them.
func (s *Store) FeedURL(feedID string) (string, error) {
	r, err := s.app.FindRecordById(db.FeedsCollection, feedID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("error finding feed: %w", err)
	}

	return r.GetString("feed_url"), nil
}

// SaveFetch stores a fetched feed: its metadata and the articles that are not stored yet. It returns how many
// articles are new. The title of the feed is left alone, as the user can rename a feed.
func (s *Store) SaveFetch(feedID string, result feedfetch.Result) (int, error) {
	added := 0

	err := s.app.RunInTransaction(func(tx core.App) error {
		feed, err := tx.FindRecordById(db.FeedsCollection, feedID)
		if err != nil {
			return err
		}

		feed.Set("site_url", truncate(result.Meta.SiteURL, 512))
		feed.Set("description", result.Meta.Description)
		if result.Meta.IconURL != "" {
			feed.Set("icon_url", truncate(result.Meta.IconURL, 512))
		}
		feed.Set("last_fetched_at", time.Now().UTC())
		feed.Set("last_error", "")
		if err := tx.Save(feed); err != nil {
			return err
		}

		var known []struct {
			ID       string `db:"id"`
			GUID     string `db:"guid"`
			ImageURL string `db:"image_url"`
			AudioURL string `db:"audio_url"`
		}
		if err := tx.DB().Select("id", "guid", "image_url", "audio_url").From(db.ArticlesCollection).Where(dbx.HashExp{"feed": feedID}).All(&known); err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(known)+len(result.Items))
		stored := make(map[string]struct{ id, image, audio string }, len(known)) // by guid
		for _, k := range known {
			seen[k.GUID] = struct{}{}
			stored[k.GUID] = struct{ id, image, audio string }{k.ID, k.ImageURL, k.AudioURL}
		}

		collection, err := tx.FindCollectionByNameOrId(db.ArticlesCollection)
		if err != nil {
			return err
		}

		for _, item := range result.Items[:min(len(result.Items), maxItemsPerFetch)] {
			if _, ok := seen[item.GUID]; ok {
				// articles stored before the pictures were read, or before the best one was picked, get theirs.
				if known := stored[item.GUID]; item.ImageURL != "" && item.ImageURL != known.image {
					if _, err := tx.DB().Update(db.ArticlesCollection, dbx.Params{"image_url": truncate(item.ImageURL, 2048)}, dbx.HashExp{"id": known.id}).Execute(); err != nil {
						return err
					}
					known.image = item.ImageURL
					stored[item.GUID] = known
				}
				// the same for the audio of articles stored before it was read.
				if known := stored[item.GUID]; item.AudioURL != "" && item.AudioURL != known.audio {
					if _, err := tx.DB().Update(db.ArticlesCollection, dbx.Params{"audio_url": truncate(item.AudioURL, 2048)}, dbx.HashExp{"id": known.id}).Execute(); err != nil {
						return err
					}
					known.audio = item.AudioURL
					stored[item.GUID] = known
				}

				continue
			}
			seen[item.GUID] = struct{}{}

			a := core.NewRecord(collection)
			a.Set("feed", feedID)
			a.Set("guid", truncate(item.GUID, 1024))
			a.Set("title", truncate(item.Title, 1024))
			a.Set("url", truncate(item.URL, 2048))
			a.Set("summary", truncate(item.Summary, 4096))
			a.Set("content", item.Content)
			a.Set("author", truncate(item.Author, 255))
			a.Set("image_url", truncate(item.ImageURL, 2048))
			a.Set("audio_url", truncate(item.AudioURL, 2048))
			// an article without a date is dated when it is found, so it has a place in the lists that are by date.
			published := item.PublishedAt
			if published.IsZero() {
				published = time.Now().UTC()
			}
			a.Set("published_at", published)

			if err := tx.Save(a); err != nil {
				return err
			}
			added++
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("error saving the fetched feed: %w", err)
	}

	return added, nil
}

// SaveFetchError stores that fetching the feed failed. The fetch time is set too, so the feed is tried again
// after the usual interval, not on every check.
func (s *Store) SaveFetchError(feedID string, fetchErr error) error {
	r, err := s.app.FindRecordById(db.FeedsCollection, feedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("error finding feed: %w", err)
	}

	r.Set("last_fetched_at", time.Now().UTC())
	r.Set("last_error", truncate(fetchErr.Error(), 512))

	return s.save(r, "")
}

// owned finds the record, only when it belongs to the user.
func (s *Store) owned(collection, userID, id string) (*core.Record, error) {
	r, err := s.app.FindRecordById(collection, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.GetString("user") != userID) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error finding %s: %w", collection, err)
	}

	return r, nil
}

// save stores the record. A unique index violation is reported as an InvalidError with
// duplicateMessage.
func (s *Store) save(r *core.Record, duplicateMessage string) error {
	err := s.app.Save(r)
	if err == nil {
		return nil
	}

	if duplicateMessage != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return InvalidError{Message: duplicateMessage}
	}

	return fmt.Errorf("error saving %s: %w", r.Collection().Name, err)
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > MaxNameLength {
		return "", InvalidError{Message: fmt.Sprintf("The name must be between 1 and %d characters.", MaxNameLength)}
	}

	return name, nil
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}

	return s
}

// DefaultRefreshMinutes is the refresh interval the user wants for the feeds that have none of their own, 0 for the
// default of the application.
func (s *Store) DefaultRefreshMinutes(userID string) (int, error) {
	user, err := s.app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return 0, fmt.Errorf("error finding user: %w", err)
	}

	return user.GetInt("default_refresh_minutes"), nil
}

// SetDefaultRefreshMinutes sets the refresh interval of the user for the feeds that have none of their own, 0 for the
// default of the application.
func (s *Store) SetDefaultRefreshMinutes(userID string, minutes int) error {
	if minutes != 0 && (minutes < MinRefreshMinutes || minutes > MaxRefreshMinutes) {
		return InvalidError{Message: fmt.Sprintf("The refresh interval must be between %d and %d minutes.", MinRefreshMinutes, MaxRefreshMinutes)}
	}

	user, err := s.app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return fmt.Errorf("error finding user: %w", err)
	}
	user.Set("default_refresh_minutes", minutes)

	if err := s.app.Save(user); err != nil {
		return fmt.Errorf("error saving user: %w", err)
	}

	return nil
}

// FeedStat is what is stored about a feed of the user.
type FeedStat struct {
	ID          string
	Title       string
	URL         string // the address of the feed
	Folder      string // empty when the feed is in no folder
	IconURL     string
	Articles    int
	Images      int // articles that have a picture
	LastFetched time.Time
	// RefreshMinutes is how often the feed is refreshed: its own interval, the one of its user or the default of the
	// application.
	RefreshMinutes int
	Paused         bool
	Hidden         bool
	LastError      string
}

// Stats is what is stored for a user.
type Stats struct {
	Folders       int
	Feeds         int
	FeedsWithIcon int
	Articles      int
	Images        int
	Favourites    int
	ReadLater     int
	// DefaultRefreshMinutes is the interval of the user for the feeds that have none of their own, 0 for the default of
	// the application.
	DefaultRefreshMinutes int
	PerFeed               []FeedStat
}

// Stats counts what is stored for the user. The feeds are by title. defaultInterval is the refresh interval of the
// application.
func (s *Store) Stats(userID string, defaultInterval time.Duration) (Stats, error) {
	var stats Stats

	user, err := s.app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return stats, fmt.Errorf("error finding user: %w", err)
	}
	stats.DefaultRefreshMinutes = user.GetInt("default_refresh_minutes")

	userInterval := int(defaultInterval / time.Minute)
	if stats.DefaultRefreshMinutes > 0 {
		userInterval = stats.DefaultRefreshMinutes
	}

	folders, err := s.app.FindAllRecords(db.FoldersCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return stats, fmt.Errorf("error finding folders: %w", err)
	}
	folderNames := make(map[string]string, len(folders))
	for _, f := range folders {
		folderNames[f.Id] = f.GetString("name")
	}
	stats.Folders = len(folders)

	feeds, err := s.app.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return stats, fmt.Errorf("error finding feeds: %w", err)
	}

	var counts []struct {
		Feed     string `db:"feed"`
		Articles int    `db:"articles"`
		Images   int    `db:"images"`
	}
	err = s.app.DB().NewQuery(`
		SELECT feed, COUNT(*) AS articles, COALESCE(SUM(CASE WHEN image_url != '' THEN 1 ELSE 0 END), 0) AS images
		FROM articles
		WHERE feed IN (SELECT id FROM feeds WHERE user = {:user})
		GROUP BY feed`).Bind(dbx.Params{"user": userID}).All(&counts)
	if err != nil {
		return stats, fmt.Errorf("error counting articles: %w", err)
	}
	countOf := make(map[string]int, len(counts))
	imagesOf := make(map[string]int, len(counts))
	for _, c := range counts {
		countOf[c.Feed], imagesOf[c.Feed] = c.Articles, c.Images
		stats.Articles += c.Articles
		stats.Images += c.Images
	}

	stats.Feeds = len(feeds)
	for _, r := range feeds {
		stat := FeedStat{
			ID:             r.Id,
			Title:          feedTitle(r),
			URL:            r.GetString("feed_url"),
			Folder:         folderNames[r.GetString("folder")],
			IconURL:        feedIcon(r),
			Articles:       countOf[r.Id],
			Images:         imagesOf[r.Id],
			LastFetched:    r.GetDateTime("last_fetched_at").Time(),
			RefreshMinutes: userInterval,
			Paused:         !r.GetBool("is_active"),
			Hidden:         r.GetBool("hide_from_home"),
			LastError:      r.GetString("last_error"),
		}
		if minutes := r.GetInt("refresh_minutes"); minutes > 0 {
			stat.RefreshMinutes = minutes
		}
		if r.GetString("icon_override") != "" || r.GetString("icon_url") != "" {
			stats.FeedsWithIcon++
		}

		stats.PerFeed = append(stats.PerFeed, stat)
	}
	slices.SortFunc(stats.PerFeed, func(a, b FeedStat) int {
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})

	for kind, count := range map[string]*int{db.SavedFavourite: &stats.Favourites, db.SavedReadLater: &stats.ReadLater} {
		n, err := s.app.CountRecords(db.SavedArticlesCollection, dbx.HashExp{"user": userID, "kind": kind})
		if err != nil {
			return stats, fmt.Errorf("error counting saved articles: %w", err)
		}
		*count = int(n)
	}

	return stats, nil
}

const (
	maxEmailLength = 255
	// MinPasswordLength is the shortest password of an account.
	MinPasswordLength = 8
	maxPasswordLength = 72 // what the hashing of passwords takes
)

// UpdateProfile changes the name and the email of the user. The email is only changed when the current password is
// given and right, as the email is how the user signs in.
func (s *Store) UpdateProfile(userID, name, email, currentPassword string) error {
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 255 {
		return InvalidError{Message: "The name can have at most 255 characters."}
	}

	email = strings.TrimSpace(email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > maxEmailLength {
		return InvalidError{Message: "Please enter a valid email address."}
	}

	user, err := s.app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return fmt.Errorf("error finding user: %w", err)
	}

	if !strings.EqualFold(email, user.Email()) {
		if !user.ValidatePassword(currentPassword) {
			return InvalidError{Message: "Enter your current password to change the email."}
		}

		// the email of another account, in any case: the database tells a@x.com from A@x.com
		others, err := s.app.FindAllRecords(db.UsersCollection, dbx.NewExp("LOWER(email) = {:email} AND id != {:id}", dbx.Params{"email": strings.ToLower(email), "id": user.Id}))
		if err != nil {
			return fmt.Errorf("error finding users: %w", err)
		}
		if len(others) > 0 {
			return InvalidError{Message: "An account with this email already exists."}
		}

		user.SetEmail(email)
	}
	user.Set("name", name)

	return s.save(user, "An account with this email already exists.")
}

// FeedExport is a feed of the user as it is written to a file.
type FeedExport struct {
	Folder  string
	Title   string
	FeedURL string
	SiteURL string
	Type    string
}

// ExportFeeds returns the feeds of the user with the name of their folder, by folder and title. The feeds that are in
// no folder come last.
func (s *Store) ExportFeeds(userID string) ([]FeedExport, error) {
	tree, err := s.Tree(userID)
	if err != nil {
		return nil, err
	}

	feeds, err := s.app.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return nil, fmt.Errorf("error finding feeds: %w", err)
	}
	byID := make(map[string]*core.Record, len(feeds))
	for _, f := range feeds {
		byID[f.Id] = f
	}

	exports := make([]FeedExport, 0, len(feeds))
	for _, folder := range tree {
		for _, f := range folder.Feeds {
			r, ok := byID[f.ID]
			if !ok {
				continue
			}

			exports = append(exports, FeedExport{
				Folder:  folder.Name,
				Title:   feedTitle(r),
				FeedURL: r.GetString("feed_url"),
				SiteURL: r.GetString("site_url"),
				Type:    r.GetString("feed_type"),
			})
		}
	}

	return exports, nil
}

// FeedImport is a feed to add from a file, like an OPML file.
type FeedImport struct {
	Folder  string
	Title   string
	FeedURL string
	SiteURL string
	Type    string
}

// FeedsImported tells what ImportFeeds did.
type FeedsImported struct {
	Feeds    int // added
	Folders  int // created
	Existing int // feeds that were in the account already
	Invalid  int // feeds that were skipped, as their address is not an http or https one
}

// ImportFeeds adds the feeds to the account of the user, each in its folder, which is created when it is not there. A
// feed that is in the account already is left as it is. The articles of the new feeds are fetched by the workers. Either
// all of it is added, or nothing.
func (s *Store) ImportFeeds(userID string, feeds []FeedImport) (FeedsImported, error) {
	var result FeedsImported

	err := s.app.RunInTransaction(func(tx core.App) error {
		result = FeedsImported{}

		folderCollection, err := tx.FindCollectionByNameOrId(db.FoldersCollection)
		if err != nil {
			return err
		}
		feedCollection, err := tx.FindCollectionByNameOrId(db.FeedsCollection)
		if err != nil {
			return err
		}

		existingFolders, err := tx.FindAllRecords(db.FoldersCollection, dbx.HashExp{"user": userID})
		if err != nil {
			return err
		}
		folderIDs := make(map[string]string, len(existingFolders))
		for _, f := range existingFolders {
			folderIDs[f.GetString("name")] = f.Id
		}

		existingFeeds, err := tx.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(existingFeeds))
		for _, f := range existingFeeds {
			known[f.GetString("feed_url")] = true
		}

		for _, f := range feeds {
			feedURL, err := feedfetch.ParseURL(f.FeedURL)
			if err != nil {
				result.Invalid++
				continue
			}
			if known[feedURL.String()] {
				result.Existing++
				continue
			}

			folderID := ""
			if name := truncate(strings.TrimSpace(f.Folder), MaxNameLength); name != "" {
				id, ok := folderIDs[name]
				if !ok {
					r := core.NewRecord(folderCollection)
					r.Set("user", userID)
					r.Set("name", name)
					if err := tx.Save(r); err != nil {
						return err
					}

					id = r.Id
					folderIDs[name] = id
					result.Folders++
				}
				folderID = id
			}

			title := strings.TrimSpace(f.Title)
			if title == "" {
				title = feedURL.Hostname()
			}

			feedType := db.FeedTypeRSS
			if f.Type == db.FeedTypeAtom {
				feedType = db.FeedTypeAtom
			}

			r := core.NewRecord(feedCollection)
			r.Set("user", userID)
			r.Set("folder", folderID)
			r.Set("feed_url", feedURL.String())
			if site, err := feedfetch.ParseURL(f.SiteURL); err == nil && len(site.String()) <= 512 {
				r.Set("site_url", site.String())
			}
			r.Set("title", truncate(title, maxTitleLen))
			r.Set("feed_type", feedType)
			r.Set("is_active", true)
			// last_fetched_at stays empty, so the feed is due for its first fetch.
			if err := tx.Save(r); err != nil {
				return err
			}

			known[feedURL.String()] = true
			result.Feeds++
		}

		return nil
	})
	if err != nil {
		return FeedsImported{}, fmt.Errorf("error importing feeds: %w", err)
	}

	return result, nil
}

// ChangePassword sets a new password for the user, when the current password is right. PocketBase ends the sessions of an
// account when its password changes, so the caller has to start a new one for the user.
func (s *Store) ChangePassword(userID, currentPassword, newPassword string) error {
	user, err := s.app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return fmt.Errorf("error finding user: %w", err)
	}

	if !user.ValidatePassword(currentPassword) {
		return InvalidError{Message: "The current password is not right."}
	}

	switch {
	case len(newPassword) < MinPasswordLength:
		return InvalidError{Message: fmt.Sprintf("The new password must have at least %d characters.", MinPasswordLength)}
	case len(newPassword) > maxPasswordLength:
		return InvalidError{Message: fmt.Sprintf("The new password can have at most %d characters.", maxPasswordLength)}
	case newPassword == currentPassword:
		return InvalidError{Message: "The new password must be different from the current one."}
	}

	user.SetPassword(newPassword)

	return s.save(user, "")
}
