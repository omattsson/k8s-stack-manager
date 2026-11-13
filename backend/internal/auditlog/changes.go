package auditlog

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"backend/internal/helm"
	"backend/internal/models"
)

// valueFields are the JSON field names whose old and new values a change set
// may hold. Every other field gives its name only: free text, Helm values and
// URLs can hold secrets (#786).
var valueFields = map[string]bool{
	"role": true, "disabled": true, "allow_exec": true, "allow_port_forward": true,
	"ttl_minutes": true, "branch": true, "default_branch": true, "chart_version": true,
	"name": true, "is_default": true,
}

// skippedFields change on every write and say nothing.
var skippedFields = map[string]bool{"created_at": true, "updated_at": true, "deleted_at": true}

const (
	maxValueBytes = 200
	maxKeyPaths   = 200
	maxPathBytes  = 256
)

// Fields returns the changed fields between two values of the same struct
// type (or pointers to it). It skips fields that are not stored (json:"-",
// gorm:"-" or "->", readonly:"true"). Callers add secret fields that Fields
// cannot see with Redacted.
func Fields(before, after any) []models.AuditChange {
	b, a := reflect.Indirect(reflect.ValueOf(before)), reflect.Indirect(reflect.ValueOf(after))
	if b.Kind() != reflect.Struct || a.Type() != b.Type() {
		return nil
	}
	var out []models.AuditChange
	diffStruct(b, a, &out)
	return out
}

func diffStruct(b, a reflect.Value, out *[]models.AuditChange) {
	t := b.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			diffStruct(b.Field(i), a.Field(i), out)
			continue
		}
		name, ok := storedFieldName(f)
		if !ok || skippedFields[name] {
			continue
		}
		bv, av := b.Field(i).Interface(), a.Field(i).Interface()
		if reflect.DeepEqual(bv, av) {
			continue
		}
		ch := models.AuditChange{Field: name}
		if valueFields[name] {
			old, oldOK := scalar(b.Field(i))
			nw, newOK := scalar(a.Field(i))
			if oldOK && newOK {
				ch.Old, ch.New = old, nw
			}
		}
		*out = append(*out, ch)
	}
}

// storedFieldName returns the JSON name of a stored field.
func storedFieldName(f reflect.StructField) (string, bool) {
	jsonName := strings.Split(f.Tag.Get("json"), ",")[0]
	if jsonName == "-" || f.Tag.Get("readonly") == "true" {
		return "", false
	}
	for _, opt := range strings.Split(f.Tag.Get("gorm"), ";") {
		opt = strings.TrimSpace(opt)
		if opt == "-" || strings.HasPrefix(opt, "->") {
			return "", false
		}
	}
	if jsonName == "" {
		jsonName = f.Name
	}
	return jsonName, true
}

// scalar returns a showable value: a bool, a number or a short string. A nil
// pointer gives (nil, true): the field was unset.
func scalar(v reflect.Value) (any, bool) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, true
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return v.Interface(), true
	case reflect.String:
		if v.Len() > maxValueBytes {
			return nil, false
		}
		return v.String(), true
	}
	return nil, false
}

// Redacted marks a secret field that changed. It holds no value.
func Redacted(field string) models.AuditChange {
	return models.AuditChange{Field: field, Redacted: true}
}

// ChangedKeyPaths returns the dotted key paths (with prefix) of the leaves
// that differ between two Helm values documents, sorted. A list is one leaf.
// It never returns values, and at most maxKeyPaths paths plus "…".
func ChangedKeyPaths(prefix, oldYAML, newYAML string) ([]models.AuditChange, error) {
	oldMap, err := helm.ParseYAML(oldYAML)
	if err != nil {
		return nil, err
	}
	newMap, err := helm.ParseYAML(newYAML)
	if err != nil {
		return nil, err
	}
	oldLeaves, newLeaves := map[string]any{}, map[string]any{}
	flatten("", true, oldMap, oldLeaves)
	flatten("", true, newMap, newLeaves)
	var paths []string
	for p, v := range oldLeaves {
		if nv, ok := newLeaves[p]; !ok || !reflect.DeepEqual(v, nv) {
			paths = append(paths, p)
		}
	}
	for p := range newLeaves {
		if _, ok := oldLeaves[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	cut := len(paths) > maxKeyPaths
	if cut {
		paths = paths[:maxKeyPaths]
	}
	out := make([]models.AuditChange, 0, len(paths)+1)
	for _, p := range paths {
		out = append(out, models.AuditChange{Field: cutPath(prefix + p)})
	}
	if cut {
		out = append(out, models.AuditChange{Field: "…"})
	}
	return out, nil
}

// flatten adds the leaves of a YAML value: a non-empty map recurses, anything
// else (scalar, list, empty map) is a leaf.
func flatten(path string, root bool, v any, out map[string]any) {
	var keys []string
	var get func(string) any
	switch m := v.(type) {
	case map[string]any:
		for k := range m {
			keys = append(keys, k)
		}
		get = func(k string) any { return m[k] }
	case map[any]any:
		byName := map[string]any{}
		for k, val := range m {
			byName[fmt.Sprint(k)] = val
		}
		for k := range byName {
			keys = append(keys, k)
		}
		get = func(k string) any { return byName[k] }
	}
	if len(keys) == 0 {
		if !root {
			out[path] = v
		}
		return
	}
	for _, k := range keys {
		p := escapeKey(k)
		if !root {
			p = path + "." + p
		}
		flatten(p, false, get(k), out)
	}
}

// cutPath cuts a key path to maxPathBytes at a rune start.
func cutPath(p string) string {
	if len(p) <= maxPathBytes {
		return p
	}
	cut := maxPathBytes
	for cut > 0 && !utf8.RuneStart(p[cut]) {
		cut--
	}
	p = p[:cut]
	// Do not end inside an escape pair: drop an odd trailing backslash.
	n := len(p) - len(strings.TrimRight(p, `\`))
	if n%2 == 1 {
		p = p[:len(p)-1]
	}
	return p
}

// keyEscaper keeps "a.b" (one key) apart from a nested "a" and "b", and a
// literal key `""` apart from the empty key.
var keyEscaper = strings.NewReplacer(`\`, `\\`, `.`, `\.`, `"`, `\"`)

func escapeKey(k string) string {
	if k == "" {
		return `""`
	}
	return keyEscaper.Replace(k)
}
