package bot

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	text = formatItalicUnderscores(text, tokens)
	return tgbot.EscapeMarkdown(text)
}

func formatItalicUnderscores(text string, tokens *markdownTokens) string {
	var out strings.Builder
	for len(text) > 0 {
		match := italicUnderscore.FindStringSubmatchIndex(text)
		if match == nil {
			out.WriteString(text)
			break
		}
		start, end := match[0], match[1]
		if markdownWordBefore(text, start) || markdownWordAfter(text, end) {
			out.WriteString(text[:start+1])
			text = text[start+1:]
			continue
		}
		out.WriteString(text[:start])
		out.WriteString(tokens.put("_" + tgbot.EscapeMarkdown(text[match[2]:match[3]]) + "_"))
		text = text[end:]
	}
	return out.String()
}

func markdownWordBefore(text string, index int) bool {
	if index == 0 {
		return false
	}
	symbol, _ := utf8.DecodeLastRuneInString(text[:index])
	return unicode.IsLetter(symbol) || unicode.IsNumber(symbol)
}

func markdownWordAfter(text string, index int) bool {
	if index == len(text) {
		return false
	}
	symbol, _ := utf8.DecodeRuneInString(text[index:])
	return unicode.IsLetter(symbol) || unicode.IsNumber(symbol)
}
