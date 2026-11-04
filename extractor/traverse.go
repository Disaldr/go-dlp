package extractor

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Get обходит дерево JSON (map[string]any / []any) по пути.
// Элементы пути: string — ключ объекта, int — индекс массива (отрицательный — с конца).
// Возвращает nil, если путь не существует.
func Get(v any, path ...any) any {
	cur := v
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur, ok = m[k]
			if !ok {
				return nil
			}
		case int:
			s, ok := cur.([]any)
			if !ok {
				return nil
			}
			if k < 0 {
				k += len(s)
			}
			if k < 0 || k >= len(s) {
				return nil
			}
			cur = s[k]
		default:
			return nil
		}
	}
	return cur
}

// Str возвращает строку по пути ("" если нет или не строка).
func Str(v any, path ...any) string {
	switch x := Get(v, path...).(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// Int возвращает целое по пути; строки с числами тоже поддерживаются.
func Int(v any, path ...any) int64 {
	return ToInt(Get(v, path...))
}

// Float возвращает число с плавающей точкой по пути.
func Float(v any, path ...any) float64 {
	return ToFloat(Get(v, path...))
}

// Bool возвращает bool по пути.
func Bool(v any, path ...any) bool {
	b, _ := Get(v, path...).(bool)
	return b
}

// Map возвращает объект по пути.
func Map(v any, path ...any) map[string]any {
	m, _ := Get(v, path...).(map[string]any)
	return m
}

// Slice возвращает массив по пути.
func Slice(v any, path ...any) []any {
	s, _ := Get(v, path...).([]any)
	return s
}

// FirstStr возвращает первую непустую строку из набора путей.
func FirstStr(v any, paths ...[]any) string {
	for _, p := range paths {
		if s := Str(v, p...); s != "" {
			return s
		}
	}
	return ""
}

// P — сокращение для построения пути в FirstStr.
func P(path ...any) []any { return path }

// ToInt приводит значение к int64.
func ToInt(x any) int64 {
	switch n := x.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, _ := n.Float64()
			return int64(f)
		}
		return i
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(n), ",", "")
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

// ToFloat приводит значение к float64.
func ToFloat(x any) float64 {
	switch n := x.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f
	}
	return 0
}

// FindAll рекурсивно ищет все значения с ключом key в дереве JSON
// (обход в глубину: порядок элементов массивов сохраняется, ключи
// объектов обходятся в лексикографическом порядке — результат детерминирован).
func FindAll(v any, key string) []any {
	var out []any
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if val, ok := t[key]; ok {
				out = append(out, val)
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				if k != key {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		}
	}
	walk(v)
	return out
}

// FindFirst — первое значение с ключом key (см. FindAll).
func FindFirst(v any, key string) any {
	if all := FindAll(v, key); len(all) > 0 {
		return all[0]
	}
	return nil
}
