package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/kitex_gen/eigenflux/sort"
)

type fakeChat struct {
	resp string
	err  error
}

func (f *fakeChat) Complete(_ context.Context, _ string) (string, error) {
	return f.resp, f.err
}

func ptrF(v float64) *float64 { return &v }

func TestResolveSubIntents_FromAgent(t *testing.T) {
	req := &sort.SearchServicesReq{
		RawQuery: "x",
		SubIntents: []*sort.SubIntent{
			{Name: "translate", QueryText: "translate", Importance: ptrF(1.0)},
		},
	}
	effective, source := resolveSubIntents(context.Background(), req, nil)
	assert.Equal(t, sourceAgent, source)
	require.Len(t, effective, 1)
	assert.Equal(t, "translate", effective[0].Name)
}

func TestResolveSubIntents_LLMFallback(t *testing.T) {
	req := &sort.SearchServicesReq{RawQuery: "do x"}
	chat := &fakeChat{resp: `{"sub_intents":[{"name":"x","query_text":"x","importance":1.0}]}`}
	effective, source := resolveSubIntents(context.Background(), req, chat)
	assert.Equal(t, sourceLLMFallback, source)
	require.Len(t, effective, 1)
}

func TestResolveSubIntents_LLMErrorFallsBackToSingleIntent(t *testing.T) {
	req := &sort.SearchServicesReq{RawQuery: "raw"}
	chat := &fakeChat{err: errors.New("upstream")}
	effective, source := resolveSubIntents(context.Background(), req, chat)
	assert.Equal(t, sourceSingleIntentFallback, source)
	require.Len(t, effective, 1)
	assert.Equal(t, "raw", effective[0].QueryText)
}

func TestResolveSubIntents_NoChatFallsBackToSingleIntent(t *testing.T) {
	req := &sort.SearchServicesReq{RawQuery: "raw"}
	effective, source := resolveSubIntents(context.Background(), req, nil)
	assert.Equal(t, sourceSingleIntentFallback, source)
	require.Len(t, effective, 1)
}

func TestTrimAndCapAgentIntents_DedupsAndCaps(t *testing.T) {
	in := []*sort.SubIntent{
		{Name: "a", QueryText: "x"},
		{Name: "a", QueryText: "y"}, // dup
		{Name: "b", QueryText: "x"},
	}
	out := trimAndCapAgentIntents(in)
	require.Len(t, out, 2)
	assert.Equal(t, "a", out[0].Name)
	assert.Equal(t, "b", out[1].Name)
}

func TestTrimAndCapAgentIntents_HardCapAtEight(t *testing.T) {
	in := make([]*sort.SubIntent, 0, 12)
	for i := 0; i < 12; i++ {
		in = append(in, &sort.SubIntent{Name: string(rune('a' + i)), QueryText: "q"})
	}
	out := trimAndCapAgentIntents(in)
	assert.Len(t, out, maxSubIntents)
}

func TestBuildFilters_NilWhenAbsent(t *testing.T) {
	req := &sort.SearchServicesReq{}
	assert.Nil(t, buildFilters(req))
}

func TestSingleIntent_HasImportanceOne(t *testing.T) {
	out := singleIntent("hello")
	require.Len(t, out, 1)
	assert.Equal(t, "hello", out[0].QueryText)
	assert.InDelta(t, 1.0, out[0].Importance, 1e-9)
}
