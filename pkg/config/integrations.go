package config

// EnterpriseRateLimitConfig controls request admission, independently of Feed cadence.
type EnterpriseRateLimitConfig struct {
	Mode              string
	RequestsPerIP     int
	RequestsTotal     int
	RequestsPerApp    int
	ControlReservePct int
	TrustedProxyCIDRs []string
}

func loadEnterpriseRateLimits() EnterpriseRateLimitConfig {
	return EnterpriseRateLimitConfig{
		Mode:              getEnv("ENTERPRISE_RATE_LIMIT_MODE", "off"),
		RequestsPerIP:     getEnvInt("ENTERPRISE_RATE_LIMIT_IP_PER_MINUTE", 600),
		RequestsTotal:     getEnvInt("ENTERPRISE_RATE_LIMIT_TOTAL_PER_MINUTE", 6000),
		RequestsPerApp:    getEnvInt("ENTERPRISE_RATE_LIMIT_APP_PER_MINUTE", 600),
		ControlReservePct: getEnvInt("ENTERPRISE_RATE_LIMIT_CONTROL_RESERVE_PERCENT", 50),
		TrustedProxyCIDRs: getEnvStringList("ENTERPRISE_RATE_LIMIT_TRUSTED_PROXY_CIDRS", nil),
	}
}
