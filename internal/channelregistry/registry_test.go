package channelregistry_test

import (
	"github.com/shridarpatil/whatomate/internal/channelregistry"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCompiledRegistryAndTrustedDeploymentEndpoint(t *testing.T) {
	registry, err := channelregistry.New("")
	require.NoError(t, err)
	require.Len(t, registry.Descriptors(), 1)
	for _, url := range []string{"http://api.example.com", "https://user:secret@api.example.com", "https://api.example.com/path"} {
		_, err := channelregistry.New(url)
		require.Error(t, err)
	}
	_, err = channelregistry.New("https://fixture.example.com:8443")
	require.NoError(t, err)
}
