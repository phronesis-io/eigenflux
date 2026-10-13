package need

import (
	"strings"

	"golang.org/x/text/language"
)

// Only standard codes are accepted; natural-language aliases are not interpreted.
func languageCode(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "_") {
		return "", false
	}
	tag, err := language.Parse(value)
	base, _, _ := tag.Raw()
	return tag.String(), err == nil && base.String() != "und"
}
func regionCode(value string) (string, bool) {
	if len(value) != 2 {
		return "", false
	}
	region, err := language.ParseRegion(strings.ToUpper(value))
	return region.String(), err == nil && region.IsCountry()
}
