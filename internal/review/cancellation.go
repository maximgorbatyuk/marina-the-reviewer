package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

func contextHash(ctx context.Context, value string) (string, error) {
	digest := sha256.New()
	for offset := 0; offset < len(value); offset += 65536 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, _ = digest.Write([]byte(value[offset:min(len(value), offset+65536)]))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func contextJSON(ctx context.Context, value any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	encoded := canonicalJSON(value)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return encoded, nil
}
