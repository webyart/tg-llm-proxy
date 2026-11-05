package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/config"
)

func BenchmarkStreamRefreshNotDue(b *testing.B) {
	r := &streamReplier{}
	d := &Dispatcher{deps: Deps{Cfg: config.Config{
		ParseMode:       ParseMarkdownV2,
		MaxMessageChunk: 4096,
	}}}
	s := d.newStreamSession(context.Background(), 106, r)
	s.interval = time.Hour
	s.answer.WriteString(strings.Repeat("**жирный** текст со [ссылкой](https://example.com) и `кодом`. ", 100))
	s.post()
	s.rotate()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.refresh()
	}
}

type benchStreamSender struct{ next int64 }

func (s *benchStreamSender) SendStreamMessage(context.Context, int64, string) (int64, error) {
	s.next++
	return s.next, nil
}

func (s *benchStreamSender) EditStreamMessage(context.Context, int64, int64, string, bool) error {
	return nil
}

func BenchmarkStreamRefreshDue(b *testing.B) {
	r := &benchStreamSender{}
	d := &Dispatcher{deps: Deps{Cfg: config.Config{
		ParseMode:       ParseMarkdownV2,
		MaxMessageChunk: 256,
	}}}
	s := d.newStreamSession(context.Background(), 106, r)
	s.interval = 0
	s.answer.WriteString(strings.Repeat("**жирный** текст со [ссылкой](https://example.com) и `кодом`. ", 100))
	s.post()
	s.rotate()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.refresh(); err != nil {
			b.Fatal(err)
		}
	}
}
