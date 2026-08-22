package consolev2

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

var onboardingCountryAliases = map[string]string{
	"CN": "CN", "CHINA": "CN", "中国": "CN", "中国大陆": "CN",
	"HK": "HK", "HONG KONG": "HK", "香港": "HK", "中国香港": "HK",
	"SG": "SG", "SINGAPORE": "SG", "新加坡": "SG",
	"JP": "JP", "JAPAN": "JP", "日本": "JP",
	"US": "US", "USA": "US", "UNITED STATES": "US", "美国": "US",
	"GB": "GB", "UK": "GB", "UNITED KINGDOM": "GB", "英国": "GB",
	"ZZ": "ZZ", "OTHER": "ZZ", "其他": "ZZ",
}

var onboardingTimezoneAliases = map[string]string{
	"ASIA/SHANGHAI":       "Asia/Shanghai",
	"ASIA/SINGAPORE":      "Asia/Singapore",
	"ASIA/TOKYO":          "Asia/Tokyo",
	"AMERICA/LOS_ANGELES": "America/Los_Angeles",
	"AMERICA/NEW_YORK":    "America/New_York",
	"EUROPE/LONDON":       "Europe/London",
}

var onboardingListFields = []string{
	"working_languages",
	"seeking",
	"offering",
	"agent_status",
	"human_status",
	"interests_negative",
}

func normalizeOnboardingCountry(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if code, ok := onboardingCountryAliases[strings.ToUpper(value)]; ok {
		return code, nil
	}
	upper := strings.ToUpper(value)
	if countryCodePattern.MatchString(upper) {
		return upper, nil
	}
	return "", fmt.Errorf("geo must use a supported ISO 3166-1 alpha-2 code")
}

func normalizeOnboardingTimezone(raw, countryCode string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	withoutOffset := regexp.MustCompile(`(?i)\s*\(UTC[+-][^)]+\)\s*$`).ReplaceAllString(value, "")
	if zone, ok := onboardingTimezoneAliases[strings.ToUpper(withoutOffset)]; ok {
		return zone, nil
	}
	compactOffset := strings.ToUpper(strings.ReplaceAll(value, " ", ""))
	if compactOffset == "UTC+8" || compactOffset == "GMT+8" {
		switch countryCode {
		case "CN", "HK":
			return "Asia/Shanghai", nil
		case "SG":
			return "Asia/Singapore", nil
		default:
			// An offset alone cannot identify a location. Keep the field empty so
			// the human selects the correct IANA zone in Step 2.
			return "", nil
		}
	}
	if _, err := time.LoadLocation(withoutOffset); err != nil {
		return "", fmt.Errorf("timezone must use a valid IANA identifier")
	}
	return withoutOffset, nil
}

func normalizeOnboardingDraftLocations(draft map[string]interface{}) error {
	identity, ok := draft["identity_card"].(map[string]interface{})
	if !ok {
		return nil
	}
	country := ""
	if raw, exists := identity["geo"]; exists {
		text, ok := raw.(string)
		if !ok {
			return fmt.Errorf("geo must be a string")
		}
		normalized, err := normalizeOnboardingCountry(text)
		if err != nil {
			return err
		}
		country = normalized
		identity["geo"] = normalized
	}
	if raw, exists := identity["timezone"]; exists {
		text, ok := raw.(string)
		if !ok {
			return fmt.Errorf("timezone must be a string")
		}
		normalized, err := normalizeOnboardingTimezone(text, country)
		if err != nil {
			return err
		}
		identity["timezone"] = normalized
	}
	return nil
}

func normalizeOnboardingDraftLists(draft map[string]interface{}) error {
	identity, ok := draft["identity_card"].(map[string]interface{})
	if !ok {
		return nil
	}
	for _, field := range onboardingListFields {
		raw, exists := identity[field]
		if !exists || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case []interface{}:
			items := make([]string, 0, len(value))
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					return fmt.Errorf("%s must contain strings", field)
				}
				if text = strings.TrimSpace(text); text != "" {
					items = append(items, text)
				}
			}
			identity[field] = items
		case string:
			items := strings.FieldsFunc(value, func(r rune) bool {
				switch r {
				case '·', ',', '，', ';', '；', '\n', '\r':
					return true
				default:
					return false
				}
			})
			for index := range items {
				items[index] = strings.TrimSpace(items[index])
			}
			identity[field] = items
		default:
			return fmt.Errorf("%s must be an array of strings", field)
		}
	}
	return nil
}

func normalizeOnboardingDraftJSON(raw json.RawMessage) (json.RawMessage, map[string]interface{}, error) {
	draft, err := decodeJSONObject(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := normalizeOnboardingDraftLocations(draft); err != nil {
		return nil, nil, err
	}
	if err := normalizeOnboardingDraftLists(draft); err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(draft)
	return encoded, draft, err
}
