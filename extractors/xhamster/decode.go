package xhamster

import (
	"encoding/json"
	"strings"
)

// decodeAt декодирует JSON-значение, начинающееся с позиции i.
func decodeAt(page string, i int) any {
	var v any
	if json.NewDecoder(strings.NewReader(page[i:])).Decode(&v) != nil {
		return nil
	}
	return v
}
