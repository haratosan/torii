package channel

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	reCodeBlock     = regexp.MustCompile("(?s)```(\\w*)\n?(.*?)```")
	reInlineCode    = regexp.MustCompile("`([^`\n]+)`")
	reLink          = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBold          = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic        = regexp.MustCompile(`\*([^*\n]+?)\*`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
)

// markdownToHTML converts common Markdown patterns to Telegram-compatible HTML.
// Unsupported or broken formatting passes through as plain text.
func markdownToHTML(s string) string {
	var phs []string
	ph := func(h string) string {
		idx := len(phs)
		phs = append(phs, h)
		return fmt.Sprintf("\x00PH%d\x00", idx)
	}

	// 1. Code blocks → <pre>
	s = reCodeBlock.ReplaceAllStringFunc(s, func(m string) string {
		sub := reCodeBlock.FindStringSubmatch(m)
		lang, code := sub[1], html.EscapeString(sub[2])
		if lang != "" {
			return ph(fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", lang, code))
		}
		return ph("<pre>" + code + "</pre>")
	})

	// 2. Inline code → <code>
	s = reInlineCode.ReplaceAllStringFunc(s, func(m string) string {
		return ph("<code>" + html.EscapeString(reInlineCode.FindStringSubmatch(m)[1]) + "</code>")
	})

	// 3. Links → <a>
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		return ph(fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(sub[2]), html.EscapeString(sub[1])))
	})

	// 4. HTML-escape all remaining text
	s = html.EscapeString(s)

	// 5. Convert formatting (* and ~ are not HTML-special, so survive step 4)
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reItalic.ReplaceAllString(s, "<i>$1</i>")
	s = reStrikethrough.ReplaceAllString(s, "<s>$1</s>")

	// 6. Restore placeholders (escaped by step 4, but \x00 is not an HTML entity)
	for i, block := range phs {
		key := html.EscapeString(fmt.Sprintf("\x00PH%d\x00", i))
		s = strings.Replace(s, key, block, 1)
	}

	return s
}
