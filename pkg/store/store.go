// Package store reads and writes the folders and feeds of a user. Every method is scoped to the user it is
// given, a record of another user is reported as not found.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	MaxNameLength = 64
	maxTitleLen   = 255
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
}

// Folder holds the feeds of a folder. The feeds that are in no folder are returned as a Folder without ID and name.
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
		byFolder[folderID] = append(byFolder[folderID], Feed{ID: r.Id, Title: feedTitle(r)})
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

// FindFeed returns the feed of the user.
func (s *Store) FindFeed(userID, id string) (Feed, error) {
	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return Feed{}, err
	}

	return Feed{ID: r.Id, Title: feedTitle(r)}, nil
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
	r.Set("is_active", true)
	r.Set("last_fetched_at", time.Now().UTC())

	if err := s.save(r, "You already follow this feed."); err != nil {
		return Feed{}, err
	}

	return Feed{ID: r.Id, Title: feedTitle(r)}, nil
}

// FeedURL returns the url of the feed of the user.
func (s *Store) FeedURL(userID, id string) (string, error) {
	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return "", err
	}

	return r.GetString("feed_url"), nil
}

// MarkRefreshed stores the metadata of a fetched feed. The title is left alone, as the user can rename a feed.
func (s *Store) MarkRefreshed(userID, id string, meta feedfetch.Meta) error {
	r, err := s.owned(db.FeedsCollection, userID, id)
	if err != nil {
		return err
	}

	r.Set("site_url", meta.SiteURL)
	r.Set("description", meta.Description)
	r.Set("last_fetched_at", time.Now().UTC())

	return s.save(r, "")
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
