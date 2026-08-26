package transfer

import (
	"bytes"
	"testing"
)

// TestParseObjectFormat pins the format decision for every metadata shape the
// two backends produce. S3 lowercases metadata keys and Azure keeps whatever
// casing the writer used, so each case is parsed twice — once as S3 reports it
// and once through Azure's map of pointers with the casing the metadata carries.
// Both must reach the same version, and this table is the record of which
// version each input means.
func TestParseObjectFormat(t *testing.T) {
	// "AQID" is base64 for the bytes 1, 2, 3.
	decodedIV := []byte{1, 2, 3}

	tests := []struct {
		name     string
		metadata map[string]string
		want     ObjectFormat
		wantErr  string
	}{{
		name:     "no metadata at all is legacy",
		metadata: nil,
		want:     ObjectFormat{Version: 0},
	}, {
		name:     "empty metadata is legacy",
		metadata: map[string]string{},
		want:     ObjectFormat{Version: 0},
	}, {
		name:     "legacy carries only an IV",
		metadata: map[string]string{"iv": "AQID"},
		want:     ObjectFormat{Version: 0, IV: decodedIV},
	}, {
		name:     "an IV that will not decode is dropped, not fatal",
		metadata: map[string]string{"iv": "not base64!!"},
		want:     ObjectFormat{Version: 0},
	}, {
		name:     "HKDF as S3 reports it",
		metadata: map[string]string{"formatversion": "1", "fileid": "Zm8=", "partsize": "1048576"},
		want:     ObjectFormat{Version: 1, FileID: "Zm8=", PartSize: 1048576},
	}, {
		name:     "HKDF as Azure title-cases it",
		metadata: map[string]string{"FormatVersion": "1", "FileId": "Zm8=", "PartSize": "1048576"},
		want:     ObjectFormat{Version: 1, FileID: "Zm8=", PartSize: 1048576},
	}, {
		name:     "HKDF ignores an IV it does not use",
		metadata: map[string]string{"formatversion": "1", "fileid": "Zm8=", "partsize": "64", "iv": "AQID"},
		want:     ObjectFormat{Version: 1, FileID: "Zm8=", PartSize: 64},
	}, {
		name:     "HKDF partSize is scanned, so trailing junk still yields its number",
		metadata: map[string]string{"formatversion": "1", "fileid": "Zm8=", "partsize": "1024x"},
		want:     ObjectFormat{Version: 1, FileID: "Zm8=", PartSize: 1024},
	}, {
		name:     "HKDF without a fileId cannot derive part keys",
		metadata: map[string]string{"formatversion": "1", "partsize": "64"},
		wantErr:  "streaming format missing fileId in metadata",
	}, {
		name:     "HKDF without a partSize cannot find its parts",
		metadata: map[string]string{"formatversion": "1", "fileid": "Zm8="},
		wantErr:  "streaming format missing partSize in metadata",
	}, {
		name:     "HKDF with an unreadable partSize names the value",
		metadata: map[string]string{"formatversion": "1", "fileid": "Zm8=", "partsize": "huge"},
		wantErr:  "invalid partSize in metadata: huge",
	}, {
		name:     "a formatVersion other than 1 does not select HKDF",
		metadata: map[string]string{"formatversion": "2", "streamingformat": "cbc", "iv": "AQID"},
		want:     ObjectFormat{Version: 2, IV: decodedIV},
	}, {
		name:     "CBC as S3 reports it",
		metadata: map[string]string{"streamingformat": "cbc", "partsize": "8388608", "iv": "AQID"},
		want:     ObjectFormat{Version: 2, PartSize: 8388608, IV: decodedIV},
	}, {
		name:     "CBC as Azure title-cases it",
		metadata: map[string]string{"StreamingFormat": "cbc", "PartSize": "8388608", "IV": "AQID"},
		want:     ObjectFormat{Version: 2, PartSize: 8388608, IV: decodedIV},
	}, {
		name:     "CBC without a partSize leaves it to be calculated",
		metadata: map[string]string{"streamingformat": "cbc", "iv": "AQID"},
		want:     ObjectFormat{Version: 2, PartSize: 0, IV: decodedIV},
	}, {
		name:     "CBC with a zero partSize leaves it to be calculated",
		metadata: map[string]string{"streamingformat": "cbc", "partsize": "0"},
		want:     ObjectFormat{Version: 2, PartSize: 0},
	}, {
		name:     "CBC with a negative partSize leaves it to be calculated",
		metadata: map[string]string{"streamingformat": "cbc", "partsize": "-8"},
		want:     ObjectFormat{Version: 2, PartSize: 0},
	}, {
		name:     "CBC with an unreadable partSize leaves it to be calculated",
		metadata: map[string]string{"streamingformat": "cbc", "partsize": "big"},
		want:     ObjectFormat{Version: 2, PartSize: 0},
	}, {
		name:     "the streaming format marker must read exactly cbc",
		metadata: map[string]string{"streamingformat": "CBC", "iv": "AQID"},
		want:     ObjectFormat{Version: 0, IV: decodedIV},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, shape := range []struct {
				backend    string
				normalized map[string]string
			}{
				{"s3", NormalizeMetadata(tt.metadata)},
				{"azure", NormalizeMetadataPointers(pointerMetadata(tt.metadata))},
			} {
				got, err := ParseObjectFormat(shape.normalized)

				if tt.wantErr != "" {
					if err == nil {
						t.Fatalf("%s: ParseObjectFormat returned no error, want %q", shape.backend, tt.wantErr)
					}
					if err.Error() != tt.wantErr {
						t.Fatalf("%s: error = %q, want %q", shape.backend, err, tt.wantErr)
					}
					continue
				}

				if err != nil {
					t.Fatalf("%s: ParseObjectFormat: %v", shape.backend, err)
				}
				if got.Version != tt.want.Version {
					t.Errorf("%s: version = %d, want %d", shape.backend, got.Version, tt.want.Version)
				}
				if got.FileID != tt.want.FileID {
					t.Errorf("%s: fileID = %q, want %q", shape.backend, got.FileID, tt.want.FileID)
				}
				if got.PartSize != tt.want.PartSize {
					t.Errorf("%s: partSize = %d, want %d", shape.backend, got.PartSize, tt.want.PartSize)
				}
				if !bytes.Equal(got.IV, tt.want.IV) {
					t.Errorf("%s: IV = %v, want %v", shape.backend, got.IV, tt.want.IV)
				}
			}
		})
	}
}

// TestNormalizeMetadataPointersDropsUnsetValues covers the one difference between
// the two maps: Azure reports a metadata name with no value as a nil pointer, and
// callers only ever test for the empty string.
func TestNormalizeMetadataPointersDropsUnsetValues(t *testing.T) {
	value := "cbc"
	got := NormalizeMetadataPointers(map[string]*string{"StreamingFormat": &value, "PartSize": nil})

	if got["streamingformat"] != "cbc" {
		t.Errorf("streamingformat = %q, want %q", got["streamingformat"], "cbc")
	}
	if _, present := got["partsize"]; present {
		t.Errorf("an unset partsize was kept: %#v", got)
	}
}

func pointerMetadata(metadata map[string]string) map[string]*string {
	if metadata == nil {
		return nil
	}
	pointers := make(map[string]*string, len(metadata))
	for key, value := range metadata {
		pointers[key] = &value
	}
	return pointers
}
