package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetKeysDecodesJSONStringArrayElements(t *testing.T) {
	channel := &Channel{Key: `[" key-one ","key\\\"two",{"type":"service_account","project_id":"demo"},null]`}
	got := channel.GetKeys()
	require.Equal(t, []string{
		"key-one",
		`key\"two`,
		`{"type":"service_account","project_id":"demo"}`,
	}, got)
}

func TestGetKeysKeepsNewlineFormat(t *testing.T) {
	channel := &Channel{Key: "key-one\nkey-two\n"}
	require.Equal(t, []string{"key-one", "key-two"}, channel.GetKeys())
}
