package scopetest

import (
	"testing"
)

// TestThreeRefusesAParallelTest: a scopetest helper swaps the process-wide
// scope table, so a parallel caller must panic at once instead of racing the
// other tests of its package.
func TestThreeRefusesAParallelTest(t *testing.T) {
	for name, install := range map[string]func(testing.TB){
		"Three":         Three,
		"RenamedMaster": RenamedMaster,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s accepted a parallel test", name)
				}
			}()
			install(t)
		})
	}
}
