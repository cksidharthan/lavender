package pages

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHighlightParts(t *testing.T) {
	require.Equal(t, []textPart{{Text: "Hello"}}, highlightParts("Hello", ""))
	require.Equal(t, []textPart{{Text: "Hello"}}, highlightParts("Hello", "xyz"))
	require.Equal(t,
		[]textPart{{Text: "Learn "}, {Text: "Go", Match: true}, {Text: " and "}, {Text: "go", Match: true}, {Text: "lang"}},
		highlightParts("Learn Go and golang", "go")[:5],
	)
	// words overlap into one match, and letters with accents are kept
	require.Equal(t, []textPart{{Text: "Café ", Match: false}, {Text: "crème", Match: true}}, highlightParts("Café crème", "CRÈME"))
	require.Equal(t, []textPart{{Text: "abcd", Match: true}}, highlightParts("abcd", "abc bcd"))
}
