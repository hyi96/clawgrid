package httpapi

import (
	"context"
	"strings"
	"unicode/utf8"
)

type dispatchSnippetMessage struct {
	Type    string
	Role    string
	Content string
}

func buildSessionSnippetFromNewestFirst(messages []dispatchSnippetMessage) string {
	if len(messages) == 0 {
		return "no messages yet"
	}

	fragments := make([]string, 0, len(messages))
	usedRunes := 0
	truncatedOlderContext := false
	for _, message := range messages {
		fragment := clipSnippetFragment(dispatchSnippetLine(message), dispatchSnippetFragmentRuneLimit)
		if fragment == "" {
			continue
		}
		extra := utf8.RuneCountInString(fragment)
		if len(fragments) > 0 {
			extra += 5
		}
		if usedRunes+extra > dispatchSnippetOutputRuneLimit {
			remaining := dispatchSnippetOutputRuneLimit - usedRunes
			if len(fragments) > 0 {
				remaining -= 5
			}
			if remaining <= 0 {
				truncatedOlderContext = true
				break
			}
			fragment = clipSnippetFragment(fragment, remaining)
			if fragment == "" {
				truncatedOlderContext = true
				break
			}
			extra = utf8.RuneCountInString(fragment)
			if len(fragments) > 0 {
				extra += 5
			}
		}
		fragments = append(fragments, fragment)
		usedRunes += extra
		if usedRunes >= dispatchSnippetOutputRuneLimit {
			truncatedOlderContext = true
			break
		}
	}
	if len(fragments) == 0 {
		return "no messages yet"
	}
	reverseStrings(fragments)
	snippet := strings.Join(fragments, " | ")
	if len(messages) > len(fragments) || truncatedOlderContext {
		return "... " + snippet
	}
	return snippet
}

func normalizeSnippetText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func dispatchSnippetRolePrefix(message dispatchSnippetMessage) string {
	if message.Role == "responder" {
		return "reply"
	}
	return "user"
}

func dispatchSnippetLine(message dispatchSnippetMessage) string {
	content := normalizeSnippetText(message.Content)
	if content == "" {
		return ""
	}
	if message.Type == "feedback" {
		return ""
	}
	prefix := dispatchSnippetRolePrefix(message)
	return prefix + ": " + content
}

func clipSnippetFragment(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	if max <= 3 {
		return string(runes[len(runes)-max:])
	}
	return "..." + string(runes[len(runes)-(max-3):])
}

func tailRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[len(runes)-max:])
}

func clipRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func reverseStrings(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func (s *Server) buildDispatchSessionSnippet(ctx context.Context, sessionID string) (string, error) {
	rows, err := s.db.Query(ctx, `
SELECT type, role, content
FROM messages
WHERE session_id = $1
ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	messages := make([]dispatchSnippetMessage, 0, 12)
	usedRunes := 0
	for rows.Next() {
		var typ, role, content string
		if err := rows.Scan(&typ, &role, &content); err != nil {
			return "", err
		}
		content = normalizeSnippetText(content)
		if content == "" {
			continue
		}
		remaining := dispatchSnippetSourceRuneLimit - usedRunes
		if remaining <= 0 {
			break
		}
		if utf8.RuneCountInString(content) > remaining {
			content = tailRunes(content, remaining)
		}
		messages = append(messages, dispatchSnippetMessage{Type: typ, Role: role, Content: content})
		usedRunes += utf8.RuneCountInString(content)
	}
	return buildSessionSnippetFromNewestFirst(messages), nil
}
