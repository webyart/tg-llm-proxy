package bot

import (
	"strings"
	"testing"
)

// commandParityMatrix is the contract the roadmap asks for: every command in registry() either has a Mini App
// surface (one or more /api/... endpoints) or an explicit Telegram-only reason. Adding a command without updating
// this table fails the test, so the third client cannot silently forget half of the commands.
var commandParityMatrix = map[string]string{
	CommandStart:         "telegram-only: the Mini App opens with a Telegram-issued session, /start is not needed",
	CommandHelp:          "telegram-only: help lives in the README and in inline UI hints",
	CommandPing:          "telegram-only: diagnostics; the Mini App uses /healthz",
	CommandStatus:        "/api/status",
	CommandModel:         "/api/settings, /api/settings/model",
	CommandStream:        "/api/settings/stream",
	CommandMiniApp:       "/api/settings/miniapp-fallback",
	CommandPersona:       "/api/settings/persona",
	CommandLanguage:      "telegram-only: the Mini App has its own UI language and does not read the Telegram chat override",
	CommandAsk:           "/api/messages",
	CommandReset:         "/api/context/reset",
	CommandSkip:          "/api/pending/skip",
	CommandUnskip:        "/api/pending/skip",
	CommandHistory:       "/api/history, /api/history/item",
	CommandDocs:          "/api/documents, /api/documents/search, /api/documents/{id}/delete",
	CommandLinks:         "/api/links, /api/links/search, /api/links/item",
	CommandRemind:        "/api/reminders",
	CommandReminders:     "/api/reminders, /api/reminders/{id}",
	CommandTask:          "/api/tasks, /api/tasks/{id}, /api/tasks/cancel",
	CommandTTS:           "telegram-only: audio is synthesized by the client; only the TTS speed setting is exposed",
	CommandExport:        "/api/history/export",
	CommandSearch:        "/api/history/search",
	CommandDelete:        "/api/history/clear",
	CommandStats:         "/api/usage",
	CommandFeedback:      "/api/history/{id}/feedback",
	CommandFeedbackAlias: "/api/history/{id}/feedback",
	CommandUsage:         "/api/usage",
	CommandWhoAmI:        "/api/whoami",
	CommandAdmin:         "telegram-only: admin aggregate metrics are not exposed to the Mini App",
}

// Voice is no longer a Telegram-only capability: the Mini App mic path (POST /api/transcribe) calls the
// same model.Transcriber and the same audio allowlist/signature validation as a Telegram voice note, so
// voice input is now on both clients. See internal/bot/miniapp_voice.go for the shared-path comment.

// TestCommandParityMatrixCoversEveryCommand — registry() is the source of truth; the matrix must list every
// command and no stale names, and every non-exception has to point at an /api/ surface.
func TestCommandParityMatrixCoversEveryCommand(t *testing.T) {
	var dispatcher = &Dispatcher{}
	var registered = map[string]bool{}
	for _, command := range dispatcher.registry() {
		registered[command.Name] = true
		if _, ok := commandParityMatrix[command.Name]; !ok {
			t.Errorf("command %q has no Mini App parity entry (add an /api/... surface or a telegram-only reason)", command.Name)
		}
	}
	var exceptions int
	for name, surface := range commandParityMatrix {
		if !registered[name] {
			t.Errorf("parity matrix lists %q, which registry() no longer serves", name)
			continue
		}
		switch {
		case strings.HasPrefix(surface, "telegram-only: "):
			exceptions++
			if len(strings.TrimPrefix(surface, "telegram-only: ")) < 10 {
				t.Errorf("command %q has a telegram-only entry without a meaningful reason", name)
			}
		case strings.HasPrefix(surface, "/api/"):
			for _, endpoint := range strings.Split(surface, ",") {
				endpoint = strings.TrimSpace(endpoint)
				if !strings.HasPrefix(endpoint, "/api/") {
					t.Errorf("command %q has a malformed endpoint %q", name, endpoint)
				}
			}
		default:
			t.Errorf("command %q has neither an /api/ surface nor a telegram-only reason: %q", name, surface)
		}
	}
	if exceptions != 6 {
		t.Errorf("telegram-only commands = %d, want the 6 documented exceptions (start, help, ping, language, tts, admin)", exceptions)
	}
}
