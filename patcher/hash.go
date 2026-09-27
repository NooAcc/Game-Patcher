package patcher

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

// HashBytes 返回 BLAKE3-256 哈希。
func HashBytes(b []byte) [32]byte { return blake3.Sum256(b) }

// HashReader 流式计算 BLAKE3-256 哈希。
func HashReader(r io.Reader) ([32]byte, error) {
	var out [32]byte
	h := blake3.New()
	if _, err := io.Copy(h, r); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}

// HashFile 流式计算文件 BLAKE3-256 哈希。
func HashFile(path string) ([32]byte, error) {
	var out [32]byte
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	return HashReader(f)
}

func hashHex(h [32]byte) string { return hex.EncodeToString(h[:]) }

func parseHashHex(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("哈希格式非法: %w", err)
	}
	if len(b) != 32 {
		return out, fmt.Errorf("哈希长度非法: %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
