package consumer

// Redis Stream envelope fields are strings or byte slices, never coerced numbers.
func streamText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}
