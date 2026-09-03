package perfmetrics

import "sync/atomic"

type Store interface {
	Record(sample Sample)
	Query(params QueryParams) (QueryResult, error)
}

type Sample struct {
	Model            string
	Group            string
	LatencyMs        int64
	TtftMs           int64
	HasTtft          bool
	Success          bool
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CacheObserved    bool
	OutputTokens     int64
	GenerationMs     int64
}

type QueryParams struct {
	Model string
	Group string
	Hours int
}

type BucketPoint struct {
	Ts               int64    `json:"ts"`
	RequestCount     int64    `json:"request_count"`
	SuccessCount     int64    `json:"success_count"`
	InputTokens      int64    `json:"input_tokens,omitempty"`
	CacheReadTokens  int64    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64    `json:"cache_write_tokens,omitempty"`
	CacheRequests    int64    `json:"cache_observed_requests,omitempty"`
	CacheHitRate     *float64 `json:"cache_hit_rate,omitempty"`
	TotalLatencyMs   int64    `json:"total_latency_ms"`
	TtftSumMs        int64    `json:"ttft_sum_ms"`
	TtftCount        int64    `json:"ttft_count"`
	OutputTokens     int64    `json:"output_tokens"`
	GenerationMs     int64    `json:"generation_ms"`
	AvgTtftMs        int64    `json:"avg_ttft_ms"`
	AvgLatencyMs     int64    `json:"avg_latency_ms"`
	SuccessRate      float64  `json:"success_rate"`
	AvgTps           float64  `json:"avg_tps"`
}

type GroupResult struct {
	Group            string        `json:"group"`
	RequestCount     int64         `json:"request_count"`
	SuccessCount     int64         `json:"success_count"`
	InputTokens      int64         `json:"input_tokens,omitempty"`
	CacheReadTokens  int64         `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64         `json:"cache_write_tokens,omitempty"`
	CacheRequests    int64         `json:"cache_observed_requests,omitempty"`
	CacheHitRate     *float64      `json:"cache_hit_rate,omitempty"`
	TotalLatencyMs   int64         `json:"total_latency_ms"`
	TtftSumMs        int64         `json:"ttft_sum_ms"`
	TtftCount        int64         `json:"ttft_count"`
	OutputTokens     int64         `json:"output_tokens"`
	GenerationMs     int64         `json:"generation_ms"`
	AvgTtftMs        int64         `json:"avg_ttft_ms"`
	AvgLatencyMs     int64         `json:"avg_latency_ms"`
	SuccessRate      float64       `json:"success_rate"`
	AvgTps           float64       `json:"avg_tps"`
	Series           []BucketPoint `json:"series"`
}

type QueryResult struct {
	ModelName    string        `json:"model_name"`
	SeriesSchema string        `json:"series_schema"`
	Groups       []GroupResult `json:"groups"`
}

type ModelSummary struct {
	ModelName          string    `json:"model_name"`
	TotalLatencyMs     int64     `json:"total_latency_ms"`
	TtftSumMs          int64     `json:"ttft_sum_ms"`
	TtftCount          int64     `json:"ttft_count"`
	OutputTokens       int64     `json:"output_tokens"`
	GenerationMs       int64     `json:"generation_ms"`
	AvgTtftMs          int64     `json:"avg_ttft_ms"`
	AvgLatencyMs       int64     `json:"avg_latency_ms"`
	SuccessRate        float64   `json:"success_rate"`
	AvgTps             float64   `json:"avg_tps"`
	RecentSuccessRates []float64 `json:"recent_success_rates,omitempty"`
	RequestCount       int64     `json:"request_count"`
	InputTokens        int64     `json:"input_tokens,omitempty"`
	CacheReadTokens    int64     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int64     `json:"cache_write_tokens,omitempty"`
	CacheRequests      int64     `json:"cache_observed_requests,omitempty"`
	CacheHitRate       *float64  `json:"cache_hit_rate,omitempty"`
}

type SummaryAllResult struct {
	Models     []ModelSummary `json:"models"`
	GroupOrder []string       `json:"group_order"`
}

type bucketKey struct {
	model    string
	group    string
	bucketTs int64
}

type counters struct {
	requestCount     int64
	successCount     int64
	totalLatencyMs   int64
	ttftSumMs        int64
	ttftCount        int64
	outputTokens     int64
	generationMs     int64
	inputTokens      int64
	cacheReadTokens  int64
	cacheWriteTokens int64
	cacheRequests    int64
}

type atomicBucket struct {
	requestCount     atomic.Int64
	successCount     atomic.Int64
	totalLatencyMs   atomic.Int64
	ttftSumMs        atomic.Int64
	ttftCount        atomic.Int64
	outputTokens     atomic.Int64
	generationMs     atomic.Int64
	inputTokens      atomic.Int64
	cacheReadTokens  atomic.Int64
	cacheWriteTokens atomic.Int64
	cacheRequests    atomic.Int64
}

func (b *atomicBucket) add(sample Sample) {
	b.requestCount.Add(1)
	if sample.Success {
		b.successCount.Add(1)
	}
	if sample.LatencyMs > 0 {
		b.totalLatencyMs.Add(sample.LatencyMs)
	}
	if sample.HasTtft && sample.TtftMs >= 0 {
		b.ttftSumMs.Add(sample.TtftMs)
		b.ttftCount.Add(1)
	}
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		b.outputTokens.Add(sample.OutputTokens)
		b.generationMs.Add(sample.GenerationMs)
	}
	if sample.Success && sample.CacheObserved && sample.InputTokens > 0 {
		b.inputTokens.Add(sample.InputTokens)
		if sample.CacheReadTokens > 0 {
			b.cacheReadTokens.Add(sample.CacheReadTokens)
		}
		if sample.CacheWriteTokens > 0 {
			b.cacheWriteTokens.Add(sample.CacheWriteTokens)
		}
		b.cacheRequests.Add(1)
	}
}

func (b *atomicBucket) snapshot() counters {
	return counters{
		requestCount:     b.requestCount.Load(),
		successCount:     b.successCount.Load(),
		totalLatencyMs:   b.totalLatencyMs.Load(),
		ttftSumMs:        b.ttftSumMs.Load(),
		ttftCount:        b.ttftCount.Load(),
		outputTokens:     b.outputTokens.Load(),
		generationMs:     b.generationMs.Load(),
		inputTokens:      b.inputTokens.Load(),
		cacheReadTokens:  b.cacheReadTokens.Load(),
		cacheWriteTokens: b.cacheWriteTokens.Load(),
		cacheRequests:    b.cacheRequests.Load(),
	}
}

func (b *atomicBucket) drain() counters {
	return counters{
		requestCount:     b.requestCount.Swap(0),
		successCount:     b.successCount.Swap(0),
		totalLatencyMs:   b.totalLatencyMs.Swap(0),
		ttftSumMs:        b.ttftSumMs.Swap(0),
		ttftCount:        b.ttftCount.Swap(0),
		outputTokens:     b.outputTokens.Swap(0),
		generationMs:     b.generationMs.Swap(0),
		inputTokens:      b.inputTokens.Swap(0),
		cacheReadTokens:  b.cacheReadTokens.Swap(0),
		cacheWriteTokens: b.cacheWriteTokens.Swap(0),
		cacheRequests:    b.cacheRequests.Swap(0),
	}
}

func (b *atomicBucket) addCounters(c counters) {
	if c.requestCount != 0 {
		b.requestCount.Add(c.requestCount)
	}
	if c.successCount != 0 {
		b.successCount.Add(c.successCount)
	}
	if c.totalLatencyMs != 0 {
		b.totalLatencyMs.Add(c.totalLatencyMs)
	}
	if c.ttftSumMs != 0 {
		b.ttftSumMs.Add(c.ttftSumMs)
	}
	if c.ttftCount != 0 {
		b.ttftCount.Add(c.ttftCount)
	}
	if c.outputTokens != 0 {
		b.outputTokens.Add(c.outputTokens)
	}
	if c.generationMs != 0 {
		b.generationMs.Add(c.generationMs)
	}
	if c.inputTokens != 0 {
		b.inputTokens.Add(c.inputTokens)
	}
	if c.cacheReadTokens != 0 {
		b.cacheReadTokens.Add(c.cacheReadTokens)
	}
	if c.cacheWriteTokens != 0 {
		b.cacheWriteTokens.Add(c.cacheWriteTokens)
	}
	if c.cacheRequests != 0 {
		b.cacheRequests.Add(c.cacheRequests)
	}
}
