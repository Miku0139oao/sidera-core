package rule

import (
	"crypto/sha256"
	"testing"

	"github.com/Miku0139oao/sidera-core/adapter"

	"github.com/stretchr/testify/require"
)

func TestRemoteRuleSetCacheMatchesURL(t *testing.T) {
	t.Parallel()
	urlHash := sha256.Sum256([]byte("https://example.com/rules.srs"))
	ruleSet := &RemoteRuleSet{urlHash: urlHash}
	// Caches written before URLHash existed are still accepted.
	require.True(t, ruleSet.cacheMatchesURL(&adapter.SavedBinary{}))
	require.True(t, ruleSet.cacheMatchesURL(&adapter.SavedBinary{URLHash: urlHash[:]}))
	otherHash := sha256.Sum256([]byte("https://example.com/other.srs"))
	require.False(t, ruleSet.cacheMatchesURL(&adapter.SavedBinary{URLHash: otherHash[:]}))
}
