package transfer

// This file holds the object-metadata reading the S3 and Azure providers share.
// Both backends record the same encryption format in the object's user metadata;
// only the map differs. S3 hands back plain strings and lowercases the keys for
// us, Azure hands back pointers and preserves whatever casing the writer used.
// Each provider normalizes its map and this file decides the format, so one
// table governs both backends instead of two that can disagree.

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/crypto" // package name is 'encryption'
)

// ObjectFormat is the encryption format recorded in an object's metadata.
type ObjectFormat struct {
	// Version is 0 for legacy (one IV for the whole file), 1 for HKDF streaming
	// (a key and IV derived per part), 2 for CBC streaming (parts chained).
	Version int

	// FileID is the base64 file ID that HKDF part keys derive from (v1 only).
	FileID string

	// PartSize is the plaintext part size. Zero means the metadata did not record
	// one and the downloader must calculate it from the object size, which is how
	// files written before the field existed still decrypt.
	PartSize int64

	// IV is the decoded initialization vector, nil when the metadata carries none
	// or carries one that will not decode. The caller then falls back to the IV
	// the API reported for the file.
	IV []byte
}

// NormalizeMetadata lowercases the keys of an object-metadata map so lookups are
// case-insensitive whichever casing the backend reports.
//
// Keys are visited in order so the result never depends on Go's map iteration:
// two spellings of one key cannot both be stored (HTTP treats metadata names
// case-insensitively), but if a backend ever reported both, a value would win
// over an empty one and the earlier spelling would win any remaining tie.
func NormalizeMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}

	normalized := make(map[string]string, len(metadata))
	for _, key := range slices.Sorted(maps.Keys(metadata)) {
		lower := strings.ToLower(key)
		if held, ok := normalized[lower]; ok && held != "" {
			continue
		}
		normalized[lower] = metadata[key]
	}
	return normalized
}

// NormalizeMetadataPointers is NormalizeMetadata for the map of pointers the
// Azure SDK returns. Unset values are dropped so callers only test for "".
func NormalizeMetadataPointers(metadata map[string]*string) map[string]string {
	if metadata == nil {
		return nil
	}

	flattened := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if value != nil {
			flattened[key] = *value
		}
	}
	return NormalizeMetadata(flattened)
}

// ParseObjectFormat decides which encryption format the metadata describes.
// metadata must already have lowercased keys (see NormalizeMetadata); a nil map
// reads as legacy, which is what an object carrying no metadata at all is.
func ParseObjectFormat(metadata map[string]string) (ObjectFormat, error) {
	if metadata["formatversion"] == "1" {
		fileID, partSize, err := hkdfPartKeying(metadata)
		if err != nil {
			return ObjectFormat{}, err
		}
		return ObjectFormat{Version: 1, FileID: fileID, PartSize: partSize}, nil
	}

	// Every non-HKDF format reports the IV: legacy downloads decrypt the whole
	// file with it, CBC downloads chain the first part from it. A value that will
	// not decode is dropped rather than fatal, because the file's API record
	// carries an IV too and the caller prefers the metadata one only when it read.
	var iv []byte
	if encoded := metadata["iv"]; encoded != "" {
		if decoded, err := encryption.DecodeBase64(encoded); err == nil {
			iv = decoded
		}
	}

	if metadata["streamingformat"] == "cbc" {
		// CBC streaming, written by rescale-int v3.2.4 and later: parts decrypt
		// sequentially straight to the output file, with no encrypted temp file.
		var partSize int64
		if recorded := metadata["partsize"]; recorded != "" {
			if parsed, err := strconv.ParseInt(recorded, 10, 64); err == nil && parsed > 0 {
				partSize = parsed
			}
		}
		return ObjectFormat{Version: 2, PartSize: partSize, IV: iv}, nil
	}

	// Legacy: written by the Rescale platform or by a client old enough to
	// predate streaming. The whole file downloads, then decrypts.
	return ObjectFormat{Version: 0, IV: iv}, nil
}

// hkdfPartKeying reads the two fields HKDF (v1) per-part key derivation needs.
// partSize is scanned rather than parsed strictly, which is what both backends
// have always done: a recorded value with trailing junk still yields its number.
func hkdfPartKeying(metadata map[string]string) (fileID string, partSize int64, err error) {
	fileID = metadata["fileid"]
	if fileID == "" {
		return "", 0, fmt.Errorf("streaming format missing fileId in metadata")
	}

	recorded := metadata["partsize"]
	if recorded == "" {
		return "", 0, fmt.Errorf("streaming format missing partSize in metadata")
	}
	if _, scanErr := fmt.Sscanf(recorded, "%d", &partSize); scanErr != nil {
		return "", 0, fmt.Errorf("invalid partSize in metadata: %s", recorded)
	}

	return fileID, partSize, nil
}
