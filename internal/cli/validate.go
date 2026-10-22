// Package cli provides the validation-only command boundary.
package cli

import (
	"fmt"
	"io"
	"os"

	"pulltide/internal/application"
)

// Run reports success, invalid/unreadable input, and usage errors as 0, 1, and 2.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: pulltide validate <application.yaml>")
		return 2
	}
	input, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintln(stderr, "cannot read Application document")
		return 1
	}
	if _, err := application.Parse(input); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "Application is valid")
	return 0
}
