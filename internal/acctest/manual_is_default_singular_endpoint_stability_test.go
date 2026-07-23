package acctest

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// TestManualIsDefaultSingularEndpointStability is a manual, real-backend
// diagnostic from the anyscale_cloud.is_default investigation - NOT an
// automated acceptance test. Deliberately does NOT start with "TestAcc": that
// prefix means "runs in a CI shard" everywhere else in this repo (see
// .github/workflows/ci.yml's ^TestAcc[A-Za-z]+DataSource / ^TestAcc[A-Za-z]+
// Resource regexes), and this test doesn't fit either category - it isn't
// exercising a resource or data source's CRUD behavior, it's probing raw
// backend read stability. Giving it a TestAcc name would either silently
// never run (wrong shard regex) or run somewhere it doesn't belong; staying
// unprefixed keeps that ambiguity from ever existing. Run manually with
// TF_ACC=1 go test ./internal/acctest/... -run TestManualIsDefaultSingularEndpointStability -v
//
// Finding it confirmed (6 calls, ~2s apart, against the pinned static test
// cloud): the singular GET /api/v2/clouds/{id} endpoint - the only one
// anyscale_cloud's Read/ImportState path ever calls - returns a stable
// is_default across repeated calls. This rules out "the singular endpoint
// itself is racy" as the mechanism behind the reported idle-plan noise,
// distinct from the already-confirmed singular-vs-plural cross-endpoint
// disagreement (commit 082e29f, workbench #94), which this resource's Read
// path never exercises in the first place. Read-only, no mutation, no token
// ever logged.
func TestManualIsDefaultSingularEndpointStability(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)

	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}

	cloudID := GetTestCloudID(t)
	ctx := context.Background()

	var values []bool
	for i := 0; i < 6; i++ {
		resp, err := provider.DoRequestAndParse[provider.CloudResponse](
			ctx, client, "GET", fmt.Sprintf("/api/v2/clouds/%s", cloudID), nil, http.StatusOK,
		)
		if err != nil {
			t.Fatalf("call %d: GET /clouds/%s failed: %v", i, cloudID, err)
		}
		values = append(values, resp.Result.IsDefault)
		t.Logf("call %d: is_default=%v", i, resp.Result.IsDefault)
		time.Sleep(2 * time.Second)
	}

	for i := 1; i < len(values); i++ {
		if values[i] != values[0] {
			t.Errorf("is_default UNSTABLE across repeated singular GETs: call 0=%v, call %d=%v", values[0], i, values[i])
		}
	}
}
