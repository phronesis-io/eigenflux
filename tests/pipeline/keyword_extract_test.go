package pipeline_test

import (
	"context"
	"testing"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractKeywords(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping LLM keyword extraction test in short mode")
	}

	cfg := config.Load()
	if cfg.LLMApiKey == "" {
		t.Skip("LLM API key not configured")
	}

	prompts, err := llm.LoadDefaultPrompts()
	require.NoError(t, err)

	client := llm.NewClient(cfg, prompts)
	ctx := context.Background()

	cases := []struct {
		name            string
		bio             string
		expectKeywords  []string // at least one of these should appear
		rejectKeywords  []string // none of these should appear alone
		expectCountry   string   // expected country code, empty to skip check
	}{
		{
			name: "ai startup founder",
			bio: `Domains: AI agents, startup strategy, product research, crypto markets, geopolitics
Purpose: Personal assistant for an AI startup founder; handles research, product strategy, demos, planning, and execution
Recent work: Building an agent-native information network, exploring broadcast-based information infrastructure, tracking AI ecosystem shifts and market signals
Looking for: high-signal updates on AI agents, startup opportunities, infrastructure trends, crypto and macro signals, and other agents who can collaborate or exchange useful intelligence
Country: China`,
			expectKeywords: []string{"ai-agents", "crypto", "geopolitics"},
			rejectKeywords: []string{"infrastructure", "collaboration", "agent-native", "macro-signals", "market-signals", "ai"},
			expectCountry:  "CN",
		},
		{
			name: "chinese ai assistant",
			bio: `Domains: AI 技术，知识管理，效率工具，数据分析
Purpose: 个人 AI 助理，帮助用户获取和处理信息
Recent work: 探索 AI Agent 网络，学习新知识管理方式
Looking for: AI 领域最新动态，效率工具推荐，有价值的行业洞察
Country: China`,
			expectKeywords: []string{"knowledge-management", "data-analysis", "ai-agents"},
			rejectKeywords: []string{"industry-insights", "ai-trends", "ai"},
			expectCountry:  "CN",
		},
		{
			name: "vc fund ai-infra with entities",
			bio: `Domains: AI infra, VC, crypto infra, agent systems, frontier technologies
Purpose: I help my team with technical DD, market scouting, founder research, and workflow automation
Recent work: analyzing AI infra projects, candidate diligence, OpenClaw updates, and automation workflows
Looking for: AI agents, infra tools, tokenization/stablecoin rails, strong technical founders, and useful open source signals
Country: China / Singapore
Fund: Impa Ventures, an early-stage venture fund focused on AI and frontier technologies, partnering with founders from 0 to 1`,
			expectKeywords: []string{"ai-infra", "crypto-infra", "openclaw", "impa-ventures", "technical-dd"},
			rejectKeywords: []string{"automation", "infrastructure", "technology", "ai"},
			expectCountry:  "CN,SG",
		},
	}


	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keywords, country, err := client.ExtractKeywords(ctx, tc.bio)
			require.NoError(t, err, "ExtractKeywords should not error")

			t.Logf("Bio:      %s", tc.bio)
			t.Logf("Keywords: %v", keywords)
			t.Logf("Country:  %s", country)

			assert.NotEmpty(t, keywords, "should extract at least one keyword")
			assert.LessOrEqual(t, len(keywords), 10, "should extract at most 10 keywords")

			// Check at least one expected keyword is present
			if len(tc.expectKeywords) > 0 {
				found := false
				for _, expect := range tc.expectKeywords {
					for _, kw := range keywords {
						if kw == expect {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				assert.True(t, found, "expected at least one of %v in keywords %v", tc.expectKeywords, keywords)
			}

			// Check rejected keywords do not appear as standalone
			for _, reject := range tc.rejectKeywords {
				for _, kw := range keywords {
					if kw == reject {
						t.Errorf("rejected keyword %q found in output %v — this is too generic/ambiguous", reject, keywords)
					}
				}
			}

			if tc.expectCountry != "" {
				assert.Contains(t, country, tc.expectCountry,
					"country %q should contain expected %q", country, tc.expectCountry)
			}
		})
	}
}
