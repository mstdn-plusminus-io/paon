package api

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// remoteNoteContentPolicy mirrors Rails Sanitize::Config::MASTODON_STRICT, which
// HtmlAwareFormatter applies to remote Note content (HtmlAwareFormatter#reformat runs
// Sanitize.fragment(text, Sanitize::Config::MASTODON_STRICT)). It keeps Mastodon's
// supported structural and inline HTML while stripping scripts, event handlers, styles,
// images, and other unsupported markup before the content is stored or served.
//
// This is the sanitizer half of the remote status normalization pipeline; EmojiFormatter
// (shortcode -> custom-emoji <img>) is the other half and is tracked separately.
var remoteNoteContentPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"p", "br", "span", "a", "del", "s", "pre", "blockquote", "code",
		"b", "strong", "u", "i", "em", "ul", "ol", "li", "ruby", "rt", "rp",
	)
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("rel").OnElements("a")
	p.AllowAttrs("class").OnElements("a", "span")
	p.AllowAttrs("translate").OnElements("a", "span")
	p.AllowAttrs("start", "reversed").OnElements("ol")
	p.AllowAttrs("value").OnElements("li")
	p.AllowURLSchemes("http", "https", "dat", "dweb", "ipfs", "ipns", "ssb", "gopher", "xmpp", "magnet", "gemini")
	p.AllowNoAttrs().OnElements("br")
	return p
}()

// sanitizeRemoteNoteContent mirrors Rails HtmlAwareFormatter#reformat for the remote
// branch: it returns the content unchanged when empty, otherwise runs it through the
// MASTODON_STRICT-like policy so disallowed tags/attributes are dropped.
func sanitizeRemoteNoteContent(htmlContent string) string {
	if strings.TrimSpace(htmlContent) == "" {
		return htmlContent
	}
	preformatted := mastodonStrictPreformatUnsupportedElements(htmlContent)
	sanitized := remoteNoteContentPolicy.Sanitize(preformatted)
	return mastodonStrictNormalizeSanitizedFragment(sanitized)
}

func mastodonStrictPreformatUnsupportedElements(fragment string) string {
	nodes, err := parseHTMLFragment(fragment)
	if err != nil {
		return fragment
	}
	root := fragmentRoot(nodes)
	mastodonStrictReplaceMathFallbacks(root)
	mastodonStrictConvertHeadings(root)
	mastodonStrictRemoveUnsupportedAnchors(root)
	return renderHTMLFragment(childNodes(root))
}

// mastodonStrictReplaceMathFallbacks mirrors Mastodon 4.4's MATH_TRANSFORMER
// for FEP-dc88 MathML. MathML itself is not part of the strict allowlist, but a
// direct annotation child can provide a safe plain-text representation. TeX is
// preferred over text/plain regardless of source order.
func mastodonStrictReplaceMathFallbacks(node *nethtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == nethtml.ElementNode && child.Data == "math" {
			if fallback, ok := mastodonStrictMathFallback(child); ok {
				replaceNodeWithText(child, node, fallback)
			}
		} else {
			mastodonStrictReplaceMathFallbacks(child)
		}
		child = next
	}
}

func mastodonStrictMathFallback(math *nethtml.Node) (string, bool) {
	var semantics *nethtml.Node
	for child := math.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode {
			semantics = child
			break
		}
	}
	if semantics == nil || semantics.Data != "semantics" {
		return "", false
	}

	var plain *nethtml.Node
	var tex *nethtml.Node
	for child := semantics.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != nethtml.ElementNode || child.Data != "annotation" {
			continue
		}
		encoding := ""
		for _, attr := range child.Attr {
			if attr.Key == "encoding" {
				encoding = attr.Val
				break
			}
		}
		switch encoding {
		case "application/x-tex":
			if tex == nil {
				tex = child
			}
		case "text/plain":
			if plain == nil {
				plain = child
			}
		}
	}
	if tex != nil {
		delimiter := "$"
		for _, attr := range math.Attr {
			if attr.Key == "display" && attr.Val == "block" {
				delimiter = "$$"
				break
			}
		}
		return delimiter + textContent(tex) + delimiter, true
	}
	if plain != nil {
		return textContent(plain), true
	}
	return "", false
}

func mastodonStrictConvertHeadings(node *nethtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		mastodonStrictConvertHeadings(child)
		if child.Type == nethtml.ElementNode && mastodonStrictHeadingElement(child.Data) {
			strong := &nethtml.Node{Type: nethtml.ElementNode, Data: "strong"}
			for grandchild := child.FirstChild; grandchild != nil; {
				nextGrandchild := grandchild.NextSibling
				child.RemoveChild(grandchild)
				strong.AppendChild(grandchild)
				grandchild = nextGrandchild
			}
			p := &nethtml.Node{Type: nethtml.ElementNode, Data: "p"}
			p.AppendChild(strong)
			node.InsertBefore(p, child)
			node.RemoveChild(child)
		}
		child = next
	}
}

func mastodonStrictNormalizeSanitizedFragment(fragment string) string {
	nodes, err := parseHTMLFragment(fragment)
	if err != nil {
		return fragment
	}
	root := fragmentRoot(nodes)
	mastodonStrictNormalizeNode(root)
	return renderHTMLFragment(childNodes(root))
}

func mastodonStrictNormalizeNode(node *nethtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		mastodonStrictNormalizeNode(child)
		if child.Type == nethtml.ElementNode {
			switch child.Data {
			case "a":
				if !mastodonStrictNormalizeAnchor(child) {
					replaceNodeWithText(child, node, textContent(child))
				}
			case "span":
				child.Attr = mastodonStrictNormalizeClassAndTranslate(child.Attr, true)
			}
		}
		child = next
	}
}

func mastodonStrictRemoveUnsupportedAnchors(node *nethtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		mastodonStrictRemoveUnsupportedAnchors(child)
		if child.Type == nethtml.ElementNode && child.Data == "a" && !mastodonStrictAnchorHasSupportedHref(child) {
			replaceNodeWithText(child, node, textContent(child))
		}
		child = next
	}
}

func mastodonStrictNormalizeAnchor(node *nethtml.Node) bool {
	href := mastodonStrictAnchorHref(node)
	if !mastodonStrictSupportedHref(href) {
		return false
	}
	attrs := []nethtml.Attribute{{Key: "href", Val: href}}
	attrs = append(attrs, mastodonStrictNormalizeClassAndTranslate(node.Attr, true)...)
	attrs = append(attrs,
		nethtml.Attribute{Key: "rel", Val: "nofollow noopener noreferrer"},
		nethtml.Attribute{Key: "target", Val: "_blank"},
	)
	node.Attr = attrs
	return true
}

func mastodonStrictAnchorHasSupportedHref(node *nethtml.Node) bool {
	return mastodonStrictSupportedHref(mastodonStrictAnchorHref(node))
}

func mastodonStrictAnchorHref(node *nethtml.Node) string {
	for _, attr := range node.Attr {
		if attr.Key == "href" {
			return attr.Val
		}
	}
	return ""
}

func mastodonStrictNormalizeClassAndTranslate(attrs []nethtml.Attribute, allowClass bool) []nethtml.Attribute {
	out := make([]nethtml.Attribute, 0, 2)
	for _, attr := range attrs {
		switch attr.Key {
		case "class":
			if allowClass {
				if value := mastodonStrictAllowedClassList(attr.Val); value != "" {
					out = append(out, nethtml.Attribute{Key: "class", Val: value})
				}
			}
		case "translate":
			if attr.Val == "no" {
				out = append(out, nethtml.Attribute{Key: "translate", Val: "no"})
			}
		}
	}
	return out
}

func mastodonStrictAllowedClassList(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	allowed := make([]string, 0, 2)
	for _, className := range strings.Fields(value) {
		if strings.HasPrefix(className, "h-") ||
			strings.HasPrefix(className, "p-") ||
			strings.HasPrefix(className, "u-") ||
			strings.HasPrefix(className, "dt-") ||
			strings.HasPrefix(className, "e-") ||
			className == "mention" ||
			className == "hashtag" ||
			className == "ellipsis" ||
			className == "invisible" {
			allowed = append(allowed, className)
		}
	}
	return strings.Join(allowed, " ")
}

func mastodonStrictSupportedHref(href string) bool {
	if strings.TrimSpace(href) != href || href == "" {
		return false
	}
	parsed, err := url.Parse(href)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "dat", "dweb", "ipfs", "ipns", "ssb", "gopher", "xmpp", "magnet", "gemini":
		return true
	default:
		return false
	}
}

func mastodonStrictHeadingElement(name string) bool {
	switch name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}

func replaceNodeWithText(node *nethtml.Node, parent *nethtml.Node, text string) {
	parent.InsertBefore(&nethtml.Node{Type: nethtml.TextNode, Data: text}, node)
	parent.RemoveChild(node)
}

func textContent(node *nethtml.Node) string {
	var builder strings.Builder
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			builder.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func renderHTMLFragment(nodes []*nethtml.Node) string {
	var buf bytes.Buffer
	for _, node := range nodes {
		_ = nethtml.Render(&buf, node)
	}
	return strings.ReplaceAll(buf.String(), "<br/>", "<br>")
}

func parseHTMLFragment(fragment string) ([]*nethtml.Node, error) {
	return nethtml.ParseFragment(strings.NewReader(fragment), &nethtml.Node{
		Type:     nethtml.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
}

func fragmentRoot(nodes []*nethtml.Node) *nethtml.Node {
	root := &nethtml.Node{Type: nethtml.ElementNode, Data: "div"}
	for _, node := range nodes {
		root.AppendChild(node)
	}
	return root
}

func childNodes(root *nethtml.Node) []*nethtml.Node {
	nodes := make([]*nethtml.Node, 0)
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		nodes = append(nodes, child)
	}
	return nodes
}
