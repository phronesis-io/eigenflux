package queryprocessing

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQueryAnalysisWithoutVocabulary(t *testing.T) {
	for _, tc := range []struct {
		query, normalized, script string
		phrases                   []string
	}{
		{"  Ｋ８Ｓ\t运维  ", "k8s 运维", "mixed", nil},
		{"K8S operations", "k8s operations", "latin", nil},
		{"寻找人工智能服务", "寻找人工智能服务", "cjk", []string{"寻找人工智能服务"}},
		{"港股研報", "港股研報", "cjk", []string{"港股研報"}},
		{"no k8s please", "no k8s please", "latin", nil},
		{"partial report", "partial report", "latin", nil},
		{"軟體架構", "軟體架構", "cjk", []string{"軟體架構"}},
		{"running", "running", "latin", nil},
		{"寻找有丰富实践经验的软件架构设计师", "寻找有丰富实践经验的软件架构设计师", "cjk", nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			p := Process(tc.query, Options{})
			require.Equal(t, tc.normalized, p.Normalized)
			require.Equal(t, tc.script, p.Script)
			require.Equal(t, tc.phrases, p.Phrases)
			require.Equal(t, p, Process(tc.query, Options{}))
		})
	}
}
func TestIdentityPreservesOriginalText(t *testing.T) {
	for _, raw := range []string{"9223372036854775807", "AbCdE", "ＡＩ Studio"} {
		p := Process(raw, Options{Identity: true})
		require.Equal(t, raw, p.Normalized)
		require.Equal(t, "identity", p.Script)
		require.Empty(t, p.Phrases)
	}
}
