package encryption

import (
	"crypto/aes"
	"crypto/cipher"
)

// StreamingEncryptor writes the legacy per-part HKDF format (format version 1).
// Nothing uploads it any more, but StreamingDecryptor still has to read it, so
// the tests keep this encryptor to produce it.
type StreamingEncryptor struct {
	masterKey, fileId []byte
}

func GenerateFileId() ([]byte, error) {
	return GenerateKey() // same size and randomness as a key
}

// NewStreamingEncryptor draws a fresh master key and file ID. The part size
// only matters to the decryptor, so it is not kept.
func NewStreamingEncryptor(int64) (*StreamingEncryptor, error) {
	masterKey, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	fileId, err := GenerateFileId()
	return &StreamingEncryptor{masterKey: masterKey, fileId: fileId}, err
}

// EncryptPart pads one part and encrypts it under the key and IV derived for
// its index.
func (se *StreamingEncryptor) EncryptPart(partIndex int64, plaintext []byte) ([]byte, error) {
	key, iv, err := DerivePartKeyIV(se.masterKey, se.fileId, partIndex)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return ciphertext, nil
}

func (se *StreamingEncryptor) GetMasterKey() []byte { return se.masterKey }

func (se *StreamingEncryptor) GetFileId() []byte { return se.fileId }
