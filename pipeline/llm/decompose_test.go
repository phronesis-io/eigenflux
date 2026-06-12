package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeChat struct {
	resp string
	err  error
}

func (f *fakeChat) Complete(_ context.Context, _ string) (string, error) {
	return f.resp, f.err
}

func TestDecomposeTask_HappyPath(t *testing.T) {
	f := &fakeChat{resp: `{"sub_intents":[
      {"name":"翻译","query_text":"西语->中文","importance":1.0},
      {"name":"PPT生成","query_text":"生成 PPT","importance":0.9}
    ]}`}
	out, err := DecomposeTask(context.Background(), f, "翻译西语然后做PPT")
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "翻译", out[0].Name)
	assert.InDelta(t, 1.0, out[0].Importance, 1e-9)
}

func TestDecomposeTask_StripsCodeFence(t *testing.T) {
	f := &fakeChat{resp: "```json\n{\"sub_intents\":[{\"name\":\"x\",\"query_text\":\"y\",\"importance\":0.5}]}\n```"}
	out, err := DecomposeTask(context.Background(), f, "x")
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "x", out[0].Name)
}

func TestDecomposeTask_LLMError(t *testing.T) {
	f := &fakeChat{err: errors.New("up")}
	_, err := DecomposeTask(context.Background(), f, "x")
	assert.Error(t, err)
}

func TestDecomposeTask_BadJSON(t *testing.T) {
	f := &fakeChat{resp: "not json"}
	_, err := DecomposeTask(context.Background(), f, "x")
	assert.Error(t, err)
}

func TestDecomposeTask_EmptyQuery(t *testing.T) {
	_, err := DecomposeTask(context.Background(), &fakeChat{resp: "{}"}, "   ")
	assert.Error(t, err)
}

func TestDecomposeTask_NoSubIntents(t *testing.T) {
	f := &fakeChat{resp: `{"sub_intents":[]}`}
	_, err := DecomposeTask(context.Background(), f, "x")
	assert.Error(t, err)
}
