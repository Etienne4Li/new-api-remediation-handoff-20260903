package operation_setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOperationRuntimeConfigIsCopyOnWrite exercises the same update path used
// by option hot reloads while many request-style readers retain snapshots.
// The generation marker in every field makes a torn publication observable;
// -race additionally verifies that no reader touches a mutable backing slice.
func TestOperationRuntimeConfigIsCopyOnWrite(t *testing.T) {
	original := GetOperationRuntimeConfig()
	t.Cleanup(func() {
		UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
			*config = original
		})
	})

	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.DemoSiteEnabled = true
		config.SelfUseModeEnabled = false
		config.AutomaticDisableKeywords = []string{"generation:0"}
		config.AutomaticDisableStatusCodeRanges = []StatusCodeRange{{Start: 100, End: 100}}
		config.AutomaticRetryStatusCodeRanges = []StatusCodeRange{{Start: 200, End: 200}}
	})

	// Returned slices are detached from the published value.
	copy := GetOperationRuntimeConfig()
	copy.AutomaticDisableKeywords[0] = "mutated"
	copy.AutomaticDisableStatusCodeRanges[0].Start = 599
	unchanged := GetOperationRuntimeConfig()
	require.Equal(t, "generation:0", unchanged.AutomaticDisableKeywords[0])
	require.Equal(t, StatusCodeRange{Start: 100, End: 100}, unchanged.AutomaticDisableStatusCodeRanges[0])

	const iterations = 2000
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for generation := 1; generation <= iterations; generation++ {
			g := generation
			UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
				config.DemoSiteEnabled = g%2 == 0
				config.SelfUseModeEnabled = g%2 == 1
				config.AutomaticDisableKeywords = []string{fmt.Sprintf("generation:%d", g)}
				config.AutomaticDisableStatusCodeRanges = []StatusCodeRange{{Start: 100, End: 100 + g%400}}
				config.AutomaticRetryStatusCodeRanges = []StatusCodeRange{{Start: 200, End: 200 + (g*3)%399}}
			})
		}
	}()

	for reader := 0; reader < 7; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				config := GetOperationRuntimeConfig()
				if len(config.AutomaticDisableKeywords) != 1 || len(config.AutomaticDisableStatusCodeRanges) != 1 || len(config.AutomaticRetryStatusCodeRanges) != 1 {
					errs <- fmt.Errorf("invalid operation snapshot: %+v", config)
					return
				}
				generationText := strings.TrimPrefix(config.AutomaticDisableKeywords[0], "generation:")
				generation, err := strconv.Atoi(generationText)
				if err != nil || generation < 0 || generation > iterations {
					errs <- fmt.Errorf("invalid operation generation: %+v", config)
					return
				}
				expectedDemoSite := generation%2 == 0
				expectedSelfUse := generation%2 == 1
				if config.DemoSiteEnabled != expectedDemoSite || config.SelfUseModeEnabled != expectedSelfUse {
					errs <- fmt.Errorf("mixed operation flags: %+v", config)
					return
				}
				if config.AutomaticDisableStatusCodeRanges[0].End != 100+generation%400 || config.AutomaticRetryStatusCodeRanges[0].End != 200+(generation*3)%399 {
					errs <- fmt.Errorf("mixed operation ranges: %+v", config)
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}
