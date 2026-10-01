// Package totp implements RFC 6238 time-based one-time passwords with the
// standard library: HMAC-SHA1, six digits, thirty-second steps, as every
// authenticator app expects.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238 uses HMAC-SHA1; authenticator apps expect it
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Digits in a code.
	Digits = 6
	// Period of one step.
	Period = 30 * time.Second
	// Window is how many steps either side of now still verify: one, so a
	// phone that is up to thirty seconds off still works.
	Window = 1
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret makes a 160-bit secret, base32 without padding.
func NewSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return enc.EncodeToString(b), nil
}

// Step is the counter for t.
func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

func decode(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	return enc.DecodeString(s)
}

// CodeAt is the code for one step.
func CodeAt(secret string, step int64) (string, error) {
	key, err := decode(secret)
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	code := bin % 1_000_000
	s := strconv.FormatUint(uint64(code), 10)
	return strings.Repeat("0", Digits-len(s)) + s, nil
}

// Code is the code for now.
func Code(secret string, now time.Time) (string, error) { return CodeAt(secret, Step(now)) }

// Verify checks code against the steps around now and refuses any step at
// or before lastStep, so a code never works twice. It returns the step
// that matched.
func Verify(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	cur := Step(now)
	matched := int64(-1)
	for d := int64(-Window); d <= Window; d++ {
		step := cur + d
		want, err := CodeAt(secret, step)
		if err != nil {
			return 0, false
		}
		// every step is compared so timing says nothing about which matched
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && step > lastStep && matched < 0 {
			matched = step
		}
	}
	return matched, matched >= 0
}

// URI is the otpauth link an authenticator app scans.
func URI(issuer, account, secret string) string {
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {strconv.Itoa(Digits)}, "period": {strconv.Itoa(int(Period / time.Second))}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// Pretty groups the secret in fours for typing it by hand.
func Pretty(secret string) string {
	var b strings.Builder
	for i, c := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}
