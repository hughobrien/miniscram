package main

import (
	"os"
	"testing"
)

func redumpTestCreds(t *testing.T) (string, string) {
	t.Helper()
	username := os.Getenv("MINISCRAM_REDUMP_TEST_USERNAME")
	password := os.Getenv("MINISCRAM_REDUMP_TEST_PASSWORD")
	if username == "" || password == "" {
		t.Skip("set MINISCRAM_REDUMP_TEST_USERNAME and MINISCRAM_REDUMP_TEST_PASSWORD")
	}
	return username, password
}
