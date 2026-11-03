package parser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rc4"
	"errors"
	"io"
	"testing"
)

func encryptedValue(t *testing.T, key []byte, useAES bool, ptr objptr, plain []byte) []byte {
	t.Helper()
	derived := cryptKey(key, useAES, ptr)
	if !useAES {
		block, err := rc4.NewCipher(derived)
		if err != nil {
			t.Fatal(err)
		}
		result := make([]byte, len(plain))
		block.XORKeyStream(result, plain)
		return result
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{7}, aes.BlockSize)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	return append(iv, padded...)
}

func TestEncryptedStringsAndStreamsUseTheSameObjectIdentity(t *testing.T) {
	identity := objptr{42, 7}
	for _, useAES := range []bool{false, true} {
		for _, length := range []int{0, 1, aes.BlockSize - 1, aes.BlockSize, 3*aes.BlockSize + 1} {
			key := bytes.Repeat([]byte{3}, 16)
			plain := bytes.Repeat([]byte{'A'}, length)
			wire := encryptedValue(t, key, useAES, identity, plain)
			if got := decryptString(key, useAES, identity, string(wire)); got != string(plain) {
				t.Fatalf("decrypted string includes padding: %q", got)
			}
			stream := decryptStream(key, useAES, identity, bytes.NewReader(wire))
			if n, err := stream.Read(nil); n != 0 || err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("empty read=%d %v", n, err)
			}
			var got bytes.Buffer
			for {
				next := make([]byte, 1)
				n, err := stream.Read(next)
				got.Write(next[:n])
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(got.Bytes(), plain) {
				t.Fatalf("stream=%q want=%q", got.Bytes(), plain)
			}
		}
	}
	shortKey := []byte{1, 2, 3, 4, 5}
	if len(cryptKey(shortKey, false, identity)) != 10 {
		t.Fatal("40-bit document used a 128-bit object key")
	}
}

func TestEncryptedStreamsRejectTruncationAndInvalidPadding(t *testing.T) {
	key := bytes.Repeat([]byte{3}, aes.BlockSize)
	identity := objptr{42, 7}
	wire := encryptedValue(t, key, true, identity, []byte("hello"))
	for _, size := range []int{0, 1, aes.BlockSize - 1, aes.BlockSize, len(wire) - 1} {
		_, err := io.ReadAll(decryptStream(key, true, identity, bytes.NewReader(wire[:size])))
		if !errors.Is(err, errInvalidEncryptedData) {
			t.Fatalf("size=%d error=%v", size, err)
		}
	}
	for _, padding := range []byte{0, aes.BlockSize + 1, 2} {
		block, err := aes.NewCipher(cryptKey(key, true, identity))
		if err != nil {
			t.Fatal(err)
		}
		iv := bytes.Repeat([]byte{7}, aes.BlockSize)
		plain := bytes.Repeat([]byte{'x'}, aes.BlockSize)
		plain[len(plain)-1] = padding
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(plain, plain)
		_, err = io.ReadAll(decryptStream(key, true, identity, bytes.NewReader(append(iv, plain...))))
		if !errors.Is(err, errInvalidEncryptedData) {
			t.Fatalf("padding=%d error=%v", padding, err)
		}
	}
}
