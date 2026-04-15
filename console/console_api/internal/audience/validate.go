package audience

import (
	"github.com/expr-lang/expr"
)

var knownVars = map[string]interface{}{
	"skill_ver":     "",
	"skill_ver_num": 0,
	"cli_ver":       "",
	"cli_ver_num":   0,
	"agent_id":      int64(0),
	"email":         "",
}

func compileOptions() []expr.Option {
	return []expr.Option{
		expr.Env(knownVars),
		expr.AsBool(),
	}
}

func Validate(expression string) error {
	if expression == "" {
		return nil
	}
	_, err := expr.Compile(expression, compileOptions()...)
	return err
}
