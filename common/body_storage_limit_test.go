package common

import (
	"bytes"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateBodyStorageFromReaderRejectsInvalidLimitAndDeclaredOversize(t *testing.T) {
	_, err := CreateBodyStorageFromReader(nil, -1, 1)
	require.Error(t, err)

	// Non-positive limits are normalized to the finite request-body default.
	storage, err := CreateBodyStorageFromReader(bytes.NewReader([]byte("x")), -1, 1)
	require.NoError(t, err)
	require.NoError(t, storage.Close())

	_, err = CreateBodyStorageFromReader(bytes.NewReader([]byte("x")), 2, 1)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)

	// An extreme programmatic limit must be normalized before maxBytes+1 is
	// computed; this should read the small payload successfully.
	storage, err = CreateBodyStorageFromReader(bytes.NewReader([]byte("ok")), -1, math.MaxInt64)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
}
