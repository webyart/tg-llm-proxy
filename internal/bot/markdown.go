package bot

import (
	"fmt"
	"regexp"
	"strings"

	tgbot "github.com/go-telegram/bot"
)

var (
	codeFencePattern  = regexp.MustCompile(`(?s)` + "```" + `[A-Za-z0-9_+.-]*\r?\n(.*?)` + "```")
	inlineCodePattern = regexp.MustCompile("`([^`\r\n]+)`")
	linkPattern       = regexp.MustCompile(`\[([^\]\r\n]+)\]\((https?://[^\s)]+)\)`)
	boldPattern       = regexp.MustCompile(`\*\*([^*\r\n]+)\*\*`)
	underlinePattern  = regexp.MustCompile(`__([^_\r\n]+)__`)
	spoilerPattern    = regexp.MustCompile(`\|\|([^\r\n]+)\|\|`)
	strikePattern     = regexp.MustCompile(`~([^~\r\n]+)~`)
	italicAsterisk    = regexp.MustCompile(`\*([^*\r\n]+)\*`)
	italicUnderscore  = regexp.MustCompile(`_([^_\r\n]+)_`)
	headingPattern    = regexp.MustCompile(`^#{1,6}[ \t]+(.+)$`)
)

type markdownTokens struct {
	values []string
}

func (tokens *markdownTokens) put(value string) string {
	token := fmt.Sprintf("@@TGMD%d@@", len(tokens.values))
	tokens.values = append(tokens.values, value)
	return token
}

func (tokens *markdownTokens) restore(text string) string {
	for index, value := range tokens.values {
		text = strings.Replace(text, fmt.Sprintf("@@TGMD%d@@", index), value, 1)
	}
	return text
}

// formatMarkdownV2 preserves the Markdown constructs Telegram renders, while escaping all ordinary text.
// Links are accepted only with an explicit http/https URL; code is escaped for Telegram's code context.
func formatMarkdownV2(text string) string {
	tokens := &markdownTokens{}
	text = codeFencePattern.ReplaceAllStringFunc(text, func(block string) string {
		match := codeFencePattern.FindStringSubmatch(block)
		content := strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(match[1])
		return tokens.put("```\n" + content + "```")
	})

	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if match := headingPattern.FindStringSubmatch(line); match != nil {
			lines[index] = "*" + formatInlineMarkdown(match[1], tokens) + "*"
			continue
		}
		lines[index] = formatInlineMarkdown(line, tokens)
	}
	return tokens.restore(strings.Join(lines, "\n"))
}

func formatInlineMarkdown(text string, tokens *markdownTokens) string {
	text = inlineCodePattern.ReplaceAllStringFunc(text, func(code string) string {
		match := inlineCodePattern.FindStringSubmatch(code)
		content := strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(match[1])
		return tokens.put("`" + content + "`")
	})
	text = linkPattern.ReplaceAllStringFunc(text, func(link string) string {
		match := linkPattern.FindStringSubmatch(link)
		return tokens.put("[" + tgbot.EscapeMarkdown(match[1]) + "](" + match[2] + ")")
	})
	text = boldPattern.ReplaceAllStringFunc(text, func(value string) string {
		match := boldPattern.FindStringSubmatch(value)
		return tokens.put("*" + tgbot.EscapeMarkdown(match[1]) + "*")
	})
	text = underlinePattern.ReplaceAllStringFunc(text, func(value string) string {
		match := underlinePattern.FindStringSubmatch(value)
		return tokens.put("*" + tgbot.EscapeMarkdown(match[1]) + "*")
	})
	text = spoilerPattern.ReplaceAllStringFunc(text, func(value string) string {
		match := spoilerPattern.FindStringSubmatch(value)
		return tokens.put("||" + tgbot.EscapeMarkdown(match[1]) + "||")
	})
	text = strikePattern.ReplaceAllStringFunc(text, func(value string) string {
		match := strikePattern.FindStringSubmatch(value)
		return tokens.put("~" + tgbot.EscapeMarkdown(match[1]) + "~")
	})
	text = italicAsterisk.ReplaceAllStringFunc(text, func(value string) string {
		match := italicAsterisk.FindStringSubmatch(value)
		return tokens.put("*" + tgbot.EscapeMarkdown(match[1]) + "*")
	})
	text = italicUnderscore.ReplaceAllStringFunc(text, func(value string) string {
		match := italicUnderscore.FindStringSubmatch(value)
		return tokens.put("_" + tgbot.EscapeMarkdown(match[1]) + "_")
	})
	return tgbot.EscapeMarkdown(text)
}
