package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"eigenflux_server/pkg/reqinfo"
)

func TestBuildContextFeatures_FullProjection(t *testing.T) {
	got := buildContextFeatures(reqinfo.ClientInfo{
		Host:        "openclaw/0.0.12",
		Channel:     "openclaw",
		ClientID:    "ab12cd34",
		OS:          "darwin/arm64",
		TZ:          "Asia/Shanghai",
		Lang:        "zh-CN",
		CLIVer:      "0.0.7",
		CLIVerNum:   7,
		SkillVer:    "1.2.3",
		SkillVerNum: 10203,
	})
	assert.Equal(t, map[string]any{
		"client_host":    "openclaw/0.0.12",
		"client_channel": "openclaw",
		"client_id":      "ab12cd34",
		"client_os":      "darwin/arm64",
		"client_tz":      "Asia/Shanghai",
		"client_lang":    "zh-CN",
		"cli_ver":        "0.0.7",
		"cli_ver_num":    7,
		"skill_ver":      "1.2.3",
		"skill_ver_num":  10203,
	}, got)
}

func TestBuildContextFeatures_EmptyFieldsOmitted(t *testing.T) {
	got := buildContextFeatures(reqinfo.ClientInfo{
		Host:    "openclaw/0.0.12",
		Channel: "openclaw",
		// every other field zero
	})
	assert.Equal(t, map[string]any{
		"client_host":    "openclaw/0.0.12",
		"client_channel": "openclaw",
	}, got)
}

func TestBuildContextFeatures_ZeroVersionNumOmitted(t *testing.T) {
	// CLIVer set but CLIVerNum unparsed (0). The string still records the
	// raw header; the numeric form is dropped to avoid emitting a misleading
	// "0" that would shadow real version comparisons.
	got := buildContextFeatures(reqinfo.ClientInfo{
		CLIVer:    "dev",
		CLIVerNum: 0,
	})
	assert.Equal(t, map[string]any{
		"cli_ver": "dev",
	}, got)
	_, hasNum := got["cli_ver_num"]
	assert.False(t, hasNum)
}

func TestBuildContextFeatures_AllEmptyReturnsNil(t *testing.T) {
	got := buildContextFeatures(reqinfo.ClientInfo{})
	assert.Nil(t, got, "absent headers must produce no context block at all")
}
