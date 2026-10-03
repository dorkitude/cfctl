package output

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// JSON prints the value as indented JSON to stdout. A nil slice prints as []
// (not null), so an empty list is still a list.
func JSON(v interface{}) error {
	v = EmptyList(v)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// PrintKeyValue prints a styled key-value pair.
func PrintKeyValue(label string, value interface{}) {
	fmt.Printf("  %-14s %v\n", label+":", value)
}

// EmptyList returns [] for a nil slice (other than raw JSON bytes), else v.
func EmptyList(v interface{}) interface{} {
	if _, raw := v.(json.RawMessage); raw {
		return v
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.IsNil() && rv.Type().Elem().Kind() != reflect.Uint8 {
		return []struct{}{}
	}
	return v
}
