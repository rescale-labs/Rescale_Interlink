package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rescale/rescale-int/internal/models"
)

// mockAPIClient for testing
type mockAPIClient struct {
	coreTypes []models.CoreType
	err       error
	delay     time.Duration
}

func (m *mockAPIClient) GetCoreTypes(ctx context.Context) ([]models.CoreType, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.coreTypes, m.err
}

func TestNewAPICache(t *testing.T) {
	cache := NewAPICache()
	if cache == nil {
		t.Fatal("NewAPICache returned nil")
	}

	// Should have default analysis codes
	codes := cache.GetAnalysisCodes()
	if len(codes) == 0 {
		t.Error("Expected default analysis codes, got none")
	}

	// Should not have core types yet
	types, loading, err := cache.GetCoreTypes()
	if types != nil {
		t.Error("Expected no core types before fetch")
	}
	if loading {
		t.Error("Should not be loading initially")
	}
	if err != nil {
		t.Error("Should have no error initially")
	}
}

func TestFetchCoreTypes_Success(t *testing.T) {
	cache := NewAPICache()
	mockClient := &mockAPIClient{
		coreTypes: []models.CoreType{
			{Code: "test1", Name: "Test Type 1"},
			{Code: "test2", Name: "Test Type 2"},
		},
	}

	err := cache.FetchCoreTypes(context.Background(), mockClient)
	if err != nil {
		t.Fatalf("FetchCoreTypes failed: %v", err)
	}

	// Should now have cached core types
	types, loading, err := cache.GetCoreTypes()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if loading {
		t.Error("Should not be loading after successful fetch")
	}
	if len(types) != 2 {
		t.Errorf("Expected 2 core types, got %d", len(types))
	}
	if types[0].Code != "test1" {
		t.Errorf("Expected first type to be 'test1', got '%s'", types[0].Code)
	}

	// Should have recorded fetch time
	lastFetched := cache.GetLastFetched()
	if lastFetched.IsZero() {
		t.Error("Last fetched time should be set")
	}
}

func TestFetchCoreTypes_Error(t *testing.T) {
	cache := NewAPICache()
	mockClient := &mockAPIClient{
		err: errors.New("API error"),
	}

	err := cache.FetchCoreTypes(context.Background(), mockClient)
	if err == nil {
		t.Error("Expected error from FetchCoreTypes")
	}

	// Should fall back to defaults
	types, _, _ := cache.GetCoreTypes()
	if len(types) == 0 {
		t.Error("Expected fallback defaults, got none")
	}
	if types[0].Code != "emerald" {
		t.Error("Expected fallback defaults to include emerald")
	}
}

func TestFetchCoreTypes_Concurrent(t *testing.T) {
	cache := NewAPICache()
	mockClient := &mockAPIClient{
		coreTypes: []models.CoreType{
			{Code: "test", Name: "Test"},
		},
		delay: 100 * time.Millisecond,
	}

	// Start multiple concurrent fetches
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			done <- cache.FetchCoreTypes(context.Background(), mockClient)
		}()
	}

	// Wait for all to complete
	var errCount int
	for i := 0; i < 3; i++ {
		err := <-done
		if err != nil {
			errCount++
		}
	}

	// At most one should have an error (if any)
	// The others should return nil because loading was already in progress
	if errCount > 1 {
		t.Errorf("Expected at most 1 error, got %d", errCount)
	}

	// Should have cached data
	types, _, _ := cache.GetCoreTypes()
	if len(types) != 1 {
		t.Errorf("Expected 1 core type, got %d", len(types))
	}
}

func TestGetAnalysisCodes(t *testing.T) {
	cache := NewAPICache()
	codes := cache.GetAnalysisCodes()

	// Should include common codes
	expected := map[string]bool{
		"powerflow":     true,
		"openfoam":      true,
		"user_included": true,
	}

	for code := range expected {
		found := false
		for _, c := range codes {
			if c == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected analysis code '%s' not found", code)
		}
	}
}

func TestIsLoading(t *testing.T) {
	cache := NewAPICache()

	if cache.IsLoading() {
		t.Error("Should not be loading initially")
	}

	// Start a slow fetch
	mockClient := &mockAPIClient{
		coreTypes: []models.CoreType{{Code: "test"}},
		delay:     200 * time.Millisecond,
	}

	go cache.FetchCoreTypes(context.Background(), mockClient)

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	if !cache.IsLoading() {
		t.Error("Should be loading during fetch")
	}

	// Wait for completion
	time.Sleep(200 * time.Millisecond)

	if cache.IsLoading() {
		t.Error("Should not be loading after fetch completes")
	}
}
