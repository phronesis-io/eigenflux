package skilldoc

import "strings"

const apiBasePath = "/api/v1"
const skillPath = "/skill.md"

func NormalizePublicBaseURL(publicBaseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	normalized = strings.TrimSuffix(normalized, apiBasePath)
	return strings.TrimRight(normalized, "/")
}

func BuildAPIBaseURL(publicBaseURL string) string {
	normalized := NormalizePublicBaseURL(publicBaseURL)
	if normalized == "" {
		return ""
	}
	return normalized + apiBasePath
}

func BuildSkillURL(publicBaseURL string) string {
	normalized := NormalizePublicBaseURL(publicBaseURL)
	if normalized == "" {
		return ""
	}
	return normalized + skillPath
}
