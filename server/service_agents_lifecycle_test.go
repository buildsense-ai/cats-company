package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// ---- fake 上实现新增的两个 store 方法（只覆盖测试需要的语义）----

func (f *fakeServiceAgentStore) ListServiceAgentsByOwner(_ context.Context, ownerUID int64) ([]store.ServiceAgentDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ServiceAgentDescriptor, 0, len(f.agents))
	for _, agent := range f.agents {
		if agent.OwnerKind != store.ServiceAgentOwnerUser || agent.OwnerUID != ownerUID {
			continue
		}
		if f.inactive[agent.Slug] {
			agent.State = "inactive"
		} else {
			agent.State = "active"
		}
		out = append(out, agent)
	}
	return out, nil
}

func (f *fakeServiceAgentStore) PurgeServiceAgent(_ context.Context, slug string, ownerUID int64) (store.ServiceAgentPurgeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, agent := range f.agents {
		if agent.Slug != slug {
			continue
		}
		if agent.OwnerKind != store.ServiceAgentOwnerUser || agent.OwnerUID != ownerUID {
			return store.ServiceAgentPurgeResult{Slug: slug}, store.ErrServiceAgentOwnedByOther
		}
		if !f.inactive[slug] {
			return store.ServiceAgentPurgeResult{Slug: slug}, store.ErrServiceAgentNotInactive
		}
		result := store.ServiceAgentPurgeResult{Slug: slug, Versions: 1}
		f.agents = append(f.agents[:i], f.agents[i+1:]...)
		delete(f.inactive, slug)
		return result, nil
	}
	return store.ServiceAgentPurgeResult{Slug: slug}, store.ErrServiceAgentNotFound
}

// mcpAuthStub 在 server 侧测试里把请求身份写进 context（与生产 JWT 中间件同契约）。
func lifecycleRequest(t *testing.T, method, url string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload
}

// 一条龙：上架 → 未下架时拒绝删除 → 收回 → 本人还能在「我的应用」里看到 → 彻底删除 → 查不到。
//
// 这条链路覆盖的正是「下架即失联」那个缺口：目录只列 active，所以必须有 /mine
// 让本人看得到，有 /purge 让本人删得掉。
func TestServiceAgentLifecyclePublishUnpublishPurge(t *testing.T) {
	st := newFakeServiceAgentStore()
	owner := int64(441)
	other := int64(442)
	t.Setenv("CATSCO_SERVICE_CATALOG_UID_WHITELIST", "441,442")
	ctx := context.Background()

	if err := st.ClaimServiceAgentOwnership(ctx, "lifecycle-agent", "生命周期测试", "user", owner, "private"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.PublishServiceAgentPackage(ctx, "lifecycle-agent", "生命周期测试",
		json.RawMessage(`{}`), "hash-lifecycle", json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}

	publish := NewServiceAgentPublishHandlerFromEnv(st)
	publish.SetAllowed(func(int64) bool { return true })
	catalog := NewServiceAgentCatalogHandlerFromEnv(st)

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/service-agents/{slug}", publish.HandleUnpublish)
	mux.HandleFunc("DELETE /api/service-agents/{slug}/purge", publish.HandlePurge)
	mux.HandleFunc("GET /api/service-agents/mine", catalog.HandleMine)

	ownerSrv := httptest.NewServer(mcpAuthStub(owner, mux))
	t.Cleanup(ownerSrv.Close)
	otherSrv := httptest.NewServer(mcpAuthStub(other, mux))
	t.Cleanup(otherSrv.Close)

	// 1) 还在架：彻底删除必须被拒（先收回）。
	if status, payload := lifecycleRequest(t, http.MethodDelete, ownerSrv.URL+"/api/service-agents/lifecycle-agent/purge"); status != http.StatusConflict {
		t.Fatalf("purge while active = %d %v, want 409", status, payload)
	}

	// 2) 别人删不动。
	if status, _ := lifecycleRequest(t, http.MethodDelete, otherSrv.URL+"/api/service-agents/lifecycle-agent/purge"); status != http.StatusForbidden {
		t.Fatalf("foreign purge = %d, want 403", status)
	}

	// 3) 本人收回（下架）。
	if status, payload := lifecycleRequest(t, http.MethodDelete, ownerSrv.URL+"/api/service-agents/lifecycle-agent"); status != http.StatusOK {
		t.Fatalf("unpublish = %d %v, want 200", status, payload)
	}

	// 4) 目录看不到了，但「我的应用」仍然看得到，且 state=inactive。
	listed, err := st.ListServiceAgents(ctx)
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	for _, agent := range listed {
		if agent.Slug == "lifecycle-agent" {
			t.Fatalf("unpublished agent still in catalog: %+v", agent)
		}
	}
	status, payload := lifecycleRequest(t, http.MethodGet, ownerSrv.URL+"/api/service-agents/mine")
	if status != http.StatusOK {
		t.Fatalf("mine = %d %v, want 200", status, payload)
	}
	agents, _ := payload["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("mine agents = %d, want 1 (%v)", len(agents), payload)
	}
	first, _ := agents[0].(map[string]any)
	if first["slug"] != "lifecycle-agent" || first["state"] != "inactive" {
		t.Fatalf("mine entry = %v, want slug=lifecycle-agent state=inactive", first)
	}

	// 5) 彻底删除。
	status, payload = lifecycleRequest(t, http.MethodDelete, ownerSrv.URL+"/api/service-agents/lifecycle-agent/purge")
	if status != http.StatusOK || payload["purged"] != true {
		t.Fatalf("purge = %d %v, want 200 purged", status, payload)
	}
	if status, _ := lifecycleRequest(t, http.MethodGet, ownerSrv.URL+"/api/service-agents/mine"); status != http.StatusOK {
		t.Fatalf("mine after purge = %d", status)
	}
	if agents, _ := func() ([]any, map[string]any) {
		status, payload := lifecycleRequest(t, http.MethodGet, ownerSrv.URL+"/api/service-agents/mine")
		_ = status
		list, _ := payload["agents"].([]any)
		return list, payload
	}(); len(agents) != 0 {
		t.Fatalf("mine after purge = %v, want empty", agents)
	}

	// 6) 再删一次：已经不存在。
	if status, payload := lifecycleRequest(t, http.MethodDelete, ownerSrv.URL+"/api/service-agents/lifecycle-agent/purge"); status != http.StatusNotFound {
		t.Fatalf("second purge = %d %v, want 404", status, payload)
	}
}
