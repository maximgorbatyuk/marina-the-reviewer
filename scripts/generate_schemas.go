//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/maximgorbatyuk/marina-the-reviewer/internal/config"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/engine"
	"github.com/maximgorbatyuk/marina-the-reviewer/internal/review"
)

func shape(t reflect.Type) map[string]any {
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{shape(t.Elem()), map[string]any{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			properties[name] = shape(f.Type)
			if len(parts) < 2 || parts[1] != "omitempty" {
				required = append(required, name)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": shape(t.Elem())}
	case reflect.Slice:
		return map[string]any{"type": []string{"array", "null"}, "items": shape(t.Elem())}
	default:
		panic("unsupported schema type: " + t.String())
	}
}

func main() {
	types := map[string]reflect.Type{"review-execution-archive": reflect.TypeFor[engine.ExecutionArchive](), "review-document": reflect.TypeFor[engine.Document](), "review-snapshot": reflect.TypeFor[engine.Snapshot](), "review-plan": reflect.TypeFor[engine.Plan](), "review-result": reflect.TypeFor[engine.Result](), "config": reflect.TypeFor[config.Config](), "marina-prepared-batch-v3": reflect.TypeFor[review.PreparedBatch](), "marina-batch-result-v3": reflect.TypeFor[review.BatchResult]()}
	for name, t := range types {
		schema := shape(t)
		schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		schema["title"] = name
		if properties, ok := schema["properties"].(map[string]any); ok {
			if _, has := properties["schemaVersion"]; has {
				properties["schemaVersion"] = map[string]any{"const": 1}
			}
		}
		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			panic(err)
		}
		if err = os.MkdirAll("schemas", 0755); err != nil {
			panic(err)
		}
		if err = os.WriteFile(filepath.Join("schemas", name+".schema.json"), append(data, '\n'), 0644); err != nil {
			panic(err)
		}
	}
}
