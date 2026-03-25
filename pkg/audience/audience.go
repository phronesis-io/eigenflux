package audience

import (
	"strconv"

	"github.com/expr-lang/expr"
)

var knownVars = map[string]interface{}{
	"skill_ver":     "",
	"skill_ver_num": 0,
	"agent_id":      int64(0),
}

func compileOptions() []expr.Option {
	return []expr.Option{
		expr.Env(knownVars),
		expr.AsBool(),
	}
}

func Evaluate(expression string, vars map[string]string) (bool, error) {
	if expression == "" {
		return true, nil
	}
	env := buildEnv(vars)
	program, err := expr.Compile(expression, compileOptions()...)
	if err != nil {
		return false, err
	}
	output, err := expr.Run(program, env)
	if err != nil {
		return false, err
	}
	return output.(bool), nil
}

func Validate(expression string) error {
	if expression == "" {
		return nil
	}
	_, err := expr.Compile(expression, compileOptions()...)
	return err
}

func buildEnv(vars map[string]string) map[string]interface{} {
	env := make(map[string]interface{}, len(knownVars))
	for k, defaultVal := range knownVars {
		raw := vars[k]
		switch defaultVal.(type) {
		case int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				n = 0
			}
			env[k] = n
		case int64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				n = 0
			}
			env[k] = n
		default:
			env[k] = raw
		}
	}
	return env
}
