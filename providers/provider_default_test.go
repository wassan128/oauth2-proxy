package providers

import (
	"testing"
	"time"

	"github.com/oauth2-proxy/oauth2-proxy/pkg/apis/sessions"
	"github.com/stretchr/testify/assert"
)

func TestRefresh(t *testing.T) {
	p := &ProviderData{}
	expires := time.Now().Add(time.Duration(-11) * time.Minute)
	refreshed, err := p.RefreshSessionIfNeeded(&sessions.SessionState{
		ExpiresOn: &expires,
	})
	assert.Equal(t, false, refreshed)
	assert.Equal(t, nil, err)
}
