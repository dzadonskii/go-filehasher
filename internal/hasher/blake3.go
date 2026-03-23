package hasher

import (
	"context"
	"encoding/hex"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

func HashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := blake3.New()
	cw := &contextWriter{ctx: ctx, dst: hasher}
	if _, err := io.Copy(cw, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type contextWriter struct {
	ctx context.Context
	dst io.Writer
}

func (c *contextWriter) Write(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
		return c.dst.Write(p)
	}
}
