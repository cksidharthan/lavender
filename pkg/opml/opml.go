// Package opml reads and writes OPML, the file the feed readers use to move the list of feeds between them.
package opml

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// maxBytes is the biggest file that is read, maxDepth how deep the outlines of a file can be nested, and maxFeeds
	// how many feeds a file can have.
	maxBytes = 8 << 20
	maxDepth = 16
	MaxFeeds = 5000
)

var (
	ErrNotOPML = errors.New("the file is not an OPML file")
	ErrTooBig  = errors.New("the file is too big")
	ErrTooMany = fmt.Errorf("the file has more than %d feeds", MaxFeeds)
	ErrTooDeep = errors.New("the folders in the file are nested too deep")
)

// Feed is a feed of an OPML file.
type Feed struct {
	// Folder is the name of the folder the feed is in, empty when it is in none. Folders in folders are one folder,
	// the one the feed is in.
	Folder  string
	Title   string
	XMLURL  string // the address of the feed
	HTMLURL string // the address of the site
	Type    string // rss, atom or empty
}

// Parse reads the feeds of an OPML file.
func Parse(r io.Reader) ([]Feed, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	decoder := xml.NewDecoder(limited)
	// files in other charsets are read as they are, only the names can be off.
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	var (
		feeds   []Feed
		folders []string // the names of the outlines that are open, "" for the ones that are feeds
		seenTag bool
	)

	for {
		token, err := decoder.Token()
		if limited.N <= 0 {
			return nil, ErrTooBig
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrNotOPML
		}

		switch t := token.(type) {
		case xml.StartElement:
			switch {
			case !seenTag:
				if !strings.EqualFold(t.Name.Local, "opml") {
					return nil, ErrNotOPML
				}
				seenTag = true
			case strings.EqualFold(t.Name.Local, "outline"):
				if len(folders) >= maxDepth {
					return nil, ErrTooDeep
				}

				attrs := attributes(t)
				if feedURL := attrs["xmlurl"]; feedURL != "" {
					if len(feeds) >= MaxFeeds {
						return nil, ErrTooMany
					}

					feeds = append(feeds, Feed{
						Folder:  nearest(folders),
						Title:   firstNonEmpty(attrs["title"], attrs["text"]),
						XMLURL:  feedURL,
						HTMLURL: attrs["htmlurl"],
						Type:    strings.ToLower(attrs["type"]),
					})
					folders = append(folders, "")
				} else {
					folders = append(folders, strings.TrimSpace(firstNonEmpty(attrs["title"], attrs["text"])))
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "outline") && len(folders) > 0 {
				folders = folders[:len(folders)-1]
			}
		}
	}

	if !seenTag {
		return nil, ErrNotOPML
	}

	return feeds, nil
}

// attributes are the attributes of an element by their lower case name, as files write xmlUrl, xmlurl and XMLURL.
func attributes(e xml.StartElement) map[string]string {
	attrs := make(map[string]string, len(e.Attr))
	for _, a := range e.Attr {
		attrs[strings.ToLower(a.Name.Local)] = strings.TrimSpace(a.Value)
	}

	return attrs
}

// nearest is the name of the folder that is the closest around, empty when there is none.
func nearest(folders []string) string {
	for i := len(folders) - 1; i >= 0; i-- {
		if folders[i] != "" {
			return folders[i]
		}
	}

	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

type document struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    head     `xml:"head"`
	Body    body     `xml:"body"`
}

type head struct {
	Title       string `xml:"title"`
	DateCreated string `xml:"dateCreated"`
}

type body struct {
	Outlines []outline `xml:"outline"`
}

type outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr,omitempty"`
	Type     string    `xml:"type,attr,omitempty"`
	XMLURL   string    `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string    `xml:"htmlUrl,attr,omitempty"`
	Children []outline `xml:"outline,omitempty"`
}

// Write writes the feeds as an OPML file. The feeds in a folder are in an outline of that folder, the other feeds are
// at the top. The order is the one of the feeds.
func Write(w io.Writer, title string, feeds []Feed, now time.Time) error {
	doc := document{Version: "2.0", Head: head{Title: title, DateCreated: now.UTC().Format(time.RFC1123Z)}}

	folderIndex := make(map[string]int)
	var loose []outline
	for _, f := range feeds {
		o := outline{Text: f.Title, Title: f.Title, Type: firstNonEmpty(f.Type, "rss"), XMLURL: f.XMLURL, HTMLURL: f.HTMLURL}

		if f.Folder == "" {
			loose = append(loose, o)
			continue
		}

		i, ok := folderIndex[f.Folder]
		if !ok {
			i = len(doc.Body.Outlines)
			folderIndex[f.Folder] = i
			doc.Body.Outlines = append(doc.Body.Outlines, outline{Text: f.Folder, Title: f.Folder})
		}
		doc.Body.Outlines[i].Children = append(doc.Body.Outlines[i].Children, o)
	}
	doc.Body.Outlines = append(doc.Body.Outlines, loose...)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("error writing the OPML file: %w", err)
	}

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("error writing the OPML file: %w", err)
	}

	_, err := io.WriteString(w, "\n")

	return err
}
