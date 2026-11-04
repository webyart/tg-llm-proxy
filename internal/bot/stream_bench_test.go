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
