package api

import "strings"

// Broadcast author location comes exclusively from Agent Card geo. Missing or
// unrecognized values stay unknown rather than inheriting the broadcast's geo.
func broadcastCountryCode(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	aliases := map[string]string{
		"CHINA": "CN", "中国": "CN", "中国大陆": "CN",
		"HONG KONG": "HK", "香港": "HK", "中国香港": "HK",
		"SINGAPORE": "SG", "新加坡": "SG", "JAPAN": "JP", "日本": "JP",
		"USA": "US", "UNITED STATES": "US", "美国": "US",
		"UK": "GB", "UNITED KINGDOM": "GB", "英国": "GB",
	}
	if code, exists := aliases[value]; exists {
		return code
	}
	if len(value) == 2 && value != "ZZ" && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z' {
		return value
	}
	return ""
}
