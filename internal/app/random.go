package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func randomToken() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create operation marker: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
