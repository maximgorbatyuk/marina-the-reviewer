package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf16"
)

// MarshalJSON preserves the candidate decision wire shape for compatibility consumers.
func (c CandidateDecision) MarshalJSON() ([]byte, error) {
	type candidate CandidateDecision
	v := struct {
		candidate
		ReasonCode *string `json:"reasonCode"`
		Reason     *string `json:"reason"`
	}{candidate: candidate(c)}
	if c.ReasonCode != "" {
		v.ReasonCode = &c.ReasonCode
	}
	if c.Reason != "" {
		v.Reason = &c.Reason
	}
	return marshal(v), nil
}
func defaultJSON(value any) string {
	var b bytes.Buffer
	defaultJSONValue(&b, reflect.ValueOf(value))
	return b.String()
}
func defaultJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r >= 32 && r <= 126 && !strings.ContainsRune("\"'<>&+`", r) {
				b.WriteRune(r)
			} else {
				for _, u := range utf16.Encode([]rune{r}) {
					fmt.Fprintf(b, `\u%04X`, u)
				}
			}
		}
	}
	b.WriteByte('"')
}
func defaultJSONValue(b *bytes.Buffer, v reflect.Value) {
	if !v.IsValid() {
		b.WriteString("null")
		return
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			b.WriteString("null")
			return
		}
		defaultJSONValue(b, v.Elem())
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		b.WriteByte('{')
		first := true
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if t == reflect.TypeOf(GraphNode{}) || t == reflect.TypeOf(GraphEdge{}) {
				name = strings.ReplaceAll(field.Name, "ID", "Id")
			} else if name == "" {
				name = field.Name
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			defaultJSONString(b, name)
			b.WriteByte(':')
			defaultJSONValue(b, v.Field(i))
		}
		b.WriteByte('}')
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("null")
			return
		}
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			defaultJSONValue(b, v.Index(i))
		}
		b.WriteByte(']')
	case reflect.String:
		defaultJSONString(b, v.String())
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()))
	default:
		raw, _ := json.Marshal(v.Interface())
		b.Write(raw)
	}
}
