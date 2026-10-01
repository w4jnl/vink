package totp

import (
	"testing"
	"time"
)

func TestRFC6238Vectors(t *testing.T) {
	// the RFC's SHA-1 test secret, its 8-digit vectors cut to six
	secret := enc.EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		at   int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got, err := Code(secret, time.Unix(tc.at, 0))
		if err != nil || got != tc.want {
			t.Errorf("at %d: %s %v, want %s", tc.at, got, err, tc.want)
		}
	}
}

func TestVerifyWindowAndReplay(t *testing.T) {
	secret, err := NewSecret()
	if err != nil || len(secret) != 32 {
		t.Fatalf("secret: %q %v", secret, err)
	}
	now := time.Unix(1_700_000_000, 0)
	cur := Step(now)
	for _, d := range []int64{-1, 0, 1} {
		code, _ := CodeAt(secret, cur+d)
		if step, ok := Verify(secret, code, now, 0); !ok || step != cur+d {
			t.Errorf("step %+d must verify: %v %d", d, ok, step)
		}
	}
	for _, d := range []int64{-2, 2} {
		code, _ := CodeAt(secret, cur+d)
		if _, ok := Verify(secret, code, now, 0); ok {
			t.Errorf("step %+d must not verify", d)
		}
	}
	// a used step, and anything before it, is refused
	code, _ := CodeAt(secret, cur)
	if _, ok := Verify(secret, code, now, cur); ok {
		t.Error("replay accepted")
	}
	prev, _ := CodeAt(secret, cur-1)
	if _, ok := Verify(secret, prev, now, cur); ok {
		t.Error("older step accepted after a newer one")
	}
	for _, bad := range []string{"", "12345", "1234567", "12345a", "abcdef"} {
		if _, ok := Verify(secret, bad, now, 0); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := Verify(secret, "123 456", now, 0); ok {
		// spaces are stripped, but the digits still have to match
		t.Error("spaced wrong code accepted")
	}
	if got := Pretty("JBSWY3DPEHPK3PXP"); got != "JBSW Y3DP EHPK 3PXP" {
		t.Errorf("pretty: %q", got)
	}
	if got := URI("vink", "j@vink.w4j.nl", "JBSWY3DP"); got != "otpauth://totp/vink:j@vink.w4j.nl?algorithm=SHA1&digits=6&issuer=vink&period=30&secret=JBSWY3DP" {
		t.Errorf("uri: %s", got)
	}
}
