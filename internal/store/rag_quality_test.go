package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The RAG quality corpus is a fixed synthetic base used to measure ranking quality instead of guessing it.
// It deliberately mixes Russian and English, repeats topic vocabulary across documents, gives one document
// a name that is its only match, and keeps one document long enough that PutRAGDocument has to chunk it.
//
// The corpus has 8 documents and 41 chunks; the query set has 19 answerable entries and one no-answer entry.
// Baseline measured on the current implementation (v1.46.0, bm25 candidate union + BM25-lite scorer):
//
//	recall@1 = 1.000  recall@3 = 1.000  recall@5 = 1.000
//	MRR@1    = 1.000  MRR@3    = 1.000  MRR@5    = 1.000
//
// The pinned floors are recall@1 >= 0.90, recall@3 >= 0.95, recall@5 >= 0.95 and MRR@5 >= 0.85. With 19
// answerable queries that tolerates one top-1 miss, no top-3 miss, and two answerable queries whose first
// relevant chunk is outside the top five (MRR@5 = 17/19 = 0.895), so the baseline has a real, small margin.

const (
	ragQualityChatID = 9101
	ragQualityUserID = 11

	// Sections are padded into this rune range so every section becomes roughly one chunk: a section is
	// longer than the 500-rune minimum sentence-boundary search window and shorter than RAGChunkRunes, so
	// PutRAGDocument splits the document at section boundaries instead of mid-sentence.
	ragQualitySectionMinRunes = 720
	ragQualitySectionMaxRunes = 900
)

// ragQualityFiller is neutral prose added to reach the section length. It deliberately avoids every term the
// query set searches for, so padding can never make an unrelated chunk look relevant.
var ragQualityFiller = []string{
	"Этот раздел фиксирует общий порядок работы команды и не содержит секретов.",
	"Материал проверен, внешние ссылки убраны, примеры упрощены до безопасных.",
	"Любое изменение согласуется с владельцем раздела и записывается в журнал.",
	"Инструкция пересматривается раз в квартал, устаревшие шаги удаляются.",
	"Если шаг непонятен, спросите дежурного и обновите текст после разбора.",
	"Формулировки намеренно короткие, чтобы их можно было проверить на месте.",
	"Все имена и адреса в примерах вымышлены и не относятся к рабочей среде.",
	"Сроки и ответственные указаны в конце раздела, отдельно от описания шагов.",
	"Проверка выполняется по списку, а результат записывается одной строкой.",
	"Ссылки на внутренние системы заменены условными обозначениями.",
	"Если раздел устарел, создайте задачу и укажите, какой шаг изменился.",
}

type ragQualityDoc struct {
	name     string
	sections []string
}

type ragQualityQuery struct {
	query     string
	wantDoc   string
	wantChunk string
	note      string
}

// answerable reports whether the query is expected to find a chunk. The single no-answer entry has no wantDoc.
func (q ragQualityQuery) answerable() bool { return q.wantDoc != "" }

// ragQualityCorpus returns the eight fixed documents. Five sections per document give four to six chunks
// after chunking (six sections for the long onboarding guide), and every answer substring lives inside one
// sentence so a chunk boundary can never cut it in half.
func ragQualityCorpus(tb testing.TB) []ragQualityDoc {
	tb.Helper()
	return []ragQualityDoc{
		ragQualityDocOf(tb, "deploy-bot-playbook.md",
			"Плейбук описывает порядок деплоя: сначала собирается образ, затем обновляется конфигурация и запускается проверка здоровья бота. Релиз выполняется только после зелёного CI и короткого подтверждения релиз-менеджера.",
			"Перезапуск деплой бота выполняет дежурный: systemctl restart bot.service, а journalctl -u bot.service показывает последние строки. Unit-файл сервиса лежит в каталоге systemd и запускает один процесс.",
			"Конфигурация релиза хранится рядом с юнитом сервиса и не содержит ключей. После деплоя бота проверяются логи, метрики здоровья и отсутствие ошибок в журнале.",
			"Откат готовится заранее: предыдущий образ сохраняется, а канареечный трафик снимается. Деплой считается завершённым только после сверки версии бота и подтверждения наблюдаемости.",
			"Чеклист приёмки включает домен, сертификат, переменные окружения и smoke-тест бота. Если smoke-тест падает, релиз останавливается и деплой возвращается к предыдущей версии.",
		),
		ragQualityDocOf(tb, "deploy-gateway-runbook.md",
			"Runbook описывает порядок деплоя gateway: сначала проверяется конфигурация, затем снимается трафик и обновляется образ. Gateway работает за балансировщиком и не хранит состояние.",
			"Деплой gateway выполняется командой nginx -s reload после проверки конфигурации. Nginx держит upstream backend и перенаправляет запросы на здоровые узлы.",
			"Маршрутизация gateway описана в nginx.conf: location-блоки разделяют API и статику. Изменение маршрутов сначала проверяется на стенде, затем выкатывается на production.",
			"Upstream nginx настраивается отдельно: таймауты, keepalive и резервный backend. При деплое gateway проверяется, что upstream видит все узлы.",
			"Конфигурация хранится в репозитории сервиса gateway и проходит ревью. Изменение конфигурации сначала проверяется на стенде, затем выкатывается на production.",
		),
		ragQualityDocOf(tb, "incident-2026-02-14.md",
			"Инцидент начался после планового деплоя gateway: графики показали рост задержки, но ошибки в логах появились позже. Дежурный открыл мост и зафиксировал время начала.",
			"Инцидент latency: ошибка gateway возвращала 502 на части запросов, p99 вырос с 120 до 900 мс. После отката конфигурации latency вернулась к норме.",
			"Rollback после деплоя занял четыре минуты: предыдущий образ gateway был поднят, трафик вернулся, p99 снизился. Причина сбоя подтверждена и вынесена в отдельный тикет.",
			"Инцидент показал, что алерты сработали позже пользователей. Gateway не был перегружен, а узкое место находилось в базе.",
			"По итогам инцидента обновили runbook: проверка latency добавлена в smoke-тест, а релиз gateway теперь требует подтверждения SRE.",
		),
		ragQualityDocOf(tb, "billing-reconciliation.md",
			"Регламент описывает сверку: бухгалтер выгружает реестр, аналитик сверяет суммы и фиксирует расхождения. Платежи сверяются по идентификатору транзакции и дате.",
			"Сверка платежей выполняется по invoice: каждая строка сверяется с реестром, а расхождения попадают в отчёт. Invoice reconciliation завершается только после подтверждения финансов.",
			"Платежи с расхождением не удаляются: они помечаются и передаются на ручную проверку. Повторная сверка возможна после исправления invoice.",
			"Отчёт по сверке хранится семь лет, доступ к нему имеют бухгалтерия и аудит. Выгрузка invoice содержит суммы, даты и идентификаторы.",
			"Сверка закрывается актом: подписи сторон подтверждают, что расхождений не осталось. Копия акта хранится вместе с реестром платежей.",
		),
		ragQualityDocOf(tb, "zephyr-notes.md",
			"Заметки встречи: команда обсудила календарь релизов и договорилась перенести демо на следующую неделю. Задачи записаны в общий трекер.",
			"Заметки по обсуждению: дизайн согласован, тексты будут готовы к пятнице. Вопросы к продукту собраны отдельно и переданы владельцу.",
			"Заметки встречи с поддержкой: самые частые вопросы касаются входа и уведомлений. Ответы добавлены в базу знаний.",
			"Заметки и решения: отпуска согласованы, дежурство на праздники распределено. Следующая встреча назначена на вторник.",
			"Заметки по итогам: риски перечислены, владельцы назначены, сроки зафиксированы. Открытые вопросы вернутся на следующую встречу.",
		),
		ragQualityDocOf(tb, "onboarding-guide.md",
			"Онбординг начинается с учётной записи и почты: новичок получает логин, временный пароль и доступ к календарю. Первая неделя расписана по дням.",
			"VPN доступ выдаётся отделом инфраструктуры: ключи и профиль приходят на рабочую почту. Без VPN доступ к внутренним системам закрыт.",
			"Онбординг включает знакомство с командой и первое задание. Наставник показывает репозиторий и правила ревью.",
			"Онбординг чеклист закрывается в конце второй недели: доступы, оборудование, обучение и обратная связь. Чеклист хранится в трекере.",
			"Оборудование выдаётся после подписания соглашения: ноутбук, монитор и токен. Если оборудование не готово, онбординг продлевается.",
			"Обратная связь по онбордингу собирается через опрос: вопросы касаются доступов, наставника и первых задач. Результаты разбираются на ретро.",
		),
		ragQualityDocOf(tb, "search-ranking-design.md",
			"Дизайн поиска: ранжирование учитывает покрытие терминов, длину chunk и бонус за точную фразу. Поиск по документам возвращает top-k фрагментов.",
			"Скор задаётся формулой BM25-lite: idf, насыщение частоты и нормализация длины. Поиск сортирует кандидатов по убыванию score.",
			"BM25 ранжирование сравнивается с эталоном: recall@3 и MRR показывают, не ухудшился ли поиск. Эталонный корпус фиксирован, поэтому изменение chunking видно сразу.",
			"Recall chunk считается по первому релевантному фрагменту, а не по числу совпавших слов. Recall@5 смотрит, попал ли нужный chunk в пятёрку.",
			"Ограничение кандидатов bm25 не должно менять порядок лучших фрагментов. Поиск проверяет, что редкий термин не вытесняется частым.",
		),
		ragQualityDocOf(tb, "monitoring-notes.md",
			"Мониторинг сервиса строится на метриках: задержка, ошибки и насыщение. Метрики собираются каждые пятнадцать секунд и хранятся тридцать дней.",
			"Конфигурация сервиса мониторинга описана в репозитории: скрейп-интервалы, правила записи и retention. Изменение конфигурации проходит ревью владельца сервиса.",
			"Алерты и метрики связаны: правило срабатывает, когда задержка выше порога. Алерты дежурятся по расписанию, а метрики служат доказательством.",
			"Дашборд мониторинга показывает метрики по сервисам и узлам. Если сбор пропал, на графике виден разрыв, поэтому агент проверяется отдельно.",
			"Метрики ретеншена проверяются раз в месяц: старые серии удаляются, новые правила применяются. Дашборд обновляется вместе с конфигурацией.",
		),
	}
}

func ragQualityDocOf(tb testing.TB, name string, leads ...string) ragQualityDoc {
	tb.Helper()
	doc := ragQualityDoc{name: name, sections: make([]string, len(leads))}
	for index, lead := range leads {
		doc.sections[index] = ragQualitySection(tb, lead)
	}
	return doc
}

func ragQualitySection(tb testing.TB, lead string) string {
	tb.Helper()
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(lead))
	for _, sentence := range ragQualityFiller {
		if utf8.RuneCountInString(builder.String()) >= ragQualitySectionMinRunes {
			break
		}
		builder.WriteByte(' ')
		builder.WriteString(sentence)
	}
	section := builder.String()
	runes := utf8.RuneCountInString(section)
	if runes < ragQualitySectionMinRunes || runes > ragQualitySectionMaxRunes {
		tb.Fatalf("rag quality section has %d runes, want %d..%d: %q", runes, ragQualitySectionMinRunes, ragQualitySectionMaxRunes, lead)
	}
	return section
}

// ragQualityQueries is the fixed query set: exact terms, paraphrases sharing only some terms, a name-only
// match, a term spread across several documents, and one query with no answer at all.
func ragQualityQueries() []ragQualityQuery {
	return []ragQualityQuery{
		{query: "деплой бота", wantDoc: "deploy-bot-playbook.md", wantChunk: "Перезапуск деплой бота", note: "exact Russian phrase"},
		{query: "перезапуск бота systemctl", wantDoc: "deploy-bot-playbook.md", wantChunk: "systemctl restart bot.service", note: "paraphrase with a command term"},
		{query: "bot service unit", wantDoc: "deploy-bot-playbook.md", wantChunk: "bot.service", note: "English exact terms from the same chunk"},
		{query: "деплой gateway", wantDoc: "deploy-gateway-runbook.md", wantChunk: "Деплой gateway", note: "similar topic, distinct object"},
		{query: "маршрутизация gateway", wantDoc: "deploy-gateway-runbook.md", wantChunk: "Маршрутизация gateway", note: "exact phrase"},
		{query: "upstream nginx", wantDoc: "deploy-gateway-runbook.md", wantChunk: "Upstream nginx", note: "English exact phrase"},
		{query: "инцидент latency", wantDoc: "incident-2026-02-14.md", wantChunk: "Инцидент latency", note: "mixed Russian/English"},
		{query: "rollback после деплоя", wantDoc: "incident-2026-02-14.md", wantChunk: "Rollback после деплоя", note: "genitive form must beat деплой"},
		{query: "ошибка gateway", wantDoc: "incident-2026-02-14.md", wantChunk: "ошибка gateway", note: "term shared with the gateway runbook"},
		{query: "сверка платежей", wantDoc: "billing-reconciliation.md", wantChunk: "Сверка платежей", note: "unrelated topic, exact phrase"},
		{query: "invoice reconciliation", wantDoc: "billing-reconciliation.md", wantChunk: "Invoice reconciliation", note: "English exact phrase"},
		// A name-only match has no content answer: any chunk of the named document is correct, and the
		// scorer ranks those chunks by id because their name bonus is identical.
		{query: "zephyr", wantDoc: "zephyr-notes.md", note: "name-only match, single term"},
		{query: "zephyr notes", wantDoc: "zephyr-notes.md", note: "name-only match, two terms"},
		{query: "vpn доступ", wantDoc: "onboarding-guide.md", wantChunk: "VPN доступ", note: "long document, deep chunk"},
		{query: "онбординг чеклист", wantDoc: "onboarding-guide.md", wantChunk: "Онбординг чеклист", note: "long document, another chunk"},
		{query: "bm25 ранжирование", wantDoc: "search-ranking-design.md", wantChunk: "BM25 ранжирование", note: "technical mixed query"},
		{query: "recall chunk", wantDoc: "search-ranking-design.md", wantChunk: "Recall chunk", note: "English technical paraphrase"},
		{query: "конфигурация сервиса", wantDoc: "monitoring-notes.md", wantChunk: "Конфигурация сервиса мониторинга", note: "term present in three documents"},
		{query: "метрики алерты", wantDoc: "monitoring-notes.md", wantChunk: "Алерты и метрики", note: "two terms spread inside one document"},
		{query: "квантовый осьминог", note: "no answer: neither term exists in the corpus"},
	}
}

func ragQualityStore(tb testing.TB, name string) (*sql.DB, int64) {
	tb.Helper()
	ctx := context.Background()
	db, err := OpenMemory(ctx, name)
	if err != nil {
		tb.Fatalf("OpenMemory: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	for _, doc := range ragQualityCorpus(tb) {
		_, chunks, err := PutRAGDocument(ctx, db, ragQualityChatID, ragQualityUserID, doc.name, strings.Join(doc.sections, "\n"))
		if err != nil {
			tb.Fatalf("PutRAGDocument(%s): %v", doc.name, err)
		}
		if chunks < 4 || chunks > 6 {
			tb.Fatalf("document %s produced %d chunks, want 4..6", doc.name, chunks)
		}
	}
	return db, ragQualityChatID
}

// ragQualityRank returns the 1-based rank and score of the first chunk that belongs to the wanted document
// and contains the wanted answer substring. A rank of 0 means the answer did not reach the result limit.
func ragQualityRank(chunks []RAGChunk, query ragQualityQuery) (int, int) {
	for index, chunk := range chunks {
		if chunk.Name != query.wantDoc {
			continue
		}
		if query.wantChunk != "" && !strings.Contains(chunk.Content, query.wantChunk) {
			continue
		}
		return index + 1, chunk.Score
	}
	return 0, 0
}

func TestRAGQualityCorpus(t *testing.T) {
	ctx := context.Background()
	db, chatID := ragQualityStore(t, "rag_quality_corpus_"+t.Name())
	queries := ragQualityQueries()

	ranks := make([]int, len(queries))
	t.Logf("%-28s %-28s %4s %6s  %s", "query", "want document", "rank", "score", "note")
	for index, query := range queries {
		result, err := SearchRAGChunksDetailed(ctx, db, chatID, query.query, 5, 1<<20)
		if err != nil {
			t.Fatalf("SearchRAGChunksDetailed(%q): %v", query.query, err)
		}
		if !query.answerable() {
			if len(result.Chunks) != 0 {
				t.Errorf("no-answer query %q returned %d chunks: %+v", query.query, len(result.Chunks), result.Chunks)
			}
			t.Logf("%-28s %-28s %4s %6s  %s", query.query, "-", "none", "-", query.note)
			continue
		}
		rank, score := ragQualityRank(result.Chunks, query)
		ranks[index] = rank
		rankText := "miss"
		if rank > 0 {
			rankText = fmt.Sprintf("%d", rank)
		}
		t.Logf("%-28s %-28s %4s %6d  %s", query.query, query.wantDoc, rankText, score, query.note)
	}

	assertRAGQualityMetrics(t, queries, ranks)

	// The baseline comment at the top of the file carries the measured numbers; these are the pinned floors.
	const (
		minRecallAt1 = 0.90
		minRecallAt3 = 0.95
		minRecallAt5 = 0.95
		minMRRAt5    = 0.85
	)
	for _, threshold := range []struct {
		name  string
		value float64
		want  float64
	}{
		{"recall@1", ragQualityRecall(queries, ranks, 1), minRecallAt1},
		{"recall@3", ragQualityRecall(queries, ranks, 3), minRecallAt3},
		{"recall@5", ragQualityRecall(queries, ranks, 5), minRecallAt5},
		{"MRR@5", ragQualityMRR(queries, ranks, 5), minMRRAt5},
	} {
		if threshold.value < threshold.want {
			t.Errorf("%s = %.3f, want >= %.3f", threshold.name, threshold.value, threshold.want)
		}
	}
}

func TestRAGQualityNoAnswerQuery(t *testing.T) {
	ctx := context.Background()
	db, chatID := ragQualityStore(t, "rag_quality_noanswer_"+t.Name())
	for _, query := range ragQualityQueries() {
		if query.answerable() {
			continue
		}
		result, err := SearchRAGChunksDetailed(ctx, db, chatID, query.query, 5, 1<<20)
		if err != nil {
			t.Fatalf("SearchRAGChunksDetailed(%q): %v", query.query, err)
		}
		if len(result.Chunks) != 0 {
			t.Fatalf("no-answer query %q returned %d chunks: %+v", query.query, len(result.Chunks), result.Chunks)
		}
		if result.ScannedChunks != 0 {
			t.Fatalf("no-answer query %q scanned %d chunks, want 0", query.query, result.ScannedChunks)
		}
		t.Logf("no-answer query %q: 0 chunks, scanned=%d, total=%d", query.query, result.ScannedChunks, result.TotalChunks)
	}
}

func assertRAGQualityMetrics(t *testing.T, queries []ragQualityQuery, ranks []int) {
	t.Helper()
	answerable := 0
	for index, query := range queries {
		if !query.answerable() {
			continue
		}
		answerable++
		if ranks[index] == 0 {
			t.Logf("MISS: %q (wanted %s)", query.query, query.wantDoc)
		}
	}
	if answerable == 0 {
		t.Fatal("the query set has no answerable entries")
	}
	t.Logf("metric table over %d answerable queries:", answerable)
	for _, k := range []int{1, 3, 5} {
		t.Logf("  recall@%d=%.3f  MRR@%d=%.3f", k, ragQualityRecall(queries, ranks, k), k, ragQualityMRR(queries, ranks, k))
	}
	if ragQualityRecall(queries, ranks, 1) > ragQualityRecall(queries, ranks, 3) ||
		ragQualityRecall(queries, ranks, 3) > ragQualityRecall(queries, ranks, 5) {
		t.Errorf("recall is not monotonic across k: %v", ranks)
	}
}

func ragQualityRecall(queries []ragQualityQuery, ranks []int, k int) float64 {
	hits, answerable := 0, 0
	for index, query := range queries {
		if !query.answerable() {
			continue
		}
		answerable++
		if ranks[index] > 0 && ranks[index] <= k {
			hits++
		}
	}
	return float64(hits) / float64(answerable)
}

func ragQualityMRR(queries []ragQualityQuery, ranks []int, k int) float64 {
	total, answerable := 0.0, 0
	for index, query := range queries {
		if !query.answerable() {
			continue
		}
		answerable++
		if ranks[index] > 0 && ranks[index] <= k {
			total += 1 / float64(ranks[index])
		}
	}
	return total / float64(answerable)
}

// BenchmarkRAGQualityCorpus measures the evaluation pass itself (all queries against the fixed corpus), so a
// candidate-limit or scorer change that makes searching slower shows up next to the quality metrics.
func BenchmarkRAGQualityCorpus(b *testing.B) {
	ctx := context.Background()
	db, chatID := ragQualityStore(b, "rag_quality_benchmark")
	queries := ragQualityQueries()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		for _, query := range queries {
			if _, err := SearchRAGChunksDetailed(ctx, db, chatID, query.query, 5, 1<<20); err != nil {
				b.Fatalf("SearchRAGChunksDetailed(%q): %v", query.query, err)
			}
		}
	}
}
