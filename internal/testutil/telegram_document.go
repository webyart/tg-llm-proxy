package testutil

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-telegram/bot/models"
)

type SentDocument struct {
	ChatID int64
	Name   string
	MIME   string
	Data   []byte
}

func (s *Telegram) Documents() []SentDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SentDocument, len(s.documents))
	copy(out, s.documents)
	return out
}

func (s *Telegram) handleSendDocument(w http.ResponseWriter, r *http.Request) {
	chatID, err := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "chat_id is required", 0)
		return
	}
	file, header, err := r.FormFile("document")
	if err != nil {
		writeError(w, http.StatusBadRequest, "document is required", 0)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		writeError(w, http.StatusBadRequest, "invalid document", 0)
		return
	}
	s.mu.Lock()
	s.documents = append(s.documents, SentDocument{ChatID: chatID, Name: header.Filename, MIME: header.Header.Get("Content-Type"), Data: append([]byte(nil), data...)})
	s.mu.Unlock()
	writeOK(w, models.Message{ID: int(time.Now().UnixNano()), Date: int(time.Now().Unix()), Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}})
}
