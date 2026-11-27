package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranslateXrayAccountsUseSchemaFieldNames(t *testing.T) {
	accounts := translateAccounts([]xrayAccount{{User: "alice", Pass: "secret"}})
	require.Equal(t, "alice", accounts[0]["Username"])
	require.Equal(t, "secret", accounts[0]["Password"])
	require.NotContains(t, accounts[0], "username")
	require.NotContains(t, accounts[0], "password")
}

func TestNormalizeXrayUUIDOrAliasRejectsTrailingInput(t *testing.T) {
	const canonical = "27848739-7e62-4138-9fd3-098a63964b6b"
	normalized, err := normalizeXrayUUIDOrAlias(canonical)
	require.NoError(t, err)
	require.Equal(t, canonical, normalized)

	// Inputs longer than a UUID's 32 hex digits but shorter than the
	// bracketed maximum must not be silently truncated to a valid UUID.
	_, err = normalizeXrayUUIDOrAlias("278487397e6241389fd3098a63964b6b00")
	require.Error(t, err)
	_, err = normalizeXrayUUIDOrAlias("278487397e6241389fd3098a63964b6b000")
	require.Error(t, err)
}
