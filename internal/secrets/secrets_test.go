package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCreatesAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	k1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 || st.Size() != 32 {
		t.Fatalf("mode %v size %d", st.Mode().Perm(), st.Size())
	}
	k2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := k1.Seal([]byte("hi"))
	if out, err := k2.Open(sealed); err != nil || string(out) != "hi" {
		t.Fatalf("second load must reuse the key: %q %v", out, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("wrong-length key must fail")
	}
}

func TestSealOpen(t *testing.T) {
	k, _ := FromBytes(make([]byte, 32))
	sealed, err := k.Seal([]byte(`{"url":"https://hook"}`))
	if err != nil || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if again, _ := k.Seal([]byte(`{"url":"https://hook"}`)); again == sealed {
		t.Fatal("nonce must differ")
	}
	out, err := k.Open(sealed)
	if err != nil || string(out) != `{"url":"https://hook"}` {
		t.Fatalf("open: %q %v", out, err)
	}
	other, _ := FromBytes(append([]byte{1}, make([]byte, 31)...))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("other key must fail")
	}
	for _, bad := range []string{"", "v1:", "v1:!!!", "v2:abc", sealed[:len(sealed)-2] + "AA"} {
		if _, err := k.Open(bad); err == nil {
			t.Errorf("Open(%q) must fail", bad)
		}
	}
}

func TestSignVerify(t *testing.T) {
	k, _ := FromBytes(make([]byte, 32))
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	tok := k.Sign("ack", "incident-1", now.Add(7*24*time.Hour))
	if got, err := k.Verify("ack", tok, now); err != nil || got != "incident-1" {
		t.Fatalf("verify: %q %v", got, err)
	}
	if _, err := k.Verify("ack", tok, now.Add(8*24*time.Hour)); err == nil {
		t.Fatal("expired token accepted")
	}
	if _, err := k.Verify("other", tok, now); err == nil {
		t.Fatal("wrong purpose accepted")
	}
	if _, err := k.Verify("ack", tok[:len(tok)-1]+"x", now); err == nil {
		t.Fatal("tampered signature accepted")
	}
	if _, err := k.Verify("ack", "nodot", now); err == nil {
		t.Fatal("malformed accepted")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") {
		t.Fatalf("hash: %q %v", h, err)
	}
	if !VerifyPassword(h, "correct horse") || VerifyPassword(h, "wrong") || VerifyPassword("", "x") || VerifyPassword("$argon2id$v=19$m=1,t=1,p=1$bad$bad", "x") {
		t.Fatal("verify")
	}
}

func TestAPIKeys(t *testing.T) {
	tok, prefix, err := NewAPIKey()
	if err != nil || !strings.HasPrefix(tok, "vk_"+prefix+"_") || len(prefix) != 8 {
		t.Fatalf("%q %q %v", tok, prefix, err)
	}
	if p, ok := ParseAPIKeyPrefix(tok); !ok || p != prefix {
		t.Fatalf("parse prefix: %q %v", p, ok)
	}
	for _, bad := range []string{"", "vk_short", "xx_" + prefix + "_" + strings.Repeat("a", 32), "vk_" + prefix + "x" + strings.Repeat("a", 32)} {
		if _, ok := ParseAPIKeyPrefix(bad); ok {
			t.Errorf("ParseAPIKeyPrefix(%q) must fail", bad)
		}
	}
	h := HashToken(tok)
	if !VerifyToken(h, tok) || VerifyToken(h, tok+"x") {
		t.Fatal("token verify")
	}
	if r, _ := RandomToken(32); len(r) != 43 {
		t.Fatalf("RandomToken len %d", len(r))
	}
	if Fingerprint("a") == Fingerprint("b") || len(Fingerprint("a")) != 22 {
		t.Fatal("fingerprint")
	}
}
