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

type dispatchSnippetTurn struct {
	Role    string
	Content string
}

func buildSessionSnippetFromNewestFirst(messages []dispatchSnippetMessage) string {
	return buildSessionSnippetFromNewestFirstWithSourceTrimmed(messages, false)
}

func buildSessionSnippetFromNewestFirstWithSourceTrimmed(messages []dispatchSnippetMessage, sourceTrimmed bool) string {
	if len(messages) == 0 {
		return "no messages yet"
	}

	turns := mergeSnippetTurns(messages)
	if len(turns) == 0 {
		return "no messages yet"
	}

	latestPrompterIdx := latestSnippetTurnIndex(turns, "prompter")
	latestResponderIdx := latestSnippetTurnIndex(turns, "responder")

	selected := make(map[int]bool, 4)
	if latestPrompterIdx >= 0 {
		selected[latestPrompterIdx] = true
	}
	if latestResponderIdx >= 0 {
		selected[latestResponderIdx] = true
	}

	recentDesc := make([]int, 0, 2)
	for i := len(turns) - 1; i >= 0 && len(recentDesc) < 2; i-- {
		if selected[i] {
			continue
		}
		recentDesc = append(recentDesc, i)
		selected[i] = true
	}
	recentIndices := reverseIntsCopy(recentDesc)

	anchorIdx := oldestRemainingSnippetTurnIndex(turns, selected, "prompter")
	if anchorIdx == -1 {
		anchorIdx = oldestRemainingSnippetTurnIndex(turns, selected, "")
	}

	latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget := allocateSnippetBudgets(
		latestPrompterIdx >= 0,
		latestResponderIdx >= 0,
		len(recentIndices) > 0,
		anchorIdx >= 0,
	)

	rendered := make(map[int]string, 4)
	if latestPrompterIdx >= 0 {
		rendered[latestPrompterIdx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[latestPrompterIdx]), latestPrompterBudget)
	}
	if latestResponderIdx >= 0 {
		rendered[latestResponderIdx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[latestResponderIdx]), latestResponderBudget)
	}
	for idx, line := range clipSnippetTurnGroup(turns, recentIndices, recentBudget) {
		rendered[idx] = line
	}
	if anchorIdx >= 0 {
		rendered[anchorIdx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[anchorIdx]), anchorBudget)
	}

	fragments := make([]string, 0, len(rendered))
	earliestSelectedIdx := -1
	for i := 0; i < len(turns); i++ {
		line, ok := rendered[i]
		if !ok || line == "" {
			continue
		}
		if earliestSelectedIdx == -1 {
			earliestSelectedIdx = i
		}
		fragments = append(fragments, line)
	}
	if len(fragments) == 0 {
		return "no messages yet"
	}

	if sourceTrimmed || earliestSelectedIdx > 0 {
		if !strings.HasPrefix(fragments[0], "...") {
			fragments[0] = "... " + fragments[0]
		}
	}
	return strings.Join(fragments, "| ")
}

func allocateSnippetBudgets(hasLatestPrompter, hasLatestResponder, hasRecent, hasAnchor bool) (int, int, int, int) {
	latestPrompterBudget := 35 * dispatchSnippetOutputRuneLimit / 100
	latestResponderBudget := 25 * dispatchSnippetOutputRuneLimit / 100
	recentBudget := 25 * dispatchSnippetOutputRuneLimit / 100
	anchorBudget := dispatchSnippetOutputRuneLimit - latestPrompterBudget - latestResponderBudget - recentBudget

	allocated := 0
	if hasLatestPrompter {
		allocated += latestPrompterBudget
	} else {
		latestPrompterBudget = 0
	}
	if hasLatestResponder {
		allocated += latestResponderBudget
	} else {
		latestResponderBudget = 0
	}
	if hasRecent {
		allocated += recentBudget
	} else {
		recentBudget = 0
	}
	if hasAnchor {
		allocated += anchorBudget
	} else {
		anchorBudget = 0
	}

	unused := dispatchSnippetOutputRuneLimit - allocated
	if unused <= 0 {
		return latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget
	}

	switch {
	case hasRecent:
		recentBudget += unused
	case hasLatestPrompter && hasLatestResponder:
		latestPrompterBudget += unused / 2
		latestResponderBudget += unused - (unused / 2)
	case hasLatestPrompter:
		latestPrompterBudget += unused
	case hasLatestResponder:
		latestResponderBudget += unused
	case hasAnchor:
		anchorBudget += unused
	}

	return latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget
}

func mergeSnippetTurns(messagesNewestFirst []dispatchSnippetMessage) []dispatchSnippetTurn {
	turns := make([]dispatchSnippetTurn, 0, len(messagesNewestFirst))
	for i := len(messagesNewestFirst) - 1; i >= 0; i-- {
		message := messagesNewestFirst[i]
		if message.Type == "feedback" {
			continue
		}
		content := normalizeSnippetText(message.Content)
		if content == "" {
			continue
		}
		if len(turns) > 0 && turns[len(turns)-1].Role == message.Role {
			turns[len(turns)-1].Content += " / " + content
			continue
		}
		turns = append(turns, dispatchSnippetTurn{Role: message.Role, Content: content})
	}
	return turns
}

func latestSnippetTurnIndex(turns []dispatchSnippetTurn, role string) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == role {
			return i
		}
	}
	return -1
}

func oldestRemainingSnippetTurnIndex(turns []dispatchSnippetTurn, selected map[int]bool, preferredRole string) int {
	for i := 0; i < len(turns); i++ {
		if selected[i] {
			continue
		}
		if preferredRole == "" || turns[i].Role == preferredRole {
			return i
		}
	}
	return -1
}

func clipSnippetTurnGroup(turns []dispatchSnippetTurn, indices []int, totalBudget int) map[int]string {
	lines := make(map[int]string, len(indices))
	if len(indices) == 0 || totalBudget <= 0 {
		return lines
	}
	baseBudget := totalBudget / len(indices)
	remainder := totalBudget % len(indices)
	for i, idx := range indices {
		budget := baseBudget
		if i >= len(indices)-remainder {
			budget++
		}
		lines[idx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[idx]), budget)
	}
	return lines
}

func normalizeSnippetText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func dispatchSnippetRolePrefix(message dispatchSnippetMessage) string {
	if message.Role == "responder" {
		return "responder"
	}
	return "prompter"
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

func dispatchSnippetTurnLine(turn dispatchSnippetTurn) string {
	content := normalizeSnippetText(turn.Content)
	if content == "" {
		return ""
	}
	role := dispatchSnippetRolePrefix(dispatchSnippetMessage{Role: turn.Role})
	return role + ": " + content
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

func reverseIntsCopy(values []int) []int {
	out := make([]int, len(values))
	for i := range values {
		out[len(values)-1-i] = values[i]
	}
	return out
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
	sourceTrimmed := false
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
			sourceTrimmed = true
			break
		}
		if utf8.RuneCountInString(content) > remaining {
			content = tailRunes(content, remaining)
			sourceTrimmed = true
		}
		messages = append(messages, dispatchSnippetMessage{Type: typ, Role: role, Content: content})
		usedRunes += utf8.RuneCountInString(content)
	}
	return buildSessionSnippetFromNewestFirstWithSourceTrimmed(messages, sourceTrimmed), nil
}
