package view

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Video is a youtube video to show with a player.
type Video struct {
	ID    string
	Start int // seconds to start at, 0 for the beginning
}

// EmbedURL is the address of the player. It is the privacy friendly domain, which sets no cookies until the video is
// played.
func (v Video) EmbedURL() string {
	u := "https://www.youtube-nocookie.com/embed/" + v.ID
	if v.Start > 0 {
		u += "?start=" + strconv.Itoa(v.Start)
	}

	return u
}

// youtubeVideo returns the video a link points to. Only the real youtube hosts are accepted, and the id must have the
// shape of a video id, so nothing else ends up in the address of a player.
func youtubeVideo(rawURL string) (Video, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Video{}, false
	}

	var id string
	switch strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") {
	case "youtu.be":
		id = strings.Trim(u.Path, "/")
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		switch {
		case u.Path == "/watch":
			id = u.Query().Get("v")
		default:
			for _, prefix := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
				if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
					id = strings.Trim(rest, "/")
					break
				}
			}
		}
	}

	if !videoIDPattern.MatchString(id) {
		return Video{}, false
	}

	return Video{ID: id, Start: startSeconds(u)}, true
}

// startSeconds reads the start time of a link, as seconds (t=90, start=90s). Other forms like 1m30s are ignored.
func startSeconds(u *url.URL) int {
	for _, key := range []string{"t", "start"} {
		value := strings.TrimSuffix(u.Query().Get(key), "s")
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 && seconds < 24*60*60 {
			return seconds
		}
	}

	return 0
}

// withPlayers puts a player for each youtube video that is linked in the (sanitized) html, below the paragraph that
// has the link. A video is shown once, also when it is linked more than once, and not when it is in skip, as it is
// shown somewhere else.
func withPlayers(markup string, skip ...string) string {
	if !strings.Contains(markup, "youtu") {
		return markup
	}

	nodes, err := html.ParseFragment(strings.NewReader(markup), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return markup
	}

	shown := make(map[string]bool, len(skip))
	for _, id := range skip {
		shown[id] = true
	}

	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		container.AppendChild(n)
	}

	var anchors []*html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			anchors = append(anchors, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(container)

	for _, a := range anchors {
		href := ""
		for _, attr := range a.Attr {
			if attr.Key == "href" {
				href = attr.Val
			}
		}

		video, ok := youtubeVideo(href)
		if !ok || shown[video.ID] {
			continue
		}
		shown[video.ID] = true

		block := a
		for block.Parent != container && !isBlock(block) {
			block = block.Parent
		}
		block.Parent.InsertBefore(playerNode(video), block.NextSibling)
	}

	var out bytes.Buffer
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&out, c); err != nil {
			return markup
		}
	}

	return out.String()
}

func isBlock(n *html.Node) bool {
	switch n.DataAtom {
	case atom.P, atom.Li, atom.Td, atom.Th, atom.Div, atom.Blockquote, atom.H1, atom.H2, atom.H3, atom.H4, atom.Pre:
		return true
	}

	return false
}

func playerNode(v Video) *html.Node {
	wrapper := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div, Attr: []html.Attribute{{Key: "class", Val: "video"}}}
	wrapper.AppendChild(&html.Node{Type: html.ElementNode, Data: "iframe", DataAtom: atom.Iframe, Attr: PlayerAttrs(v)})

	return wrapper
}

// PlayerAttrs are the attributes of the iframe of a player. The player runs in a sandbox, and gets the referrer
// youtube asks for, as it refuses to play without one.
func PlayerAttrs(v Video) []html.Attribute {
	return []html.Attribute{
		{Key: "src", Val: v.EmbedURL()},
		{Key: "title", Val: "YouTube video player"},
		{Key: "loading", Val: "lazy"},
		{Key: "allowfullscreen", Val: ""},
		{Key: "allow", Val: "encrypted-media; picture-in-picture; fullscreen"},
		{Key: "referrerpolicy", Val: "strict-origin-when-cross-origin"},
		{Key: "sandbox", Val: "allow-scripts allow-same-origin allow-presentation allow-popups allow-popups-to-escape-sandbox"},
	}
}
