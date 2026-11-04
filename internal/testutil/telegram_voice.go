package testutil

import (
	"time"

	"github.com/go-telegram/bot/models"
)

func (s *Telegram) PushVoice(updateID, chatID, userID int64, fileID string, duration int) {
	s.mu.Lock()
	size := s.files[fileID].File.FileSize
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()),
		Chat:  models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:  &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		Voice: &models.Voice{FileID: fileID, MimeType: "audio/ogg", FileSize: size, Duration: duration},
	}})
}

func (s *Telegram) PushAudio(updateID, chatID, userID int64, fileID string, duration int) {
	s.mu.Lock()
	size := s.files[fileID].File.FileSize
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()),
		Chat:  models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:  &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		Audio: &models.Audio{FileID: fileID, MimeType: "audio/mpeg", FileSize: size, Duration: duration},
	}})
}

func (s *Telegram) PushVideoNote(updateID, chatID, userID int64, fileID string, duration int) {
	s.mu.Lock()
	size := int(s.files[fileID].File.FileSize)
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()),
		Chat:      models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:      &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		VideoNote: &models.VideoNote{FileID: fileID, FileSize: size, Duration: duration},
	}})
}

func (s *Telegram) PushPhotoGroup(updateID, chatID, userID int64, fileID, mediaGroupID, caption string) {
	s.mu.Lock()
	size := int(s.files[fileID].File.FileSize)
	s.mu.Unlock()
	s.Push(models.Update{ID: updateID, Message: &models.Message{
		ID: int(updateID), Date: int(time.Now().Unix()), Caption: caption, MediaGroupID: mediaGroupID,
		Chat:  models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		From:  &models.User{ID: userID, IsBot: false, FirstName: "Tester", Username: "tester"},
		Photo: []models.PhotoSize{{FileID: fileID, Width: 2, Height: 2, FileSize: size}},
	}})
}
