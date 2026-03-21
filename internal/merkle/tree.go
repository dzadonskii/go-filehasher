package merkle

import (
	"encoding/hex"
	"sort"

	"github.com/zeebo/blake3"
)

type Entry struct {
	Name string
	Hash string
}

func CalculateDirHash(entries []Entry) string {
	hasher := blake3.New()
	if len(entries) == 0 {
		return hex.EncodeToString(hasher.Sum(nil))
	}

	// Sort entries by name to ensure deterministic hashing
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	for _, entry := range entries {
		hasher.WriteString(entry.Name)
		hasher.Write([]byte{0}) // Use null byte as separator
		hasher.WriteString(entry.Hash)
		hasher.Write([]byte{0}) // Use null byte as separator
	}

	return hex.EncodeToString(hasher.Sum(nil))
}
