package model

import "strings"

// small helpers kept here so the model package has no external imports.

func trimSpace(s string) string { return strings.TrimSpace(s) }

func splitComma(s string) []string { return strings.Split(s, ",") }
