package serviceranker

type ServiceRankerConfig struct {
	SemanticWeight float64
	KeywordWeight  float64
	SuccessWeight  float64
	LatencyWeight  float64
	PriceWeight    float64
	DeadlineWeight float64
	MaxLatencyMs   int64 // normalization ceiling for latency (default 86400000 = 24h)
	MaxPriceAtomic int64 // normalization ceiling for price (default 1000000000)
	MaxDeadlineMs  int64 // normalization ceiling for deadline (default 604800000 = 7d)
}

func DefaultConfig() *ServiceRankerConfig {
	return &ServiceRankerConfig{
		SemanticWeight: 0.55,
		KeywordWeight:  0.15,
		SuccessWeight:  0.15,
		LatencyWeight:  0.07,
		PriceWeight:    0.05,
		DeadlineWeight: 0.03,
		MaxLatencyMs:   86400000,
		MaxPriceAtomic: 1000000000,
		MaxDeadlineMs:  604800000,
	}
}
