package hyper

import (
	"cmp"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func resetEnabledForTest() {
	Enabled = sync.OnceValue(func() bool {
		b, _ := strconv.ParseBool(
			cmp.Or(
				os.Getenv("HYPER"),
				os.Getenv("HYPERCRUSH"),
				os.Getenv("HYPER_ENABLE"),
				os.Getenv("HYPER_ENABLED"),
			),
		)
		return b
	})
}

func resetBaseURLForTest() {
	BaseURL = sync.OnceValue(func() string {
		return cmp.Or(os.Getenv("HYPER_URL"), defaultBaseURL)
	})
}

func resetEmbeddedForTest() {
	Embedded = sync.OnceValue(func() catwalk.Provider {
		var provider catwalk.Provider
		_ = json.Unmarshal(embedded, &provider)
		if e := os.Getenv("HYPER_URL"); e != "" {
			provider.APIEndpoint = e + "/api/v1/fantasy"
		}
		return provider
	})
}

func TestEnabled(t *testing.T) {
	t.Setenv("HYPER", "")
	t.Setenv("HYPERCRUSH", "")
	t.Setenv("HYPER_ENABLE", "")
	t.Setenv("HYPER_ENABLED", "")
	resetEnabledForTest()
	require.False(t, Enabled())

	t.Setenv("HYPER", "true")
	t.Setenv("HYPERCRUSH", "")
	t.Setenv("HYPER_ENABLE", "")
	t.Setenv("HYPER_ENABLED", "")
	resetEnabledForTest()
	require.True(t, Enabled())

	t.Setenv("HYPER", "not-a-bool")
	t.Setenv("HYPERCRUSH", "true")
	t.Setenv("HYPER_ENABLE", "")
	t.Setenv("HYPER_ENABLED", "")
	resetEnabledForTest()
	require.False(t, Enabled(), "HYPER has highest priority and invalid bool resolves false")

	t.Setenv("HYPER", "")
	t.Setenv("HYPERCRUSH", "")
	t.Setenv("HYPER_ENABLE", "true")
	t.Setenv("HYPER_ENABLED", "false")
	resetEnabledForTest()
	require.True(t, Enabled(), "HYPER_ENABLE should be used before HYPER_ENABLED")
}

func TestBaseURL(t *testing.T) {
	t.Setenv("HYPER_URL", "")
	resetBaseURLForTest()
	require.Equal(t, defaultBaseURL, BaseURL())

	t.Setenv("HYPER_URL", "https://example.hyper")
	resetBaseURLForTest()
	require.Equal(t, "https://example.hyper", BaseURL())
}

func TestEmbedded(t *testing.T) {
	t.Setenv("HYPER_URL", "")
	resetEmbeddedForTest()
	p := Embedded()
	require.NotEmpty(t, p.Name)

	t.Setenv("HYPER_URL", "https://proxy.example.com")
	resetEmbeddedForTest()
	p = Embedded()
	require.Equal(t, "https://proxy.example.com/api/v1/fantasy", p.APIEndpoint)
}
