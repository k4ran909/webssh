package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// RFC 6238 TOTP: SHA-1, 6 digits, 30 s steps — what every authenticator app expects.

const totpPeriod = 30

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// verifyTOTP accepts the current step ±1 for clock drift and returns the
// matched step so callers can reject replays.
func verifyTOTP(secret []byte, code string, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for d := int64(-1); d <= 1; d++ {
		if hmac.Equal([]byte(totpCode(secret, cur+d)), []byte(code)) {
			return cur + d, true
		}
	}
	return 0, false
}

func totpURI(secret []byte, email string) string {
	v := url.Values{}
	v.Set("secret", b32.EncodeToString(secret))
	v.Set("issuer", "webssh")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape("webssh:"+email) + "?" + v.Encode()
}
