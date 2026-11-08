package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// contextRow is one stored message of a BuildMessages case: the role is store.Role* and the text is
// the content the chat history holds.
type contextRow struct {
	role string
	text string
}

// contextMessages stores the rows of one chat (oldest first) and returns the model request window
// BuildMessages builds from them. Every case gets its own in-memory database (testStoreNamed).
func contextMessages(t *testing.T, env map[string]string, chatID int64, rows []contextRow) []model.Message {
	t.Helper()
	var cfg = testConfig(t, env)
	var d = newTestDispatcher(t, cfg, nil, nil, testStoreNamed(t, t.Name()))
	var ctx = context.Background()
	for i, row := range rows {
		var _, err = store.Append(ctx, d.deps.DB, store.Message{
			ChatID: chatID, UserID: 11, Role: row.role, Content: row.text,
		})
		if err != nil {
			t.Fatalf("append row %d (%s): %v", i, row.role, err)
		}
	}
	var messages, err = d.BuildMessages(ctx, chatID)
	if err != nil {
		t.Fatalf("BuildMessages: %v", err)
	}
	return messages
}

// assertWindow checks that the window is the persona followed by exactly the wanted messages, in
// chat order. The persona is messages[0] and must stay a system message.
func assertWindow(t *testing.T, messages []model.Message, want []contextRow) {
	t.Helper()
	if len(messages) != len(want)+1 {
		t.Fatalf("window has %d messages, want %d (+persona): %+v", len(messages), len(want)+1, contents(messages))
	}
	if messages[0].Role != model.RoleSystem || messages[0].Content != config.DefaultSystemPrompt {
		t.Fatalf("the window must open with the persona prompt, got %q/%q", messages[0].Role, messages[0].Content)
	}
	for i, row := range want {
		var got = messages[i+1]
		if got.Role != row.role || got.Content != row.text {
			t.Fatalf("message %d = %q/%q, want %q/%q", i+1, got.Role, got.Content, row.role, row.text)
		}
	}
}

func contents(messages []model.Message) []string {
	var out = make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Role+": "+message.Content)
	}
	return out
}

func TestBuildMessagesDropsWholeTurnOnCut(t *testing.T) {
	var olderQuestion = strings.Repeat("q", 100)
	var olderAnswer = strings.Repeat("a", 20)
	var newerQuestion = strings.Repeat("w", 60)
	var newerAnswer = strings.Repeat("e", 60)
	var all = []contextRow{
		{store.RoleUser, olderQuestion},
		{store.RoleAssistant, olderAnswer},
		{store.RoleUser, newerQuestion},
		{store.RoleAssistant, newerAnswer},
	}
	cases := []struct {
		name   string
		budget string
		want   []contextRow
	}{
		{
			// The older question alone would still fit (28+120+100 = 248 <= 256), so the old
			// per-message loop kept it and dropped only its answer. The turn is the unit now.
			name:   "question fits but its answer does not: the whole turn is dropped",
			budget: "256",
			want: []contextRow{
				{store.RoleUser, newerQuestion},
				{store.RoleAssistant, newerAnswer},
			},
		},
		{
			name:   "everything fits: no turn is dropped",
			budget: "1024",
			want:   all,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var messages = contextMessages(t, map[string]string{"MAX_CONTEXT_CHARS": tc.budget}, 700, all)
			assertWindow(t, messages, tc.want)
		})
	}
}

func TestBuildMessagesNewestQuestionSurvivesTinyBudget(t *testing.T) {
	// MAX_CONTEXT_CHARS cannot go below 256, so "one rune above the persona" needs a 255-rune
	// persona: the budget then leaves a single rune for the question.
	var persona = strings.Repeat("p", 255)
	var messages = contextMessages(t, map[string]string{
		"SYSTEM_PROMPT": persona, "MAX_CONTEXT_CHARS": "256",
	}, 701, []contextRow{
		{store.RoleUser, "вопрос"},
		{store.RoleAssistant, "ответ"},
	})
	if len(messages) != 2 {
		t.Fatalf("window has %d messages, want persona+question: %+v", len(messages), contents(messages))
	}
	if messages[0].Role != model.RoleSystem || messages[0].Content != persona {
		t.Fatalf("persona was not kept as the first system message: %+v", messages[0])
	}
	var want = string([]rune("вопрос")[:1]) + TruncatedMarker
	if messages[1].Role != model.RoleUser || messages[1].Content != want {
		t.Fatalf("newest question = %q/%q, want a one-rune cut %q", messages[1].Role, messages[1].Content, want)
	}
}

func TestBuildMessagesCutsNewestAssistantBeforeQuestion(t *testing.T) {
	var question = strings.Repeat("q", 100)
	var answer = strings.Repeat("a", 200)
	cases := []struct {
		name     string
		question string
		answer   string
		want     []contextRow
	}{
		{
			name:     "the answer is cut to the room the question left",
			question: question,
			answer:   answer,
			want: []contextRow{
				{store.RoleUser, question},
				{store.RoleAssistant, strings.Repeat("a", 128) + TruncatedMarker},
			},
		},
		{
			// 28 persona + 228 question = 256 exactly: nothing is left, so the answer goes and the
			// question arrives whole.
			name:     "the answer is dropped when the question fills the budget",
			question: strings.Repeat("q", 228),
			answer:   "ответ",
			want: []contextRow{
				{store.RoleUser, strings.Repeat("q", 228)},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var messages = contextMessages(t, map[string]string{"MAX_CONTEXT_CHARS": "256"}, 702, []contextRow{
				{store.RoleUser, tc.question},
				{store.RoleAssistant, tc.answer},
			})
			assertWindow(t, messages, tc.want)
		})
	}
}

func TestBuildMessagesDropsLeadingOrphanAssistant(t *testing.T) {
	cases := []struct {
		name string
		rows []contextRow
		want []contextRow
	}{
		{
			name: "an assistant prefix whose question is outside CONTEXT_MESSAGES",
			rows: []contextRow{
				{store.RoleAssistant, "ответ без вопроса"},
				{store.RoleUser, "вопрос"},
				{store.RoleAssistant, "ответ"},
			},
			want: []contextRow{
				{store.RoleUser, "вопрос"},
				{store.RoleAssistant, "ответ"},
			},
		},
		{
			name: "only orphan answers: the window is the persona alone",
			rows: []contextRow{
				{store.RoleAssistant, "первый сирота"},
				{store.RoleAssistant, "второй сирота"},
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var messages = contextMessages(t, map[string]string{}, 703, tc.rows)
			assertWindow(t, messages, tc.want)
		})
	}
}

func TestBuildMessagesKeepsMultipleAssistantRowsOfTurn(t *testing.T) {
	var olderQuestion = strings.Repeat("q", 100)
	var olderAnswer = strings.Repeat("a", 100)
	var newerQuestion = strings.Repeat("w", 20)
	var toolCall = strings.Repeat("t", 20)
	var toolResult = strings.Repeat("r", 20)
	var finalAnswer = strings.Repeat("e", 20)
	var all = []contextRow{
		{store.RoleUser, olderQuestion},
		{store.RoleAssistant, olderAnswer},
		{store.RoleUser, newerQuestion},
		{store.RoleAssistant, toolCall},
		{store.RoleAssistant, toolResult},
		{store.RoleAssistant, finalAnswer},
	}
	cases := []struct {
		name   string
		budget string
		want   []contextRow
	}{
		{
			name:   "a tool loop keeps every assistant row of its turn",
			budget: "1024",
			want:   all,
		},
		{
			// 28 persona + 80 newest turn = 108; the 200-rune older turn does not fit, so it is
			// dropped whole instead of keeping its question without its answer.
			name:   "a tight budget drops the older turn, never a row of the newest one",
			budget: "256",
			want: []contextRow{
				{store.RoleUser, newerQuestion},
				{store.RoleAssistant, toolCall},
				{store.RoleAssistant, toolResult},
				{store.RoleAssistant, finalAnswer},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var messages = contextMessages(t, map[string]string{"MAX_CONTEXT_CHARS": tc.budget}, 704, all)
			assertWindow(t, messages, tc.want)
		})
	}
}

func TestBuildMessagesCountsPersonaAgainstBudget(t *testing.T) {
	var older = strings.Repeat("o", 120)
	var newer = strings.Repeat("n", 120)
	cases := []struct {
		name   string
		system string
		want   []contextRow
	}{
		{
			// 28 persona + 120 + 120 = 268 > 256: the older turn leaves, though 120+120 alone fits.
			name:   "the default persona pushes the older turn out",
			system: "",
			want:   []contextRow{{store.RoleUser, newer}},
		},
		{
			// 1 persona + 120 + 120 = 241 <= 256: the same rows fit once the persona costs less.
			name:   "a one-rune persona leaves room for the same rows",
			system: "x",
			want: []contextRow{
				{store.RoleUser, older},
				{store.RoleUser, newer},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env = map[string]string{"MAX_CONTEXT_CHARS": "256"}
			if tc.system != "" {
				env["SYSTEM_PROMPT"] = tc.system
			}
			var messages = contextMessages(t, env, 705, []contextRow{
				{store.RoleUser, older},
				{store.RoleUser, newer},
			})
			if messages[0].Role != model.RoleSystem {
				t.Fatalf("the window must open with a system persona, got %q", messages[0].Role)
			}
			var wantPersona = config.DefaultSystemPrompt
			if tc.system != "" {
				wantPersona = tc.system
			}
			if messages[0].Content != wantPersona {
				t.Fatalf("persona = %q, want %q", messages[0].Content, wantPersona)
			}
			if len(messages) != len(tc.want)+1 {
				t.Fatalf("window = %+v, want persona + %d messages", contents(messages), len(tc.want))
			}
			for i, row := range tc.want {
				if messages[i+1].Role != row.role || messages[i+1].Content != row.text {
					t.Fatalf("message %d = %q/%q, want %q/%q", i+1, messages[i+1].Role, messages[i+1].Content, row.role, row.text)
				}
			}
		})
	}
}

func TestBuildMessagesSmallChatFitsEntirely(t *testing.T) {
	var rows = []contextRow{
		{store.RoleUser, "первый вопрос"},
		{store.RoleAssistant, "первый ответ"},
		{store.RoleUser, "второй вопрос"},
		{store.RoleAssistant, "второй ответ"},
	}
	var messages = contextMessages(t, map[string]string{}, 706, rows)
	assertWindow(t, messages, rows)
}

func TestBuildMessagesStillTruncatesUserRowsByMaxInputChars(t *testing.T) {
	var question = strings.Repeat("в", 100)
	var messages = contextMessages(t, map[string]string{"MAX_INPUT_CHARS": "64"}, 707, []contextRow{
		{store.RoleUser, question},
	})
	if len(messages) != 2 || messages[1].Role != model.RoleUser {
		t.Fatalf("window = %+v, want persona + the question", contents(messages))
	}
	var want = strings.Repeat("в", 64) + TruncatedMarker
	if messages[1].Content != want {
		t.Fatalf("question = %q, want the MAX_INPUT_CHARS cut %q", messages[1].Content, want)
	}
}
