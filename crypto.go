package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

func generateAESKeyIV() ([]byte, []byte, error) {
	// Generate random 16-byte hex strings (like Python library)
	keyBytes := make([]byte, 8)
	ivBytes := make([]byte, 8)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to generate AES key: %v", err)
	}
	if _, err := rand.Read(ivBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to generate AES IV: %v", err)
	}

	key := []byte(hex.EncodeToString(keyBytes))
	iv := []byte(hex.EncodeToString(ivBytes))

	return key, iv, nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

func pkcs7Unpad(data []byte) []byte {
	length := len(data)
	if length == 0 {
		return data
	}
	padding := int(data[length-1])
	if padding == 0 || padding > length || padding > aes.BlockSize {
		return data
	}
	// Validate all padding bytes are identical
	for i := length - padding; i < length; i++ {
		if data[i] != byte(padding) {
			return data
		}
	}
	return data[:length-padding]
}

func aesEncrypt(data, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	padded := pkcs7Pad(data, aes.BlockSize)
	encrypted := make([]byte, len(padded))

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(encrypted, padded)

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func aesDecrypt(data string, key, iv []byte) ([]byte, error) {
	encrypted, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a multiple of block size")
	}

	decrypted := make([]byte, len(encrypted))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, encrypted)

	return pkcs7Unpad(decrypted), nil
}

func rsaEncryptPKCS1(data []byte, n *big.Int, e int) ([]byte, error) {
	// Go's crypto/rsa rejects keys < 1024 bits, so we implement PKCS1v1.5 manually
	modSize := (n.BitLen() + 7) / 8

	// PKCS#1 v1.5 padding: 0x00 0x02 [random non-zero bytes] 0x00 [data]
	if len(data) > modSize-11 {
		return nil, fmt.Errorf("message too long for RSA key size")
	}

	padded := make([]byte, modSize)
	padded[0] = 0x00
	padded[1] = 0x02

	// Fill with random non-zero bytes
	psLen := modSize - len(data) - 3
	buf := make([]byte, psLen*2)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("failed to generate RSA padding: %v", err)
	}
	for i, j := 0, 0; i < psLen; j++ {
		if j >= len(buf) {
			// Extremely unlikely: refill if we filtered too many zeroes
			if _, err := rand.Read(buf); err != nil {
				return nil, fmt.Errorf("failed to generate RSA padding: %v", err)
			}
			j = 0
		}
		if buf[j] != 0 {
			padded[2+i] = buf[j]
			i++
		}
	}
	padded[2+psLen] = 0x00
	copy(padded[3+psLen:], data)

	// RSA: c = m^e mod n
	m := new(big.Int).SetBytes(padded)
	c := new(big.Int).Exp(m, big.NewInt(int64(e)), n)

	// Ensure output is modSize bytes
	result := c.Bytes()
	if len(result) < modSize {
		padResult := make([]byte, modSize)
		copy(padResult[modSize-len(result):], result)
		result = padResult
	}

	return result, nil
}

func rsaEncrypt(data string, n *big.Int, e int) (string, error) {
	modSize := (n.BitLen() + 7) / 8
	step := 53 // Python uses 53 byte chunks

	var result strings.Builder
	for i := 0; i < len(data); i += step {
		end := i + step
		if end > len(data) {
			end = len(data)
		}
		chunk := []byte(data[i:end])

		encrypted, err := rsaEncryptPKCS1(chunk, n, e)
		if err != nil {
			return "", fmt.Errorf("RSA encrypt chunk at offset %d: %v", i, err)
		}

		encHex := hex.EncodeToString(encrypted)
		for len(encHex) < modSize*2 {
			encHex = "0" + encHex
		}
		result.WriteString(encHex)
	}
	return result.String(), nil
}
