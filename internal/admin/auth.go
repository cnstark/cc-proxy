package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionTTL = 12 * time.Hour

// HashPassword 用 argon2id 加盐哈希，返回标准编码串。
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		64*1024, 2, 4,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword 恒定时间校验密码。
func VerifyPassword(password, encoded string) bool {
	params, salt, hash, ok := decodeArgon2id(encoded)
	if !ok {
		return false
	}
	other := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(hash)))
	return subtle.ConstantTimeCompare(hash, other) == 1
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeArgon2id(encoded string) (argonParams, []byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, false
	}
	var p argonParams
	for _, kv := range strings.Split(parts[3], ",") {
		kvParts := strings.SplitN(kv, "=", 2)
		if len(kvParts) != 2 {
			return argonParams{}, nil, nil, false
		}
		switch kvParts[0] {
		case "m":
			n, err := strconv.ParseUint(kvParts[1], 10, 32)
			if err != nil {
				return argonParams{}, nil, nil, false
			}
			p.memory = uint32(n)
		case "t":
			n, err := strconv.ParseUint(kvParts[1], 10, 32)
			if err != nil {
				return argonParams{}, nil, nil, false
			}
			p.time = uint32(n)
		case "p":
			n, err := strconv.ParseUint(kvParts[1], 10, 8)
			if err != nil {
				return argonParams{}, nil, nil, false
			}
			p.threads = uint8(n)
		}
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return argonParams{}, nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, false
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argonParams{}, nil, nil, false
	}
	return p, salt, hash, true
}

// GenSessionSecret 生成 32 字节 hex session secret（CLI 设密码时用）。
func GenSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SessionManager 签发与校验 session token（HMAC-SHA256 签名）。
type SessionManager struct {
	secret []byte
}

func NewSessionManager(secret string) *SessionManager {
	return &SessionManager{secret: []byte(secret)}
}

// Issue 签发一个 12h 有效期的 token，返回 token 与过期时间。
func (sm *SessionManager) Issue() (string, time.Time) {
	return sm.issueAt(time.Now().Add(sessionTTL))
}

func (sm *SessionManager) issueAt(exp time.Time) (string, time.Time) {
	randB := make([]byte, 32)
	rand.Read(randB)
	payload := fmt.Sprintf("%d.%s", exp.Unix(), base64.RawURLEncoding.EncodeToString(randB))
	return payload + "." + sm.sign(payload), exp
}

func (sm *SessionManager) sign(payload string) string {
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验 token 签名与有效期。
func (sm *SessionManager) Verify(token string) bool {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return false
	}
	payload := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, _ := base64.RawURLEncoding.DecodeString(sm.sign(payload))
	if !hmac.Equal(sig, expected) {
		return false
	}
	expUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expUnix
}
