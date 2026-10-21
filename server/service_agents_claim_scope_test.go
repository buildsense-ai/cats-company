package server

import (
	"context"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// scopedClaimStore 记录 scoped 认领收到的 slug 列表（验证 worker 侧声明生效）。
type scopedClaimStore struct {
	*fakeServiceAgentStore
	gotSlugs []string
	calls    int
}

func (s *scopedClaimStore) ClaimServiceRunsScoped(_ context.Context, _ string, _ int, agentSlugs []string) ([]store.ServiceRun, error) {
	s.calls++
	s.gotSlugs = append([]string(nil), agentSlugs...)
	return []store.ServiceRun{}, nil
}

// worker 声明了 agent_slugs 时，必须走 scoped 认领并原样透传（否则会抢别人的 run）。
func TestClaimRunsUsesScopedClaimerWhenSlugsProvided(t *testing.T) {
	st := &scopedClaimStore{fakeServiceAgentStore: newFakeServiceAgentStore()}
	o := NewServiceRunOrchestrator(st, "instance-token", "secret")

	if _, err := o.claimRuns(context.Background(), "inst-1", 1, []string{" my-agent ", "", "other"}); err != nil {
		t.Fatalf("claimRuns: %v", err)
	}
	if st.calls != 1 {
		t.Fatalf("scoped claimer calls = %d, want 1", st.calls)
	}
	if len(st.gotSlugs) != 2 || st.gotSlugs[0] != "my-agent" || st.gotSlugs[1] != "other" {
		t.Fatalf("agent slugs = %+v, want [my-agent other]", st.gotSlugs)
	}

	// 不声明 slug → 退回不带过滤的认领，不应再调 scoped。
	if _, err := o.claimRuns(context.Background(), "inst-1", 1, nil); err != nil {
		t.Fatalf("claimRuns unscoped: %v", err)
	}
	if st.calls != 1 {
		t.Fatalf("unscoped path should not call scoped claimer, calls=%d", st.calls)
	}
}
