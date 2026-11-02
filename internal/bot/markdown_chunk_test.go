package bot

import (
	"strings"
	"testing"
)

func TestMarkdownChunksKeepCodeFencesBalanced(t *testing.T) {
	cfg := testConfig(t, map[string]string{"PARSE_MODE": "MarkdownV2", "MAX_MESSAGE_CHUNK": "256"})
	d := newTestDispatcher(t, cfg, nil, nil, testStore(t))
	answer := "## Пример\n\n```go\n" + strings.Repeat("fmt.Println(\"точка.\")\n", 80) + "```\n\nЗавершение!"
	chunks := d.chunksOf(answer)
	if len(chunks) < 2 {
		t.Fatal("long code answer must be split into several messages")
	}
	for index, chunk := range chunks {
		if Runes(chunk) > 256 {
			t.Fatalf("chunk %d has %d runes, limit 256", index, Runes(chunk))
		}
		if strings.Count(chunk, "```")%2 != 0 {
			t.Fatalf("chunk %d has an unbalanced code fence: %q", index, chunk)
		}
	}
	joined := strings.Join(chunks, "\n")
	for _, want := range []string{"fmt.Println", "Завершение"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("chunked answer lost %q", want)
		}
	}
}
