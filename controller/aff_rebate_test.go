package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The masked label is the whole privacy boundary of the rebate detail API: the
// inviter may recognise a friend, never read their address.
func TestMaskInviteeIdentityNeverReturnsFullAddress(t *testing.T) {
	cases := []struct {
		name     string
		userId   int
		username string
		email    string
		expected string
	}{
		{"spec example", 12, "someone", "user@example.com", "u***@ex***.com"},
		{"multi label domain", 12, "someone", "alice@mail.corp.co.uk", "a***@ma***.corp.co.uk"},
		{"short local part", 12, "someone", "a@example.com", "***@ex***.com"},
		{"short domain label", 12, "someone", "alice@ex.com", "a***@***.com"},
		{"no dot in domain", 12, "someone", "alice@localhost", "a***@lo***"},
		{"no email falls back to username", 12, "alice", "", "a***"},
		{"single rune username", 12, "a", "", "用户 #12"},
		{"nothing identifiable", 12, "", "", "用户 #12"},
		{"malformed email", 12, "alice", "not-an-email", "a***"},
		{"unicode username", 12, "张三李四", "", "张***"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskInviteeIdentity(tc.userId, tc.username, tc.email)
			require.Equal(t, tc.expected, got)
			if tc.email != "" {
				require.NotContains(t, got, tc.email, "the full email must never survive masking")
			}
			if len([]rune(tc.username)) > 1 {
				require.NotContains(t, got, tc.username, "the full username must never survive masking")
			}
		})
	}
}

func TestMaskIdentifierPartHidesShortValues(t *testing.T) {
	require.Equal(t, "***", maskIdentifierPart("", 1))
	require.Equal(t, "***", maskIdentifierPart("a", 1))
	require.Equal(t, "a***", maskIdentifierPart("ab", 1))
	require.Equal(t, "***", maskIdentifierPart("ab", 2))
	require.Equal(t, "ab***", maskIdentifierPart("abc", 2))
}
