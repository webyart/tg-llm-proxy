package webapp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"bot/internal/bot"
	"bot/internal/store"
)

const settingsModelLimit = 50

type settingsResponse struct {
	Model struct {
		Current            string   `json:"current"`
		Default            string   `json:"default"`
		Source             string   `json:"source"`
		Available          []string `json:"available"`
		AvailableTruncated bool     `json:"available_truncated"`
	} `json:"model"`
	Persona struct {
		Current string `json:"current"`
		Custom  bool   `json:"custom"`
		Source  string `json:"source"`
		Presets []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"presets"`
	} `json:"persona"`
	Stream struct {
		Enabled bool   `json:"enabled"`
		Source  string `json:"source"`
	} `json:"stream"`
}

func methodAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
}

func (a *App) settings(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodGet {
		methodAllowed(w, http.MethodGet)
		return
	}
	if a.model == nil {
		writeError(w, http.StatusServiceUnavailable, "models_unavailable", "The provider model list is unavailable.")
		return
	}
	selected, found, err := store.ChatModel(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings model", err)
		return
	}
	available, err := a.model.Models(req.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "models_unavailable", "The current provider model list could not be read.")
		return
	}
	personaPrompt, personaFound, err := store.ChatPersona(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings persona", err)
		return
	}
	streamOverride, streamFound, err := store.ChatStreaming(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings stream", err)
		return
	}

	var response settingsResponse
	response.Model.Current = a.cfg.StrataModel
	response.Model.Default = a.cfg.StrataModel
	response.Model.Source = "global"
	if found {
		response.Model.Current = selected
		response.Model.Source = "chat"
	}
	response.Model.AvailableTruncated = len(available) > settingsModelLimit
	if response.Model.AvailableTruncated {
		available = available[:settingsModelLimit]
	}
	response.Model.Available = append([]string{}, available...)
	response.Persona.Current = "default"
	response.Persona.Source = "global"
	if personaFound {
		response.Persona.Current = "custom"
		response.Persona.Source = "chat"
		for _, preset := range bot.PersonaPresetDefinitions() {
			if preset.Prompt == personaPrompt {
				response.Persona.Current = preset.Name
				break
			}
		}
	}
	response.Persona.Custom = response.Persona.Current == "custom"
	for _, preset := range bot.PersonaPresetDefinitions() {
		response.Persona.Presets = append(response.Persona.Presets, struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}{Name: preset.Name, Description: preset.Description})
	}
	response.Stream.Enabled = a.cfg.Streaming
	response.Stream.Source = "global"
	if streamFound {
		response.Stream.Enabled = streamOverride
		response.Stream.Source = "chat"
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeSettingsJSON(req *http.Request, destination any) (int, bool) {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return http.StatusUnsupportedMediaType, false
	}
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, false
		}
		return http.StatusBadRequest, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return http.StatusBadRequest, false
	}
	return 0, true
}

func emptySettingsBody(req *http.Request) (int, bool) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, false
		}
		return http.StatusBadRequest, false
	}
	if len(body) != 0 {
		return http.StatusBadRequest, false
	}
	return 0, true
}

func settingsDecodeError(w http.ResponseWriter, status int) {
	if status == http.StatusUnsupportedMediaType {
		writeError(w, status, "content_type", "Send a strict application/json body.")
		return
	}
	if status == http.StatusRequestEntityTooLarge {
		writeError(w, status, "body_too_large", "Request body is too large.")
		return
	}
	writeError(w, status, "invalid_json", "Invalid or unknown settings fields.")
}

func (a *App) settingsModel(w http.ResponseWriter, req *http.Request, current scope) {
	switch req.Method {
	case http.MethodPut:
		var input struct {
			Model *string `json:"model"`
		}
		if status, ok := decodeSettingsJSON(req, &input); !ok {
			settingsDecodeError(w, status)
			return
		}
		if input.Model == nil || a.model == nil {
			writeError(w, http.StatusBadRequest, "invalid_model", "Choose a model from the current provider list.")
			return
		}
		requested := strings.TrimSpace(*input.Model)
		available, err := a.model.Models(req.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "models_unavailable", "The current provider model list could not be read.")
			return
		}
		found := false
		for _, name := range available {
			if requested == name {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusBadRequest, "invalid_model", "Choose a model from the current provider list.")
			return
		}
		if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
			a.queryFailed(w, "settings ensure chat", err)
			return
		}
		if err := store.SetChatModel(req.Context(), a.db, current.ChatID, requested); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_model", "The selected model name is invalid.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		if status, ok := emptySettingsBody(req); !ok {
			settingsDecodeError(w, status)
			return
		}
		if err := store.ClearChatModel(req.Context(), a.db, current.ChatID); err != nil {
			a.queryFailed(w, "settings reset model", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		methodAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (a *App) settingsPersona(w http.ResponseWriter, req *http.Request, current scope) {
	switch req.Method {
	case http.MethodPut:
		var input struct {
			Preset *string `json:"preset"`
			Custom *string `json:"custom"`
		}
		if status, ok := decodeSettingsJSON(req, &input); !ok {
			settingsDecodeError(w, status)
			return
		}
		if (input.Preset == nil) == (input.Custom == nil) {
			writeError(w, http.StatusBadRequest, "invalid_persona", "Choose exactly one existing preset or custom persona text.")
			return
		}
		prompt := ""
		if input.Custom != nil {
			prompt = strings.TrimSpace(*input.Custom)
			if prompt == "" || len([]rune(prompt)) > store.MaxPersonaPromptRunes || strings.ContainsRune(prompt, '\x00') {
				writeError(w, http.StatusBadRequest, "invalid_persona", "Custom persona must contain 1 to 4000 runes and no NUL.")
				return
			}
		} else {
			name := strings.ToLower(strings.TrimSpace(*input.Preset))
			for _, preset := range bot.PersonaPresetDefinitions() {
				if name == preset.Name {
					prompt = preset.Prompt
					break
				}
			}
			if prompt == "" {
				writeError(w, http.StatusBadRequest, "invalid_persona", "Choose an existing persona preset.")
				return
			}
		}
		if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
			a.queryFailed(w, "settings ensure chat", err)
			return
		}
		if err := store.SetChatPersona(req.Context(), a.db, current.ChatID, prompt); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_persona", "The persona text is invalid.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		if status, ok := emptySettingsBody(req); !ok {
			settingsDecodeError(w, status)
			return
		}
		if err := store.ClearChatPersona(req.Context(), a.db, current.ChatID); err != nil {
			a.queryFailed(w, "settings reset persona", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		methodAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (a *App) settingsStream(w http.ResponseWriter, req *http.Request, current scope) {
	switch req.Method {
	case http.MethodPut:
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if status, ok := decodeSettingsJSON(req, &input); !ok {
			settingsDecodeError(w, status)
			return
		}
		if input.Enabled == nil {
			writeError(w, http.StatusBadRequest, "invalid_stream", "Set enabled to true or false.")
			return
		}
		if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
			a.queryFailed(w, "settings ensure chat", err)
			return
		}
		if err := store.SetChatStreaming(req.Context(), a.db, current.ChatID, *input.Enabled); err != nil {
			a.queryFailed(w, "settings set stream", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		if status, ok := emptySettingsBody(req); !ok {
			settingsDecodeError(w, status)
			return
		}
		if err := store.ClearChatStreaming(req.Context(), a.db, current.ChatID); err != nil {
			a.queryFailed(w, "settings reset stream", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		methodAllowed(w, http.MethodPut, http.MethodDelete)
	}
}
