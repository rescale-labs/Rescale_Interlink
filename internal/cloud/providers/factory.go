// Package providers contains the cloud storage provider implementations
// and factory for creating them based on storage type.
package providers

import (
	"context"
	"fmt"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/cloud/providers/azure"
	"github.com/rescale/rescale-int/internal/cloud/providers/s3"
	"github.com/rescale/rescale-int/internal/models"
)

// Factory implements CloudTransferFactory and creates providers based on storage type.
type Factory struct{}

// NewFactory creates a new provider factory.
func NewFactory() *Factory {
	return &Factory{}
}

// NewTransfer creates a CloudTransfer for the specified storage type.
// storageType should be "S3Storage" or "AzureStorage".
func (f *Factory) NewTransfer(
	ctx context.Context,
	storageType string,
	storageInfo *models.StorageInfo,
	apiClient *api.Client,
) (cloud.CloudTransfer, error) {
	switch storageType {
	case "S3Storage":
		return s3.NewProvider(storageInfo, apiClient)
	case "AzureStorage":
		return azure.NewProvider(storageInfo, apiClient)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}

// NewTransferFromStorageInfo creates a CloudTransfer from StorageInfo.
// This is a convenience method that extracts the storage type from StorageInfo.
func (f *Factory) NewTransferFromStorageInfo(
	ctx context.Context,
	storageInfo *models.StorageInfo,
	apiClient *api.Client,
) (cloud.CloudTransfer, error) {
	if storageInfo == nil {
		return nil, fmt.Errorf("storageInfo is required")
	}
	return f.NewTransfer(ctx, storageInfo.StorageType, storageInfo, apiClient)
}

// Compile-time interface verification
var _ cloud.CloudTransferFactory = (*Factory)(nil)
