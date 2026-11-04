//go:build crewtalk_test

package codex

import "os"

func binary() string {
	if bin := os.Getenv("CREWTALK_CODEX_BIN"); bin != "" {
		return bin
	}
	return "codex"
}
