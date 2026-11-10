package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
)

const (
	settingsModelLimit           = 50
	settingsModelCacheTTL        = 45 * time.Second
	settingsModelProviderTimeout = 5 * time.Second
)

var errSettingsModelsUnavailable = errors.New("provider model list unavailable")

type modelListCache struct {
	mu              sync.Mutex
	available       []string
	fetchedAt       time.Time
	valid           bool
	refreshing      bool
	done            chan struct{}
	resultErr       error
	providerTimeout time.Duration
}

func cloneModelList(models []string) []string {
	return append([]string{}, models...)
}

func (a *App) providerModels(ctx context.Context) ([]string, error) {
	if a.model == nil {
		return nil, errSettingsModelsUnavailable
	}
	cache := &a.modelCache
	cache.mu.Lock()
	if cache.valid && time.Since(cache.fetchedAt) < settingsModelCacheTTL {
		available := cloneModelList(cache.available)
		cache.mu.Unlock()
		return available, nil
	}
	if cache.refreshing {
		done := cache.done
		cache.mu.Unlock()
		select {
		case <-done:
			cache.mu.Lock()
			available := cloneModelList(cache.available)
			valid := cache.valid
			err := cache.resultErr
			cache.mu.Unlock()
			if err != nil && !valid {
				return nil, err
			}
			return available, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cache.refreshing = true
	cache.done = make(chan struct{})
	done := cache.done
	stale := cloneModelList(cache.available)
	hadStale := cache.valid
	timeout := cache.providerTimeout
	cache.mu.Unlock()
	if timeout <= 0 {
		timeout = settingsModelProviderTimeout
	}

	providerCtx, cancel := context.WithTimeout(context.Background(), timeout)
	available, err := a.model.Models(providerCtx)
	cancel()

	cache.mu.Lock()
	cache.resultErr = err
	if err == nil {
		cache.available = cloneModelList(available)
		cache.fetchedAt = time.Now()
		cache.valid = true
	}
	cache.refreshing = false
	close(done)
	cache.mu.Unlock()
	if err != nil {
		if hadStale {
			return stale, err
		}
		return nil, err
	}
	return cloneModelList(available), nil
}

type settingsResponse struct {
	Warning string `json:"warning,omitempty"`
	// IsAdmin tells the Mini App whether to render the admin-only "Chat limits" section. It answers from
	// the same TELEGRAM_ADMIN_USERS allowlist the admin API enforces, so hiding the section is only a UI
	// convenience and never the access control.
	IsAdmin bool `json:"is_admin"`
	Model   struct {
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
	MiniAppFallback struct {
		Enabled bool   `json:"enabled"`
		Source  string `json:"source"`
	} `json:"miniapp_fallback"`
	TTS struct {
		Enabled bool      `json:"enabled"`
		Current float64   `json:"current"`
		Default float64   `json:"default"`
		Choices []float64 `json:"choices"`
	} `json:"tts"`
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
	selected, found, err := store.ChatModel(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings model", err)
		return
	}
	available, modelsErr := a.providerModels(req.Context())
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
	miniAppFallback, miniAppFallbackFound, err := store.ChatMiniAppFallback(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings Mini App fallback", err)
		return
	}
	ttsSpeed, err := bot.TTSSpeed(req.Context(), a.db, current.ChatID)
	if err != nil {
		a.queryFailed(w, "settings TTS speed", err)
		return
	}

	var response settingsResponse
	response.IsAdmin = a.cfg.IsAdmin(current.UserID)
	if modelsErr != nil {
		response.Warning = "models_unavailable"
	}
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
	response.MiniAppFallback.Enabled = a.cfg.MiniAppTelegramFallback
	response.MiniAppFallback.Source = "global"
	if miniAppFallbackFound {
		response.MiniAppFallback.Enabled = miniAppFallback
		response.MiniAppFallback.Source = "chat"
	}
	response.TTS.Enabled = a.cfg.TTSEnabled
	response.TTS.Current = ttsSpeed
	response.TTS.Default = 1
	response.TTS.Choices = []float64{0.75, 1, 1.25, 1.5}
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

// settingsModelAllowed validates a model id against the aggregated catalogue. A provider-prefixed id
// ("kimi:kimi-k3") is listed by the router exactly as configured, and the second check proves the model is in
// THAT provider's list instead of trusting a colliding id the default endpoint might advertise.
func (a *App) settingsModelAllowed(available []string, requested string) bool {
	var listed = false
	for _, name := range available {
		if requested == name {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	var provider, selected, prefixed = strings.Cut(requested, ":")
	if !prefixed || provider == "strata" {
		return true
	}
	var selectedTrimmed = strings.TrimSpace(selected)
	if selectedTrimmed == "" {
		return false
	}
	for _, configured := range a.cfg.ModelProviders {
		if configured.Name != provider {
			continue
		}
		for _, model := range configured.Models {
			if strings.TrimSpace(model) == selectedTrimmed {
				return true
			}
		}
		return false
	}
	return false
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
		if input.Model == nil || strings.TrimSpace(*input.Model) == "" {
			writeError(w, http.StatusBadRequest, "invalid_model", "Choose a model from the current provider list.")
			return
		}
		requested := strings.TrimSpace(*input.Model)
		available, err := a.providerModels(req.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "models_unavailable", "The current provider model list could not be read.")
			return
		}
		if !a.settingsModelAllowed(available, requested) {
			writeError(w, http.StatusBadRequest, "invalid_model", "Choose a model from the current provider list (provider ids are name:model).")
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

func (a *App) settingsMiniAppFallback(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPut && req.Method != http.MethodDelete {
		methodAllowed(w, http.MethodPut, http.MethodDelete)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "Mini App fallback settings do not accept query parameters.")
		return
	}
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
			writeError(w, http.StatusBadRequest, "invalid_miniapp_fallback", "Set enabled to true or false.")
			return
		}
		if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
			a.queryFailed(w, "settings ensure chat", err)
			return
		}
		if err := store.SetChatMiniAppFallback(req.Context(), a.db, current.ChatID, *input.Enabled); err != nil {
			a.queryFailed(w, "settings set Mini App fallback", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		if status, ok := emptySettingsBody(req); !ok {
			settingsDecodeError(w, status)
			return
		}
		if err := store.ResetChatMiniAppFallback(req.Context(), a.db, current.ChatID); err != nil {
			a.queryFailed(w, "settings reset Mini App fallback", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func validTTSSpeed(value string) bool {
	switch value {
	case "0.75", "1", "1.25", "1.5":
		return true
	default:
		return false
	}
}

func (a *App) settingsTTSSpeed(w http.ResponseWriter, req *http.Request, current scope) {
	if req.Method != http.MethodPut && req.Method != http.MethodDelete {
		methodAllowed(w, http.MethodPut, http.MethodDelete)
		return
	}
	if req.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "TTS speed settings do not accept query parameters.")
		return
	}
	switch req.Method {
	case http.MethodPut:
		var input struct {
			Speed *string `json:"speed"`
		}
		if status, ok := decodeSettingsJSON(req, &input); !ok {
			settingsDecodeError(w, status)
			return
		}
		if input.Speed == nil || !validTTSSpeed(*input.Speed) {
			writeError(w, http.StatusBadRequest, "invalid_tts_speed", "Set speed to one of 0.75, 1, 1.25, 1.5.")
			return
		}
		if err := store.EnsureChat(req.Context(), a.db, current.ChatID, current.UserID, ""); err != nil {
			a.queryFailed(w, "settings ensure chat", err)
			return
		}
		if _, err := bot.SetTTSSpeed(req.Context(), a.db, current.ChatID, *input.Speed); err != nil {
			a.queryFailed(w, "settings set TTS speed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodDelete:
		if status, ok := emptySettingsBody(req); !ok {
			settingsDecodeError(w, status)
			return
		}
		key := "tts_speed:" + strconv.FormatInt(current.ChatID, 10)
		if _, err := a.db.ExecContext(req.Context(), `DELETE FROM bot_state WHERE key = ?`, key); err != nil {
			a.queryFailed(w, "settings reset TTS speed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
