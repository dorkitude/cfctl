package output

import (
	"encoding/json"
	"fmt"
	"os"
)

// JSON prints the value as indented JSON to stdout.
func JSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// PrintKeyValue prints a styled key-value pair.
func PrintKeyValue(label string, value interface{}) {
	fmt.Printf("  %-14s %v\n", label+":", value)
}
