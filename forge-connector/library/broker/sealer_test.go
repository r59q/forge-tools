package broker_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
)

func TestAESGCMSealer(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	s, err := broker.NewAESGCMSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range [][]byte{nil, []byte("forge-secret"), bytes.Repeat([]byte("x"), 4096)} {
		sealed, err := s.Seal(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Seal(plaintext)
		if err != nil || bytes.Equal(sealed, other) {
			t.Fatal("Seal must use a fresh nonce")
		}
		opened, err := s.Open(sealed)
		if err != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("round trip failed: %v", err)
		}
		for i := range sealed {
			tampered := bytes.Clone(sealed)
			tampered[i] ^= 1
			if data, err := s.Open(tampered); !errors.Is(err, broker.ErrSealing) || data != nil {
				t.Fatalf("modified byte %d accepted", i)
			}
		}
		for i := range sealed {
			if data, err := s.Open(sealed[:i]); !errors.Is(err, broker.ErrSealing) || data != nil {
				t.Fatalf("truncated length %d accepted", i)
			}
		}
		wrong, err := broker.NewAESGCMSealer(bytes.Repeat([]byte{0x43}, 32))
		if err != nil {
			t.Fatal(err)
		}
		if data, err := wrong.Open(sealed); !errors.Is(err, broker.ErrSealing) || data != nil {
			t.Fatal("wrong key accepted")
		}
	}
	// Changing the caller's key does not change the cipher.
	sealed, _ := s.Seal([]byte("secret"))
	clear(key)
	if _, err := s.Open(sealed); err != nil {
		t.Fatal("sealer retained caller's key slice")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%x", "%s"} {
		if got := fmt.Sprintf(verb, s); got != "[REDACTED]" {
			t.Errorf("sealer formatting = %s", got)
		}
	}
}

func TestAESGCMSealerInvalid(t *testing.T) {
	for _, length := range []int{0, 16, 24, 31, 33} {
		if _, err := broker.NewAESGCMSealer(make([]byte, length)); !errors.Is(err, broker.ErrInvalid) {
			t.Errorf("accepted key length %d", length)
		}
	}
	for _, s := range []*broker.AESGCMSealer{nil, {}} {
		if _, err := s.Seal([]byte("secret")); !errors.Is(err, broker.ErrSealing) {
			t.Error("uninitialized Seal succeeded")
		}
		if _, err := s.Open(nil); !errors.Is(err, broker.ErrSealing) {
			t.Error("uninitialized Open succeeded")
		}
	}
}
