package httpapi

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
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

	anchorIdx := oldestRemainingSnippetTurnIndex(turns, selected, "prompter")
	if anchorIdx == -1 {
		anchorIdx = oldestRemainingSnippetTurnIndex(turns, selected, "")
	}
	if anchorIdx >= 0 {
		selected[anchorIdx] = true
	}

	recentIndicesDesc := make([]int, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		if selected[i] {
			continue
		}
		if anchorIdx >= 0 && i < anchorIdx {
			continue
		}
		recentIndicesDesc = append(recentIndicesDesc, i)
	}

	latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget := allocateSnippetBudgets(
		latestPrompterIdx >= 0,
		latestResponderIdx >= 0,
		len(recentIndicesDesc) > 0,
		anchorIdx >= 0,
	)
	latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget = rebalanceSnippetBudgetsForShortTurns(
		turns,
		latestPrompterIdx,
		latestPrompterBudget,
		latestResponderIdx,
		latestResponderBudget,
		anchorIdx,
		anchorBudget,
		recentIndicesDesc,
		recentBudget,
	)

	rendered := make(map[int]string, 4)
	if latestPrompterIdx >= 0 {
		rendered[latestPrompterIdx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[latestPrompterIdx]), latestPrompterBudget)
	}
	if latestResponderIdx >= 0 {
		rendered[latestResponderIdx] = clipSnippetFragment(dispatchSnippetTurnLine(turns[latestResponderIdx]), latestResponderBudget)
	}
	for idx, line := range clipSnippetTurnGroup(turns, recentIndicesDesc, recentBudget) {
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

func rebalanceSnippetBudgetsForShortTurns(
	turns []dispatchSnippetTurn,
	latestPrompterIdx, latestPrompterBudget int,
	latestResponderIdx, latestResponderBudget int,
	anchorIdx, anchorBudget int,
	recentIndicesDesc []int,
	recentBudget int,
) (int, int, int, int) {
	if len(recentIndicesDesc) == 0 {
		return latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget
	}

	unused := 0
	if latestPrompterIdx >= 0 {
		unused += snippetUnusedBudget(dispatchSnippetTurnLine(turns[latestPrompterIdx]), latestPrompterBudget)
	}
	if latestResponderIdx >= 0 {
		unused += snippetUnusedBudget(dispatchSnippetTurnLine(turns[latestResponderIdx]), latestResponderBudget)
	}
	if anchorIdx >= 0 {
		unused += snippetUnusedBudget(dispatchSnippetTurnLine(turns[anchorIdx]), anchorBudget)
	}
	if unused <= 0 {
		return latestPrompterBudget, latestResponderBudget, recentBudget, anchorBudget
	}
	return latestPrompterBudget, latestResponderBudget, recentBudget + unused, anchorBudget
}

func snippetUnusedBudget(line string, budget int) int {
	if budget <= 0 {
		return 0
	}
	lineLen := utf8.RuneCountInString(strings.TrimSpace(line))
	if lineLen >= budget {
		return 0
	}
	return budget - lineLen
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

func clipSnippetTurnGroup(turns []dispatchSnippetTurn, indicesDesc []int, totalBudget int) map[int]string {
	lines := make(map[int]string, len(indicesDesc))
	if len(indicesDesc) == 0 || totalBudget <= 0 {
		return lines
	}
	remaining := totalBudget
	for _, idx := range indicesDesc {
		if remaining <= 0 {
			break
		}
		line := dispatchSnippetTurnLine(turns[idx])
		if line == "" {
			continue
		}
		lineLen := utf8.RuneCountInString(line)
		if lineLen <= remaining {
			lines[idx] = line
			remaining -= lineLen
			continue
		}
		lines[idx] = clipSnippetFragment(line, remaining)
		break
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

func responderCancellationReasonFromFeedbackContent(content string) string {
	content = normalizeSnippetText(content)
	for _, prefix := range []string{
		"a responder cancelled the assigned job due to ",
		"a responder cancelled the claimed job due to ",
	} {
		if strings.HasPrefix(content, prefix) {
			reason := strings.TrimSpace(strings.TrimPrefix(content, prefix))
			if len(reason) >= 2 && reason[0] == '"' && reason[len(reason)-1] == '"' {
				reason = reason[1 : len(reason)-1]
			}
			return reason
		}
	}
	return ""
}

func (s *Server) loadLatestResponderCancelReasons(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	uniqueIDs := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		uniqueIDs = append(uniqueIDs, sessionID)
	}
	if len(uniqueIDs) == 0 {
		return map[string]string{}, nil
	}

	rows, err := s.db.Query(ctx, `
SELECT DISTINCT ON (session_id) session_id, content
FROM messages
WHERE session_id = ANY($1)
  AND type = 'feedback'
  AND role = 'responder'
ORDER BY session_id, created_at DESC`, uniqueIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reasons := make(map[string]string, len(uniqueIDs))
	for rows.Next() {
		var sessionID, content string
		if err := rows.Scan(&sessionID, &content); err != nil {
			return nil, err
		}
		reason := responderCancellationReasonFromFeedbackContent(content)
		if reason != "" {
			reasons[sessionID] = reason
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reasons, nil
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

func buildDispatchSessionSnippetWithQuery(ctx context.Context, query interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, sessionID string) (string, error) {
	rows, err := query.Query(ctx, `
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

func (s *Server) buildDispatchSessionSnippet(ctx context.Context, sessionID string) (string, error) {
	return buildDispatchSessionSnippetWithQuery(ctx, s.db, sessionID)
}

func (s *Server) refreshStoredDispatchSessionSnippetTx(ctx context.Context, tx pgx.Tx, sessionID string) (string, error) {
	snippet, err := buildDispatchSessionSnippetWithQuery(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET dispatch_snippet = $2 WHERE id = $1`, sessionID, snippet); err != nil {
		return "", err
	}
	return snippet, nil
}

func (s *Server) ensureStoredDispatchSessionSnippet(ctx context.Context, sessionID, current string) (string, error) {
	if strings.TrimSpace(current) != "" {
		return current, nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var stored string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(dispatch_snippet, '') FROM sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(&stored); err != nil {
		return "", err
	}
	if strings.TrimSpace(stored) != "" {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return stored, nil
	}

	snippet, err := s.refreshStoredDispatchSessionSnippetTx(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return snippet, nil
}
