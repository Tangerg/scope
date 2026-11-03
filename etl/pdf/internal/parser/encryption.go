// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"errors"
	"io"
	"strings"
)

var errInvalidEncryptedData = errors.New("pdf: malformed encrypted data")

func cryptKey(key []byte, useAES bool, ptr objptr) []byte {
	hash := md5.New()
	hash.Write(key)
	hash.Write([]byte{byte(ptr.id), byte(ptr.id >> 8), byte(ptr.id >> 16), byte(ptr.gen), byte(ptr.gen >> 8)})
	if useAES {
		hash.Write([]byte("sAlT"))
	}
	// PDF 32000-1 algorithm 1 uses min(key bytes + 5, 16). A 40-bit
	// document key therefore needs a 10-byte object key, not the whole digest.
	return hash.Sum(nil)[:min(len(key)+5, md5.Size)]
}

func decryptString(key []byte, useAES bool, ptr objptr, raw string) string {
	decoded, err := io.ReadAll(decryptStream(key, useAES, ptr, strings.NewReader(raw)))
	if err != nil {
		panic(err)
	}
	return string(decoded)
}

func decryptStream(key []byte, useAES bool, ptr objptr, source io.Reader) io.Reader {
	key = cryptKey(key, useAES, ptr)
	if !useAES {
		stream, err := rc4.NewCipher(key)
		if err != nil {
			return &errorReadCloser{err}
		}
		return &cipher.StreamReader{S: stream, R: source}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return &errorReadCloser{err}
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(source, iv); err != nil {
		return &errorReadCloser{errors.Join(errInvalidEncryptedData, err)}
	}
	return &cbcReader{mode: cipher.NewCBCDecrypter(block, iv), source: bufio.NewReader(source), block: make([]byte, aes.BlockSize)}
}

// PDF AES values end in PKCS#7 padding. A one-byte lookahead identifies the
// final block without retaining the whole stream or exposing padding as text.
type cbcReader struct {
	mode     cipher.BlockMode
	source   *bufio.Reader
	block    []byte
	pending  []byte
	terminal error
}

func (c *cbcReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 {
		if c.terminal != nil {
			return 0, c.terminal
		}
		if _, err := io.ReadFull(c.source, c.block); err != nil {
			c.terminal = errors.Join(errInvalidEncryptedData, err)
			return 0, c.terminal
		}
		c.mode.CryptBlocks(c.block, c.block)
		c.pending = c.block
		if _, err := c.source.Peek(1); err != nil {
			if err != io.EOF {
				c.terminal = err
				c.pending = nil
				return 0, err
			}
			padding := int(c.block[len(c.block)-1])
			if padding == 0 || padding > len(c.block) || !bytes.Equal(c.block[len(c.block)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
				c.terminal = errInvalidEncryptedData
				c.pending = nil
				return 0, c.terminal
			}
			c.pending = c.block[:len(c.block)-padding]
			c.terminal = io.EOF
		}
	}
	n := copy(data, c.pending)
	c.pending = c.pending[n:]
	if n == 0 {
		return 0, c.terminal
	}
	return n, nil
}
