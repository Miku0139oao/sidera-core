package main

import (
	"testing"

	"github.com/Miku0139oao/sidera-core/config"
	"github.com/stretchr/testify/require"
)

func TestRejectXrayConfigOutput(t *testing.T) {
	require.NoError(t, rejectXrayConfigOutput([]*OptionsEntry{{
		path:    "native.json",
		dialect: config.DialectSingBox,
	}}, "formatting"))
	require.ErrorContains(t, rejectXrayConfigOutput([]*OptionsEntry{
		{path: "native.json", dialect: config.DialectSingBox},
		{path: "xray.json", dialect: config.DialectXray},
	}, "formatting"), "formatting Xray configuration files is not implemented: xray.json")
}
