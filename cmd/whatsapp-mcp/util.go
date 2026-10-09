package main

import (
	"os"
	"strconv"
)

// envOr returns the value of the environment variable key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool parses a boolean environment variable, defaulting when unset or
// unparseable. Used for the ALLOW_WRITES switch so a typo cannot silently
// enable writes.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
