package featureindex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type fieldPlan struct {
	selected map[string]int
	removed  []int
}

// Plans belong to an immutable YAML snapshot, so removed fields take effect on
// the next read without retaining caches for every historical configuration.
func (d Definition) plan(t reflect.Type) *fieldPlan {
	if d.plans != nil {
		if p, ok := d.plans.Load(t); ok {
			return p.(*fieldPlan)
		}
	}
	allowed := make(map[string]bool, len(d.Fields))
	for _, f := range d.Fields {
		allowed[f] = true
	}
	p := &fieldPlan{selected: map[string]int{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if allowed[name] {
			p.selected[name] = i
		} else {
			p.removed = append(p.removed, i)
		}
	}
	if d.plans != nil {
		v, _ := d.plans.LoadOrStore(t, p)
		return v.(*fieldPlan)
	}
	return p
}

// Select before serialization: large source descriptions/embeddings are never
// marshalled merely to discard them. Generic map callers retain the same API.
func (d Definition) selectFields(value any) (map[string]json.RawMessage, error) {
	v := reflect.ValueOf(value)
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, fmt.Errorf("nil feature value")
		}
		v = v.Elem()
	}
	out := make(map[string]json.RawMessage, len(d.Fields))
	if v.IsValid() && v.Kind() == reflect.Struct {
		for name, i := range d.plan(v.Type()).selected {
			b, err := json.Marshal(v.Field(i).Interface())
			if err != nil {
				return nil, err
			}
			out[name] = b
		}
		return out, nil
	}
	if v.IsValid() && v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
		for _, name := range d.Fields {
			f := v.MapIndex(reflect.ValueOf(name).Convert(v.Type().Key()))
			if !f.IsValid() {
				continue
			}
			b, err := json.Marshal(f.Interface())
			if err != nil {
				return nil, err
			}
			out[name] = b
		}
		return out, nil
	}
	// Raw JSON is used by maintenance/compatibility callers, not typed readers.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	for _, name := range d.Fields {
		if b, ok := all[name]; ok {
			out[name] = b
		}
	}
	return out, nil
}

func (d Definition) decode(raw []byte, into any) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return err
	}
	v := reflect.ValueOf(into)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("feature decode requires a struct pointer")
	}
	v = v.Elem()
	for _, i := range d.plan(v.Type()).removed {
		v.Field(i).SetZero()
	}
	return nil
}

func decodeRows[T any](row componentRows) (out map[int64]T, err error) {

	out = make(map[int64]T, len(row.rows))
	for id, raw := range row.rows {
		var d T
		if err = row.view.decode(raw, &d); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, nil
}
