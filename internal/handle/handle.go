// Package handle derives the four-word name nitipan assigns to a holder.
//
// The words come from the BIP-39 English list: 2048 words, so each carries 11 bits. Four words give
// 44 bits, and a collision between two holders on one machine is negligible. The handle is a pure
// function of the holder's identity and the machine, so it is the same on every call by that holder.
package handle

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"strings"
)

// WordCount is the number of words in the list. The handle takes 11 bits from each.
const WordCount = 2048

const (
	wordsPerHandle = 4
	bitsPerWord    = 11
)

//go:embed english.txt
var wordsText string

var words = strings.Fields(wordsText)

// Words returns the word list.
func Words() []string {
	return append([]string(nil), words...)
}

// Derive returns the handle for a holder identity on a machine.
//
// A NUL separates the two inputs, so no identity can be shifted into the machine part.
func Derive(identity, machine string) string {
	sum := sha256.Sum256([]byte(identity + "\x00" + machine))
	bits := binary.BigEndian.Uint64(sum[:8])

	out := make([]string, wordsPerHandle)
	for i := range out {
		shift := 64 - bitsPerWord*(i+1)
		out[i] = words[(bits>>shift)&(WordCount-1)]
	}

	return strings.Join(out, "-")
}
