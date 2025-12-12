# Encryption and Transfer Architecture

**Version**: v3.4.0
**Date**: December 2025

## Overview

This document captures learnings and plans regarding Rescale Interlink's encryption formats, transfer architecture, and performance considerations.

---

## Encryption Formats

Rescale Interlink supports three encryption format versions:

### v0: Legacy Format (Pre-encryption)
- **How it works**: Entire file encrypted to temp file, then uploaded
- **Download**: Downloads encrypted file to `.encrypted` temp, then decrypts
- **Metadata**: Only `iv` stored in cloud metadata
- **When used**:
  - Files uploaded by Rescale platform
  - Files uploaded with `--pre-encrypt` flag
  - Files from older rescale-int versions

### v1: HKDF Streaming Format
- **How it works**: Per-part key derivation using HKDF
- **Download**: Parts can be decrypted in parallel
- **Metadata**: `formatversion=1`, `fileid`, `partsize`
- **Status**: Legacy, superseded by v2

### v2: CBC Streaming Format (Current Default)
- **How it works**: AES-256-CBC with IV chaining across parts
- **Upload**: Encrypts on-the-fly, no temp file needed
- **Download**: Sequential decryption, no temp file needed
- **Metadata**: `streamingformat=cbc`, `iv`
- **Introduced**: v3.2.4

---

## CBC Chaining Constraint

**Critical**: CBC mode requires sequential processing due to IV chaining.

Each part's encryption depends on the previous part's final ciphertext block:
```
Part 1: IV₀ → encrypt → ciphertext₁ (last block becomes IV₁)
Part 2: IV₁ → encrypt → ciphertext₂ (last block becomes IV₂)
Part 3: IV₂ → encrypt → ciphertext₃ ...
```

### Implications

1. **Upload**: Parts MUST be encrypted sequentially (not parallelizable)
2. **Download**: Parts MUST be decrypted sequentially (not parallelizable)
3. **Multi-threading**: Can parallelize network I/O, but NOT encryption/decryption

### Why CBC Was Chosen
- FIPS 140-3 compliance requirement
- Rescale platform compatibility (expects CBC-encrypted files)
- Simpler than HKDF per-part derivation

---

## Current Transfer Architecture

### Upload Flow (Streaming, v2)
```
1. InitStreamingUpload - creates multipart upload with metadata
2. For each part (sequentially):
   a. Encrypt part using CBCStreamingEncryptor
   b. Upload encrypted part to S3/Azure
3. CompleteStreamingUpload - finalizes upload
```

### Download Flow (Streaming, v2)
```
1. DetectFormat - reads cloud metadata
2. If streamingformat=cbc:
   a. For each part (sequentially):
      - Download encrypted part
      - Decrypt using CBCStreamingDecryptor
      - Write to output file
3. Else: Fall back to legacy (temp file)
```

---

## Performance Observations

### Upload Speed
- **Observed**: 20-30 MB/s uploads vs 200+ MB/s downloads
- **Bottleneck**: Sequential encryption due to CBC chaining
- **NOT a bug**: This is a cryptographic constraint

### Download Speed
- **Observed**: 200+ MB/s on fast connections
- **Note**: Also limited by sequential decryption, but less noticeable

### Why Downloads Seem Faster
1. Download servers often have better egress bandwidth
2. User's download bandwidth typically exceeds upload bandwidth
3. Perception bias - waiting for uploads feels longer

---

## Future Optimization Options (Deferred)

These options were considered but deferred for future versions:

### Option A: Parallel Encryption with Derived IVs
- Derive each part's IV deterministically: `IV_n = HKDF(masterKey, partIndex)`
- Allows parallel encryption while maintaining compatibility
- **Tradeoff**: Requires format change, breaks streaming continuity

### Option B: Pre-compute Encryption Pipeline
- Encrypt parts ahead of network upload
- Buffer encrypted parts in memory
- **Tradeoff**: Memory usage, complexity

### Option C: Switch to CTR/GCM Mode
- Counter modes allow parallel encryption
- **Tradeoff**: May not be FIPS-compliant, Rescale compatibility unclear

### Recommendation
Keep current architecture. The CBC constraint is fundamental. Focus optimization efforts on:
- Network efficiency (connection reuse, optimal chunk sizes)
- Reducing API call overhead
- Better progress reporting

---

## Format Detection for Downloads

When downloading, format is detected from cloud storage metadata:

```go
// S3: All metadata keys are lowercased
if metadata["streamingformat"] == "cbc" {
    return v2  // CBC streaming - no temp file
}
if metadata["formatversion"] == "1" {
    return v1  // HKDF streaming
}
return v0  // Legacy - uses temp file
```

### Diagnostic Output (v3.4.0+)
With `--verbose`, downloads show:
```
Format detection successful: version=2
Format detection: version=2 (0=legacy, 1=HKDF, 2=CBC streaming)
Using CBC streaming format (v2) download - no temp file
```

---

## Temp File Behavior

| Scenario | Temp File Created? |
|----------|-------------------|
| Upload (streaming, default) | No |
| Upload (--pre-encrypt) | Yes (.encrypted) |
| Download (v2 CBC streaming) | No |
| Download (v1 HKDF streaming) | No |
| Download (v0 legacy) | Yes (.encrypted) |

If downloads create temp files unexpectedly, check:
1. Was file uploaded by rescale-int v3.2.4+?
2. Was `--pre-encrypt` used during upload?
3. Is format detection succeeding? (check verbose output)

---

## Related Files

- `internal/cloud/transfer/downloader.go` - Download orchestration
- `internal/cloud/providers/s3/streaming_concurrent.go` - S3 streaming upload/download
- `internal/cloud/providers/azure/streaming_concurrent.go` - Azure streaming
- `internal/crypto/streaming.go` - CBC streaming encryptor/decryptor
- `internal/transfer/streaming.go` - Streaming state management

---

## Version History

| Version | Changes |
|---------|---------|
| v3.2.0 | Introduced streaming encryption (HKDF v1) |
| v3.2.4 | Switched to CBC streaming (v2), added `streamingformat` metadata |
| v3.4.0 | Added format detection diagnostics, documented architecture |
