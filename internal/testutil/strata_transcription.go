package testutil

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Strata) SetTranscription(text string) {
	s.mu.Lock()
	s.transcriptionText = text
	s.mu.Unlock()
}

func (s *Strata) TranscriptionCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptionCalls
}

func (s *Strata) LastTranscriptionModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptionModel
}

func (s *Strata) LastTranscriptionBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptionBytes
}

func (s *Strata) LastTranscriptionFilename() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptionFilename
}

func (s *Strata) LastTranscriptionContentType() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcriptionContentType
}

func (s *Strata) handleTranscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(11 << 20); err != nil {
		http.Error(w, "multipart required", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 10<<20+1))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.transcriptionCalls++
	s.transcriptionModel = r.FormValue("model")
	s.transcriptionFilename = header.Filename
	s.transcriptionContentType = header.Header.Get("Content-Type")
	s.transcriptionBytes = len(data)
	s.lastAuth = r.Header.Get("Authorization")
	text := s.transcriptionText
	s.mu.Unlock()
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if text == "" {
		text = "текст голосового сообщения"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": text})
}
