package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// page is a cursor response envelope.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func limitParam(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, badRequest("limit must be a positive integer")
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, nil
}

// encodeCursor packs sort keys into an opaque string.
func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x00")))
}

// decodeCursor unpacks a cursor made by encodeCursor with n parts.
func decodeCursor(s string, n int) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, badRequest("invalid cursor")
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != n {
		return nil, badRequest("invalid cursor")
	}
	return parts, nil
}
