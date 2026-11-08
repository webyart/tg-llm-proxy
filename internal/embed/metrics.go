package embed

import (
	"bot/internal/metrics"
)

// Metrics instruments embedding requests. A nil *Metrics is a no-op, so a client built without a registry (the
// CLI, every unit test) stays silent.
type Metrics struct {
	calls      *metrics.Counter
	dimensions *metrics.Gauge
}

// NewMetrics registers the embedding metric families on reg: one counter by outcome and the length of the last
// vector, which is how an operator notices a model swap that changed the dimension.
func NewMetrics(reg *metrics.Registry) *Metrics {
	if reg == nil {
		return nil
	}
	return &Metrics{
		calls:      reg.Counter("rag_embeddings_total", "Embedding requests of the RAG hybrid search by outcome."),
		dimensions: reg.Gauge("rag_embedding_dimensions", "Dimensions of the last successful embedding vector."),
	}
}

// observe records one request. It counts the request, not the batch inside it: a document of a hundred chunks is
// two requests and two increments.
func (m *Metrics) observe(err error, dimensions int) {
	if m == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	m.calls.Add(map[string]string{"outcome": outcome}, 1)
	if err == nil && dimensions > 0 {
		m.dimensions.Set(nil, float64(dimensions))
	}
}
