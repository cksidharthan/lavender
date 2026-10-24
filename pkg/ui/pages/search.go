package pages

import (
	"strings"
	"unicode"

	"github.com/cksidharthan/lavender/pkg/ui/components"
)

// SearchProps are the props of the search page.
type SearchProps struct {
	Query   string
	FeedID  string // the feed the search is limited to, empty for all of them
	Folders []components.Folder
	Results SearchResultsProps
}

// SearchResultsProps are what is below the search box: a hint, the articles that were found or a message.
type SearchResultsProps struct {
	Query string
	Page  ArticlePage
}

// textPart is a piece of a text, and whether it is what a search found.
type textPart struct {
	Text  string
	Match bool
}

// highlightParts cuts the text in pieces, the ones that hold a word of the query marked as a match. The match
// ignores the case. Without a query the text is one piece.
func highlightParts(text, query string) []textPart {
	words := strings.Fields(query)
	if len(words) == 0 {
		return []textPart{{Text: text}}
	}

	runes := []rune(text)
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}

	marked := make([]bool, len(runes))
	for _, w := range words {
		needle := []rune(strings.ToLower(w))
		if len(needle) == 0 || len(needle) != len([]rune(w)) {
			continue
		}

		for i := 0; i+len(needle) <= len(lower); i++ {
			if string(lower[i:i+len(needle)]) == string(needle) {
				for j := i; j < i+len(needle); j++ {
					marked[j] = true
				}
			}
		}
	}

	var parts []textPart
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || marked[i] != marked[start] {
			parts = append(parts, textPart{Text: string(runes[start:i]), Match: marked[start]})
			start = i
		}
	}

	if len(parts) == 0 {
		return []textPart{{Text: text}}
	}

	return parts
}
