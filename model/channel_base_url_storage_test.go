package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelStorageRejectsCredentialsInBaseURL(t *testing.T) {
	db := setupCredentialStorageTest(t)
	safeBaseURL := "https://provider.example/v1?api-version=2024-02-01"
	channel := Channel{Name: "safe base URL", BaseURL: &safeBaseURL}
	require.NoError(t, db.Create(&channel).Error)

	tests := []struct {
		name    string
		baseURL string
		update  func(string) error
	}{
		{
			name:    "map update",
			baseURL: "https://provider.example/v1?api_key=map-secret",
			update: func(baseURL string) error {
				return db.Model(&Channel{}).Where("id = ?", channel.Id).
					Updates(map[string]interface{}{"base_url": baseURL}).Error
			},
		},
		{
			name:    "struct update",
			baseURL: "https://user:struct-secret@provider.example/v1",
			update: func(baseURL string) error {
				return db.Model(&Channel{}).Where("id = ?", channel.Id).
					Updates(Channel{BaseURL: &baseURL}).Error
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.update(tt.baseURL))
			var storedBaseURL string
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).
				Pluck("base_url", &storedBaseURL).Error)
			assert.Equal(t, safeBaseURL, storedBaseURL)
		})
	}
}

func TestChannelStorageRejectsCredentialBaseURLOnCreate(t *testing.T) {
	db := setupCredentialStorageTest(t)
	baseURL := "https://provider.example/v1?access_token=create-secret"
	channel := Channel{Name: "unsafe base URL", BaseURL: &baseURL}

	require.ErrorContains(t, db.Create(&channel).Error, "credential query")
	var count int64
	require.NoError(t, db.Model(&Channel{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestChannelStorageRejectsCredentialBaseURLOnSave(t *testing.T) {
	db := setupCredentialStorageTest(t)
	safeBaseURL := "http://localhost:11434/v1"
	channel := Channel{Name: "safe local base URL", BaseURL: &safeBaseURL}
	require.NoError(t, db.Create(&channel).Error)

	unsafeBaseURL := "https://provider.example/v1?X-Goog-API-Key=save-secret"
	channel.BaseURL = &unsafeBaseURL
	require.ErrorContains(t, db.Save(&channel).Error, "credential query")

	var storedBaseURL string
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).
		Pluck("base_url", &storedBaseURL).Error)
	assert.Equal(t, safeBaseURL, storedBaseURL)
}
