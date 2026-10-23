// Package backup exports what a user has to a zip file, and imports such a file into the account of a user, on
// this or on a fresh instance. Everything of the user is in it: folders, feeds with their settings, articles, what
// was saved and the downloaded icons. Nothing of other users, and no password.
package backup

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	appName = "lavender"
	// Version is the version of the format of the backup. A backup of a newer version is not imported.
	Version = 1

	manifestFile = "manifest.json"
	settingsFile = "settings.json"
	foldersFile  = "folders.json"
	feedsFile    = "feeds.json"
	articlesFile = "articles.ndjson"
	savedFile    = "saved.json"
	iconsDir     = "icons/"

	articleBatch = 500

	// the limits of an import, as the file comes from the user and can be made to be very big when it is unpacked
	maxFiles           = 20000
	maxSmallFileBytes  = 64 << 20
	maxArticlesBytes   = 1 << 30
	maxArticleLine     = 8 << 20
	maxIconFileBytes   = 2 << 20
	maxFolderName      = store.MaxNameLength
	maxFeedTitle       = 255
	maxArticlesPerFeed = 200000
)

// InvalidError is a file that can not be imported, its message is meant to be shown to the user.
type InvalidError struct{ Message string }

func (e InvalidError) Error() string { return e.Message }

type manifest struct {
	App        string         `json:"app"`
	Version    int            `json:"version"`
	ExportedAt time.Time      `json:"exported_at"`
	Counts     map[string]int `json:"counts"`
}

type settings struct {
	DefaultRefreshMinutes int `json:"default_refresh_minutes"`
}

type folderRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type feedRecord struct {
	ID             string     `json:"id"`
	FolderID       string     `json:"folder_id,omitempty"`
	FeedURL        string     `json:"feed_url"`
	SiteURL        string     `json:"site_url,omitempty"`
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	FeedType       string     `json:"feed_type"`
	IconURL        string     `json:"icon_url,omitempty"`
	IconOverride   string     `json:"icon_override,omitempty"`
	Language       string     `json:"language,omitempty"`
	Active         bool       `json:"is_active"`
	HideFromHome   bool       `json:"hide_from_home,omitempty"`
	RefreshMinutes int        `json:"refresh_minutes,omitempty"`
	LastFetchedAt  *time.Time `json:"last_fetched_at,omitempty"`
}

type articleRecord struct {
	ID          string     `json:"id"`
	FeedID      string     `json:"feed_id"`
	GUID        string     `json:"guid"`
	Title       string     `json:"title,omitempty"`
	URL         string     `json:"url,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Content     string     `json:"content,omitempty"`
	Author      string     `json:"author,omitempty"`
	ImageURL    string     `json:"image_url,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type savedRecord struct {
	ArticleID string `json:"article_id"`
	Kind      string `json:"kind"`
}

// Counts are what a backup holds, or what an import added.
type Counts struct {
	Folders  int
	Feeds    int
	Articles int
	Saved    int
	Icons    int
}

// Export writes the backup of the user as a zip file.
func Export(w io.Writer, app core.App, userID string, icons *iconcache.Cache) (Counts, error) {
	var counts Counts

	user, err := app.FindRecordById(db.UsersCollection, userID)
	if err != nil {
		return counts, fmt.Errorf("error finding user: %w", err)
	}
	folders, err := app.FindAllRecords(db.FoldersCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding folders: %w", err)
	}
	feeds, err := app.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding feeds: %w", err)
	}
	saved, err := app.FindAllRecords(db.SavedArticlesCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding saved articles: %w", err)
	}

	articleCount := 0
	for _, f := range feeds {
		n, err := app.CountRecords(db.ArticlesCollection, dbx.HashExp{"feed": f.Id})
		if err != nil {
			return counts, fmt.Errorf("error counting articles: %w", err)
		}
		articleCount += int(n)
	}

	zw := zip.NewWriter(w)
	counts = Counts{Folders: len(folders), Feeds: len(feeds), Articles: articleCount, Saved: len(saved)}

	if err := writeJSON(zw, manifestFile, manifest{
		App:        appName,
		Version:    Version,
		ExportedAt: time.Now().UTC(),
		Counts: map[string]int{
			"folders": counts.Folders, "feeds": counts.Feeds, "articles": counts.Articles, "saved": counts.Saved,
		},
	}); err != nil {
		return counts, err
	}

	if err := writeJSON(zw, settingsFile, settings{DefaultRefreshMinutes: user.GetInt("default_refresh_minutes")}); err != nil {
		return counts, err
	}

	folderRecords := make([]folderRecord, 0, len(folders))
	for _, f := range folders {
		folderRecords = append(folderRecords, folderRecord{ID: f.Id, Name: f.GetString("name")})
	}
	if err := writeJSON(zw, foldersFile, folderRecords); err != nil {
		return counts, err
	}

	feedRecords := make([]feedRecord, 0, len(feeds))
	var iconURLs []string
	for _, f := range feeds {
		feedRecords = append(feedRecords, feedRecord{
			ID:             f.Id,
			FolderID:       f.GetString("folder"),
			FeedURL:        f.GetString("feed_url"),
			SiteURL:        f.GetString("site_url"),
			Title:          f.GetString("title"),
			Description:    f.GetString("description"),
			FeedType:       f.GetString("feed_type"),
			IconURL:        f.GetString("icon_url"),
			IconOverride:   f.GetString("icon_override"),
			Language:       f.GetString("language"),
			Active:         f.GetBool("is_active"),
			HideFromHome:   f.GetBool("hide_from_home"),
			RefreshMinutes: f.GetInt("refresh_minutes"),
			LastFetchedAt:  timeOf(f.GetDateTime("last_fetched_at").Time()),
		})
		iconURLs = append(iconURLs, f.GetString("icon_override"), f.GetString("icon_url"), faviconOf(f.GetString("site_url")), faviconOf(f.GetString("feed_url")))
	}
	if err := writeJSON(zw, feedsFile, feedRecords); err != nil {
		return counts, err
	}

	if err := writeArticles(zw, app, feeds); err != nil {
		return counts, err
	}

	savedRecords := make([]savedRecord, 0, len(saved))
	for _, r := range saved {
		savedRecords = append(savedRecords, savedRecord{ArticleID: r.GetString("article"), Kind: r.GetString("kind")})
	}
	if err := writeJSON(zw, savedFile, savedRecords); err != nil {
		return counts, err
	}

	// the downloaded icons, so a fresh instance has them also when the site of a feed no longer does
	if icons != nil {
		for _, file := range icons.Files(nonEmpty(iconURLs)...) {
			if err := writeFile(zw, iconsDir+file.Name, file.Data); err != nil {
				return counts, err
			}
			if strings.HasSuffix(file.Name, ".img") {
				counts.Icons++
			}
		}
	}

	if err := zw.Close(); err != nil {
		return counts, fmt.Errorf("error finishing the backup: %w", err)
	}

	return counts, nil
}

// writeArticles writes the articles of the feeds, one line of json each, a batch at a time.
func writeArticles(zw *zip.Writer, app core.App, feeds []*core.Record) error {
	w, err := zw.Create(articlesFile)
	if err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}
	buffered := bufio.NewWriter(w)
	encoder := json.NewEncoder(buffered)

	for _, feed := range feeds {
		for offset := 0; ; offset += articleBatch {
			records, err := app.FindRecordsByFilter(db.ArticlesCollection, "feed = {:feed}", "created,id", articleBatch, offset, dbx.Params{"feed": feed.Id})
			if err != nil {
				return fmt.Errorf("error finding articles: %w", err)
			}

			for _, r := range records {
				if err := encoder.Encode(articleRecord{
					ID:          r.Id,
					FeedID:      feed.Id,
					GUID:        r.GetString("guid"),
					Title:       r.GetString("title"),
					URL:         r.GetString("url"),
					Summary:     r.GetString("summary"),
					Content:     r.GetString("content"),
					Author:      r.GetString("author"),
					ImageURL:    r.GetString("image_url"),
					PublishedAt: timeOf(r.GetDateTime("published_at").Time()),
				}); err != nil {
					return fmt.Errorf("error writing the backup: %w", err)
				}
			}

			if len(records) < articleBatch {
				break
			}
		}
	}

	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}

	return nil
}

func writeJSON(zw *zip.Writer, name string, v any) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}

	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}

	return nil
}

func writeFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("error writing the backup: %w", err)
	}

	return nil
}

func timeOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	t = t.UTC()

	return &t
}

func faviconOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return u.Scheme + "://" + u.Host + "/favicon.ico"
}

func nonEmpty(values []string) []string {
	out := values[:0:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}

	return out
}

// Import reads a backup into the account of the user. What is in the account stays: a folder with the same name, a
// feed with the same address and an article with the same id in the feed are not added again, so importing a file twice
// changes nothing the second time. Either everything of the file is imported, or nothing is.
func Import(zr *zip.Reader, app core.App, userID string, icons *iconcache.Cache) (Counts, error) {
	var counts Counts

	if len(zr.File) > maxFiles {
		return counts, InvalidError{Message: "The file has too many files in it to be a backup of Lavender."}
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	var m manifest
	if err := readJSON(files, manifestFile, maxSmallFileBytes, &m); err != nil {
		return counts, err
	}
	if m.App != appName {
		return counts, InvalidError{Message: "This is not a backup of Lavender."}
	}
	if m.Version < 1 || m.Version > Version {
		return counts, InvalidError{Message: fmt.Sprintf("This backup is of a newer version (%d) than this Lavender can import (%d).", m.Version, Version)}
	}

	var (
		prefs   settings
		folders []folderRecord
		feeds   []feedRecord
		saved   []savedRecord
	)
	for name, target := range map[string]any{settingsFile: &prefs, foldersFile: &folders, feedsFile: &feeds, savedFile: &saved} {
		if err := readJSON(files, name, maxSmallFileBytes, target); err != nil {
			return counts, err
		}
	}

	articlesEntry, ok := files[articlesFile]
	if !ok {
		return counts, InvalidError{Message: "The backup is incomplete: " + articlesFile + " is missing."}
	}

	err := app.RunInTransaction(func(tx core.App) error {
		var err error
		counts, err = importAll(tx, userID, prefs, folders, feeds, saved, articlesEntry)

		return err
	})
	if err != nil {
		return Counts{}, err
	}

	if icons != nil {
		counts.Icons = restoreIcons(zr, icons)
	}

	return counts, nil
}

func importAll(tx core.App, userID string, prefs settings, folders []folderRecord, feeds []feedRecord, saved []savedRecord, articlesEntry *zip.File) (Counts, error) {
	var counts Counts

	// the default interval of the user, only when the user has not set one
	if prefs.DefaultRefreshMinutes >= store.MinRefreshMinutes && prefs.DefaultRefreshMinutes <= store.MaxRefreshMinutes {
		user, err := tx.FindRecordById(db.UsersCollection, userID)
		if err != nil {
			return counts, fmt.Errorf("error finding user: %w", err)
		}
		if user.GetInt("default_refresh_minutes") == 0 {
			user.Set("default_refresh_minutes", prefs.DefaultRefreshMinutes)
			if err := tx.Save(user); err != nil {
				return counts, fmt.Errorf("error saving user: %w", err)
			}
		}
	}

	// folders, by the name
	folderCollection, err := tx.FindCollectionByNameOrId(db.FoldersCollection)
	if err != nil {
		return counts, fmt.Errorf("error finding folders collection: %w", err)
	}
	existingFolders, err := tx.FindAllRecords(db.FoldersCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding folders: %w", err)
	}
	folderByName := make(map[string]string, len(existingFolders))
	for _, f := range existingFolders {
		folderByName[f.GetString("name")] = f.Id
	}

	folderIDs := make(map[string]string, len(folders)) // the id in the backup -> the id here
	for _, f := range folders {
		name := strings.TrimSpace(f.Name)
		if name == "" || len([]rune(name)) > maxFolderName || f.ID == "" {
			continue
		}

		id, ok := folderByName[name]
		if !ok {
			r := core.NewRecord(folderCollection)
			r.Set("user", userID)
			r.Set("name", name)
			if err := tx.Save(r); err != nil {
				return counts, fmt.Errorf("error saving folder: %w", err)
			}

			id = r.Id
			folderByName[name] = id
			counts.Folders++
		}
		folderIDs[f.ID] = id
	}

	// feeds, by the address
	feedCollection, err := tx.FindCollectionByNameOrId(db.FeedsCollection)
	if err != nil {
		return counts, fmt.Errorf("error finding feeds collection: %w", err)
	}
	existingFeeds, err := tx.FindAllRecords(db.FeedsCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding feeds: %w", err)
	}
	feedByURL := make(map[string]string, len(existingFeeds))
	for _, f := range existingFeeds {
		feedByURL[f.GetString("feed_url")] = f.Id
	}

	feedIDs := make(map[string]string, len(feeds))
	for _, f := range feeds {
		feedURL, err := feedfetch.ParseURL(f.FeedURL)
		if err != nil || f.ID == "" {
			continue
		}

		id, ok := feedByURL[feedURL.String()]
		if !ok {
			r := core.NewRecord(feedCollection)
			r.Set("user", userID)
			r.Set("folder", folderIDs[f.FolderID])
			r.Set("feed_url", feedURL.String())
			r.Set("site_url", webURL(f.SiteURL, 512))
			r.Set("title", truncate(f.Title, maxFeedTitle))
			r.Set("description", f.Description)
			r.Set("feed_type", feedType(f.FeedType))
			r.Set("icon_url", webURL(f.IconURL, 512))
			r.Set("icon_override", webURL(f.IconOverride, 512))
			r.Set("language", truncate(f.Language, 16))
			r.Set("is_active", f.Active)
			r.Set("hide_from_home", f.HideFromHome)
			if f.RefreshMinutes >= store.MinRefreshMinutes && f.RefreshMinutes <= store.MaxRefreshMinutes {
				r.Set("refresh_minutes", f.RefreshMinutes)
			}
			if f.LastFetchedAt != nil {
				r.Set("last_fetched_at", f.LastFetchedAt.UTC())
			}
			if err := tx.Save(r); err != nil {
				return counts, fmt.Errorf("error saving feed: %w", err)
			}

			id = r.Id
			feedByURL[feedURL.String()] = id
			counts.Feeds++
		}
		feedIDs[f.ID] = id
	}

	// articles, by the id in the feed. The ones that are in the account are matched, so what was saved can be too.
	articleCollection, err := tx.FindCollectionByNameOrId(db.ArticlesCollection)
	if err != nil {
		return counts, fmt.Errorf("error finding articles collection: %w", err)
	}
	articleIDs := make(map[string]string) // the id in the backup -> the id here
	knownGUIDs := make(map[string]map[string]string)
	perFeed := make(map[string]int)

	rc, err := articlesEntry.Open()
	if err != nil {
		return counts, InvalidError{Message: "The backup can not be read."}
	}
	defer rc.Close()

	reader := bufio.NewReaderSize(io.LimitReader(rc, maxArticlesBytes+1), 1<<20)
	read := int64(0)
	for {
		line, err := reader.ReadBytes('\n')
		read += int64(len(line))
		if read > maxArticlesBytes {
			return counts, InvalidError{Message: "The articles in the backup are too big to import."}
		}
		if len(line) > maxArticleLine {
			return counts, InvalidError{Message: "The backup has an article that is too big."}
		}

		if len(strings.TrimSpace(string(line))) > 0 {
			var a articleRecord
			if json.Unmarshal(line, &a) != nil {
				return counts, InvalidError{Message: "The backup has an article that can not be read."}
			}

			feedID, ok := feedIDs[a.FeedID]
			if ok && a.GUID != "" {
				if perFeed[feedID]++; perFeed[feedID] > maxArticlesPerFeed {
					return counts, InvalidError{Message: "The backup has too many articles in a feed to import."}
				}

				guids, loaded := knownGUIDs[feedID]
				if !loaded {
					var guidsErr error
					guids, guidsErr = guidsOf(tx, feedID)
					if guidsErr != nil {
						return counts, guidsErr
					}
					knownGUIDs[feedID] = guids
				}

				if id, exists := guids[a.GUID]; exists {
					articleIDs[a.ID] = id
				} else {
					r := core.NewRecord(articleCollection)
					r.Set("feed", feedID)
					r.Set("guid", truncate(a.GUID, 1024))
					r.Set("title", truncate(a.Title, 1024))
					r.Set("url", webURL(a.URL, 2048))
					r.Set("summary", truncate(a.Summary, 4096))
					r.Set("content", a.Content)
					r.Set("author", truncate(a.Author, 255))
					r.Set("image_url", webURL(a.ImageURL, 2048))
					if a.PublishedAt != nil {
						r.Set("published_at", a.PublishedAt.UTC())
					}
					if err := tx.Save(r); err != nil {
						return counts, fmt.Errorf("error saving article: %w", err)
					}

					guids[a.GUID] = r.Id
					articleIDs[a.ID] = r.Id
					counts.Articles++
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return counts, InvalidError{Message: "The backup can not be read."}
		}
	}

	// what the user saved
	savedCollection, err := tx.FindCollectionByNameOrId(db.SavedArticlesCollection)
	if err != nil {
		return counts, fmt.Errorf("error finding saved articles collection: %w", err)
	}
	existingSaved, err := tx.FindAllRecords(db.SavedArticlesCollection, dbx.HashExp{"user": userID})
	if err != nil {
		return counts, fmt.Errorf("error finding saved articles: %w", err)
	}
	isSaved := make(map[[2]string]bool, len(existingSaved))
	for _, r := range existingSaved {
		isSaved[[2]string{r.GetString("article"), r.GetString("kind")}] = true
	}

	for _, s := range saved {
		articleID, ok := articleIDs[s.ArticleID]
		if !ok || (s.Kind != db.SavedFavourite && s.Kind != db.SavedReadLater) || isSaved[[2]string{articleID, s.Kind}] {
			continue
		}

		r := core.NewRecord(savedCollection)
		r.Set("user", userID)
		r.Set("article", articleID)
		r.Set("kind", s.Kind)
		if err := tx.Save(r); err != nil {
			return counts, fmt.Errorf("error saving saved article: %w", err)
		}

		isSaved[[2]string{articleID, s.Kind}] = true
		counts.Saved++
	}

	return counts, nil
}

func guidsOf(app core.App, feedID string) (map[string]string, error) {
	var rows []struct {
		ID   string `db:"id"`
		GUID string `db:"guid"`
	}
	if err := app.DB().Select("id", "guid").From(db.ArticlesCollection).Where(dbx.HashExp{"feed": feedID}).All(&rows); err != nil {
		return nil, fmt.Errorf("error finding articles: %w", err)
	}

	guids := make(map[string]string, len(rows))
	for _, r := range rows {
		guids[r.GUID] = r.ID
	}

	return guids, nil
}

// restoreIcons puts the icons of the backup in the icon cache, and returns how many were restored. An icon that is
// not valid is skipped, it is downloaded again when it is needed.
func restoreIcons(zr *zip.Reader, icons *iconcache.Cache) int {
	type pair struct{ meta, data []byte }
	found := make(map[string]*pair)

	for _, f := range zr.File {
		name, ok := strings.CutPrefix(f.Name, iconsDir)
		if !ok || !iconcache.IsFileName(name) || f.UncompressedSize64 > maxIconFileBytes {
			continue
		}

		data, err := readEntry(f, maxIconFileBytes)
		if err != nil {
			continue
		}

		key, ext, _ := strings.Cut(name, ".")
		p := found[key]
		if p == nil {
			p = &pair{}
			found[key] = p
		}
		if ext == "json" {
			p.meta = data
		} else {
			p.data = data
		}
	}

	restored := 0
	for key, p := range found {
		if p.meta != nil && p.data != nil && icons.Restore(key, p.meta, p.data) {
			restored++
		}
	}

	return restored
}

func readJSON(files map[string]*zip.File, name string, limit int64, target any) error {
	f, ok := files[name]
	if !ok {
		return InvalidError{Message: "This is not a backup of Lavender, " + name + " is missing."}
	}

	data, err := readEntry(f, limit)
	if err != nil {
		return InvalidError{Message: name + " in the backup can not be read."}
	}

	if err := json.Unmarshal(data, target); err != nil {
		return InvalidError{Message: name + " in the backup is not valid."}
	}

	return nil
}

// readEntry reads a file of the zip, and fails when it is bigger than the limit once unpacked, whatever its header says.
func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file too big")
	}

	return data, nil
}

func webURL(raw string, maxLen int) string {
	u, err := feedfetch.ParseURL(raw)
	if err != nil || len(u.String()) > maxLen {
		return ""
	}

	return u.String()
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}

	return s
}

func feedType(value string) string {
	for _, t := range db.FeedTypes {
		if t == value {
			return value
		}
	}

	return db.FeedTypeOther
}
