package transfer

// CalculateTotalParts calculates the number of parts needed for a file.
func CalculateTotalParts(fileSize, partSize int64) int64 {
	if fileSize == 0 {
		return 1 // Empty files still have one part
	}
	return (fileSize + partSize - 1) / partSize
}
