package storage

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeJSONL    Mode = "jsonl"
	ModePostgres Mode = "postgres"
)

func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case "", ModeJSONL:
		return ModeJSONL, nil
	case ModePostgres:
		return ModePostgres, nil
	default:
		return "", fmt.Errorf("STORAGE must be jsonl or postgres")
	}
}
