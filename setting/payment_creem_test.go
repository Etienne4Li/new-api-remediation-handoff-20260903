package setting

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreemConfigPublishesCoherentSnapshots protects the checkout/webhook
// contract: a reader must observe one complete credential/mode/catalogue
// tuple, never fields assembled from two concurrent hot updates.
func TestCreemConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := GetCreemConfig()
	t.Cleanup(func() {
		UpdateCreemConfig(func(cfg *CreemConfig) { *cfg = previous })
	})

	first := CreemConfig{
		ApiKey:        "key-a",
		Products:      `[{"productId":"prod-a"}]`,
		TestMode:      false,
		WebhookSecret: "secret-a",
	}
	second := CreemConfig{
		ApiKey:        "key-b",
		Products:      `[{"productId":"prod-b"}]`,
		TestMode:      true,
		WebhookSecret: "secret-b",
	}
	UpdateCreemConfig(func(cfg *CreemConfig) { *cfg = first })

	start := make(chan struct{})
	var wg sync.WaitGroup
	updates := []CreemConfig{first, second}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			value := updates[i%len(updates)]
			UpdateCreemConfig(func(cfg *CreemConfig) { *cfg = value })
		}
	}()

	seen := make(chan CreemConfig, 128)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			seen <- GetCreemConfig()
		}
	}()

	close(start)
	wg.Wait()
	close(seen)
	for got := range seen {
		valid := got == first || got == second
		require.Truef(t, valid, "mixed Creem snapshot: %+v", got)
	}
}
