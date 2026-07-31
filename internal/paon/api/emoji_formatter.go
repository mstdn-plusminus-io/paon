package api

import (
	"bytes"
	htmlEscape "html"
	"regexp"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	nethtml "golang.org/x/net/html"
)

// emojiFormatterBounding mirrors Rails EmojiFormatter::DISALLOWED_BOUNDING_REGEX
// (/[[:alnum:]:]/): a :shortcode: candidate is rejected when the char immediately before
// the opening colon or after the closing colon is alphanumeric or another colon.
var emojiFormatterBounding = regexp.MustCompile(`[[:alnum:]:]`)

// applyCustomEmojisToContent mirrors Rails EmojiFormatter. It walks the text nodes of the
// HTML fragment and replaces `:shortcode:` tokens with custom-emoji <img> tags when the
// shortcode is present in emojis. Tokens bounded by an alphanumeric char or another colon
// are left untouched, and text inside an element with class "invisible" is skipped.
// emojiImgSrc builds the <img src> for a matched emoji (callers pass the Paperclip/remote
// URL builder, e.g. activityPubCustomEmojiURL), keeping this function free of *Server state
// so it is straightforward to unit-test.
func applyCustomEmojisToContent(content string, emojis map[string]models.CustomEmoji, emojiImgSrc func(models.CustomEmoji) string) string {
	if content == "" || len(emojis) == 0 || emojiImgSrc == nil {
		return content
	}
	return applyCustomEmojisToContentWithReplacer(content, emojis, func(text string) string {
		return emojiReplacementHTML(text, emojis, emojiImgSrc)
	})
}

func applyCustomEmojisToContentWithReplacer(content string, emojis map[string]models.CustomEmoji, replaceText func(string) string) string {
	if content == "" || len(emojis) == 0 || replaceText == nil {
		return content
	}
	doc, err := nethtml.Parse(strings.NewReader("<div>" + content + "</div>"))
	if err != nil {
		return content
	}
	div := findEmojiNodeByTag(findEmojiNodeByTag(doc, "body"), "div")
	if div == nil {
		return content
	}
	rewriteEmojiTextNodes(div, replaceText, false)
	var buf bytes.Buffer
	for c := div.FirstChild; c != nil; c = c.NextSibling {
		_ = nethtml.Render(&buf, c)
	}
	return buf.String()
}

func findEmojiNodeByTag(n *nethtml.Node, tag string) *nethtml.Node {
	if n == nil {
		return nil
	}
	if n.Type == nethtml.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findEmojiNodeByTag(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func rewriteEmojiTextNodes(n *nethtml.Node, replaceText func(string) string, skip bool) {
	if n.Type == nethtml.ElementNode {
		for _, attr := range n.Attr {
			if attr.Key == "class" && strings.Contains(attr.Val, "invisible") {
				skip = true
				break
			}
		}
	}
	if n.Type == nethtml.TextNode && !skip {
		if replacement := replaceText(n.Data); replacement != n.Data {
			parent := n.Parent
			if fragmentNodes, err := nethtml.ParseFragment(strings.NewReader(replacement), parent); err == nil && len(fragmentNodes) > 0 {
				for _, fn := range fragmentNodes {
					parent.InsertBefore(fn, n)
				}
				parent.RemoveChild(n)
			}
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteEmojiTextNodes(c, replaceText, skip)
	}
}

// emojiReplacementHTML rewrites :shortcode: tokens within a single text node. It mirrors
// Rails EmojiFormatter's scanner: a colon opens a candidate, the next colon closes it, and
// the candidate is replaced only when the bounding chars are allowed and the shortcode maps
// to a known custom emoji. Unknown/bounded candidates leave the opening colon in place and
// scanning continues, so they are preserved verbatim.
func emojiReplacementHTML(text string, emojis map[string]models.CustomEmoji, imgSrc func(models.CustomEmoji) string) string {
	return emojiReplacementHTMLWithTagBuilder(text, emojis, imgSrc, emojiImgTag)
}

func emojiReplacementHTMLWithTagBuilder(text string, emojis map[string]models.CustomEmoji, imgSrc func(models.CustomEmoji) string, imgTag func(models.CustomEmoji, string, func(models.CustomEmoji) string) string) string {
	var out strings.Builder
	i := 0
	for i < len(text) {
		if text[i] != ':' {
			out.WriteByte(text[i])
			i++
			continue
		}
		end := strings.IndexByte(text[i+1:], ':')
		if end < 0 {
			out.WriteString(text[i:])
			break
		}
		shortcode := text[i+1 : i+1+end]
		nextIdx := i + 1 + end + 1
		prevBad := i > 0 && emojiFormatterBounding.MatchString(text[i-1:i])
		nextBad := nextIdx < len(text) && emojiFormatterBounding.MatchString(text[nextIdx:nextIdx+1])
		if shortcode == "" || prevBad || nextBad {
			out.WriteByte(':')
			i++
			continue
		}
		emoji, ok := emojis[shortcode]
		if !ok {
			out.WriteByte(':')
			i++
			continue
		}
		out.WriteString(imgTag(emoji, shortcode, imgSrc))
		i = nextIdx
	}
	return out.String()
}

func emojiImgTag(emoji models.CustomEmoji, shortcode string, imgSrc func(models.CustomEmoji) string) string {
	src := imgSrc(emoji)
	if src == "" {
		return ":" + shortcode + ":"
	}
	return `<img draggable="false" class="emojione" alt=":` + htmlEscape.EscapeString(shortcode) + `:" title=":` + htmlEscape.EscapeString(shortcode) + `:" src="` + htmlEscape.EscapeString(src) + `" />`
}
