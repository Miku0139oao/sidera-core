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
