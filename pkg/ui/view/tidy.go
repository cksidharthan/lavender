package view

import (
	"bytes"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// tidyBody cleans up the (sanitized) html of an article for the panel:
//   - the picture that is shown on top is removed from the text, with the link that was only around it, as feeds
//     repeat it, also as a smaller version of the file, or from another host of the same site (reddit has
//     preview.redd.it and i.redd.it).
//   - a table of one row is a layout, as in the feeds of reddit that put the picture and the text side by side. Its
//     cells are shown one below the other, so the text gets the whole width.
func tidyBody(markup, heroURL string) string {
	if markup == "" {
		return markup
	}

	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(markup), container)
	if err != nil {
		return markup
	}
	for _, n := range nodes {
		container.AppendChild(n)
	}

	removeRepeatedImage(container, heroURL)
	flattenLayoutTables(container)
	// the posts of reddit that have no picture are not in a table, their footer is at the top level.
	setApart(container)

	var out bytes.Buffer
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&out, c); err != nil {
			return markup
		}
	}

	return out.String()
}

func removeRepeatedImage(root *html.Node, heroURL string) {
	if heroURL == "" {
		return
	}

	var images []*html.Node
	walk(root, func(n *html.Node) {
		if n.DataAtom == atom.Img && sameFile(attr(n, "src"), heroURL) {
			images = append(images, n)
		}
	})

	for _, img := range images {
		parent := img.Parent
		parent.RemoveChild(img)

		// the link that only wrapped the picture goes too, also when that leaves its parents empty
		for parent != nil && parent != root && isEmpty(parent) && (parent.DataAtom == atom.A || parent.DataAtom == atom.Span) {
			next := parent.Parent
			next.RemoveChild(parent)
			parent = next
		}
	}
}

// flattenLayoutTables replaces every table of at most one row by its cells, each as a block.
func flattenLayoutTables(root *html.Node) {
	var tables []*html.Node
	walk(root, func(n *html.Node) {
		if n.DataAtom == atom.Table {
			tables = append(tables, n)
		}
	})

	for _, table := range tables {
		var rows, cells []*html.Node
		walk(table, func(n *html.Node) {
			switch n.DataAtom {
			case atom.Tr:
				rows = append(rows, n)
			case atom.Td, atom.Th:
				cells = append(cells, n)
			}
		})
		if len(rows) > 1 || table.Parent == nil {
			continue
		}

		for _, cell := range cells {
			block := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
			for c := cell.FirstChild; c != nil; {
				next := c.NextSibling
				cell.RemoveChild(c)
				block.AppendChild(c)
				c = next
			}
			if isEmpty(block) {
				continue
			}

			setApart(block)
			table.Parent.InsertBefore(block, table)
		}
		table.Parent.RemoveChild(table)
	}
}

// setApart wraps what follows the last block of the text in a footer, so it can be set apart from the text. It is the
// "submitted by /u/someone [link] [comments]" that reddit adds, which comes as loose text and links after the post.
// Other trailing content is left as it is.
func setApart(block *html.Node) {
	var last *html.Node
	for c := block.FirstChild; c != nil; c = c.NextSibling {
		if isBlockElement(c) {
			last = c
		}
	}
	if last == nil || last.NextSibling == nil {
		return
	}

	footer := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div, Attr: []html.Attribute{{Key: "class", Val: "post-footer"}}}
	for c := last.NextSibling; c != nil; {
		next := c.NextSibling
		block.RemoveChild(c)
		footer.AppendChild(c)
		c = next
	}

	if !strings.Contains(strings.ToLower(textOf(footer)), "submitted by") {
		// not the footer of reddit, put the nodes back as they were
		for c := footer.FirstChild; c != nil; {
			next := c.NextSibling
			footer.RemoveChild(c)
			block.AppendChild(c)
			c = next
		}

		return
	}

	block.AppendChild(footer)
}

func textOf(n *html.Node) string {
	var b strings.Builder
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	})

	return b.String()
}

func isBlockElement(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}

	switch n.DataAtom {
	case atom.P, atom.Div, atom.Ul, atom.Ol, atom.Blockquote, atom.Pre, atom.Table, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}

	return false
}

// sameFile reports whether the two urls are the same picture, in the same or another size or on another host of the
// same site: the file name is the same, on hosts that share their last two labels.
func sameFile(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}

	nameA, nameB := path.Base(ua.Path), path.Base(ub.Path)
	if nameA == "" || nameA == "." || nameA == "/" || nameA != nameB {
		return false
	}

	return siteOf(ua.Hostname()) == siteOf(ub.Hostname())
}

func siteOf(host string) string {
	labels := strings.Split(strings.ToLower(host), ".")

	return strings.Join(labels[max(0, len(labels)-2):], ".")
}

// isEmpty reports whether the node has no element and no text but whitespace.
func isEmpty(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if strings.TrimSpace(c.Data) != "" {
				return false
			}
		case html.ElementNode:
			if c.DataAtom != atom.Br {
				return false
			}
		}
	}

	return true
}

func walk(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}

	return ""
}
