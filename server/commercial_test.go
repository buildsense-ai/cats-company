package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestValidateCommercialOfficialPaidPlanModelsRequiresAllPublicModels(t *testing.T) {
	complete := map[string]float64{
		"MiniMax-M2.7": 1500, "MiniMax-M3": 1500, "deepseek-v4-flash": 1500, "glm-5.3-flash": 1500,
		"gpt-5.6-terra": 1500, "gpt-5.6-sol": 1500, "gpt-5.6-luna": 1500,
	}
	if err := validateCommercialOfficialPaidPlanModels("catsco-personal", complete); err != nil {
		t.Fatalf("complete paid plan rejected: %v", err)
	}
	delete(complete, "gpt-5.6-luna")
	if err := validateCommercialOfficialPaidPlanModels("catsco-pro", complete); err == nil {
		t.Fatal("incomplete paid plan was accepted")
	}
	if err := validateCommercialOfficialPaidPlanModels("catsco-free", map[string]float64{}); err != nil {
		t.Fatalf("non-official plan should not be constrained: %v", err)
	}
}

type commercialTestStore struct {
	nextID          int64
	plans           []*types.CommercialPlan
	invites         []*types.CommercialInviteCode
	entitlements    map[int64][]*types.CommercialEntitlement
	grants          map[int64][]*types.CommercialQuotaGrant
	ledger          map[int64][]*types.CommercialLedgerEntry
	redeemedInvites map[int64]map[string]struct{}
}

func newCommercialTestStore() *commercialTestStore {
	return &commercialTestStore{
		nextID:          1,
		entitlements:    map[int64][]*types.CommercialEntitlement{},
		grants:          map[int64][]*types.CommercialQuotaGrant{},
		ledger:          map[int64][]*types.CommercialLedgerEntry{},
		redeemedInvites: map[int64]map[string]struct{}{},
	}
}

func TestPublicCommercialSummaryIncludesPlanIdentity(t *testing.T) {
	startsAt := time.Now().UTC()
	summary := publicCommercialSummary(&types.CommercialSummary{
		Entitlements: []*types.CommercialEntitlement{{
			PlanID: 7, PlanSlug: "catsco-pro", PlanName: "专业版", Source: "invite",
			State: "active", StartsAt: startsAt,
		}},
	})
	if len(summary.Entitlements) != 1 {
		t.Fatalf("unexpected public entitlements: %#v", summary.Entitlements)
	}
	got := summary.Entitlements[0]
	if got.PlanID != 7 || got.PlanSlug != "catsco-pro" || got.PlanName != "专业版" {
		t.Fatalf("public entitlement lost plan identity: %#v", got)
	}
}

func (s *commercialTestStore) next() int64 {
	id := s.nextID
	s.nextID++
	return id
}

func (s *commercialTestStore) ListCommercialPlans(includeDisabled bool) ([]*types.CommercialPlan, error) {
	out := []*types.CommercialPlan{}
	for _, plan := range s.plans {
		if includeDisabled || plan.State == 0 {
			cp := *plan
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *commercialTestStore) CreateCommercialPlan(plan *types.CommercialPlan) (int64, error) {
	cp := *plan
	for index, existing := range s.plans {
		if existing.Slug == cp.Slug {
			cp.ID = existing.ID
			s.plans[index] = &cp
			return cp.ID, nil
		}
	}
	cp.ID = s.next()
	if cp.DurationDays <= 0 {
		cp.DurationDays = 30
	}
	s.plans = append(s.plans, &cp)
	return cp.ID, nil
}

func (s *commercialTestStore) ListCommercialInviteCodes(limit int) ([]*types.CommercialInviteCode, error) {
	out := []*types.CommercialInviteCode{}
	for _, invite := range s.invites {
		cp := *invite
		out = append(out, &cp)
	}
	return out, nil
}

func (s *commercialTestStore) CreateCommercialInviteCode(invite *types.CommercialInviteCode) (int64, error) {
	cp := *invite
	cp.Code = strings.ToUpper(cp.Code)
	if cp.MaxRedemptions <= 0 {
		cp.MaxRedemptions = 1
	}
	for _, plan := range s.plans {
		if plan.ID == cp.PlanID {
			cp.PlanSlug = plan.Slug
			cp.PlanName = plan.Name
			break
		}
	}
	for index, existing := range s.invites {
		if existing.Code == cp.Code {
			cp.ID = existing.ID
			cp.RedeemedCount = existing.RedeemedCount
			s.invites[index] = &cp
			return cp.ID, nil
		}
	}
	cp.ID = s.next()
	s.invites = append(s.invites, &cp)
	return cp.ID, nil
}

func (s *commercialTestStore) GrantCommercialQuota(grant *types.CommercialQuotaGrant) (*types.CommercialQuotaGrant, error) {
	cp := *grant
	cp.ID = s.next()
	if cp.Model == "" {
		cp.Model = "*"
	}
	if cp.GrantType == "" {
		cp.GrantType = "manual"
	}
	cp.CreatedAt = time.Now()
	s.grants[cp.UID] = append(s.grants[cp.UID], &cp)
	s.ledger[cp.UID] = append(s.ledger[cp.UID], &types.CommercialLedgerEntry{
		ID:         s.next(),
		UID:        cp.UID,
		Model:      cp.Model,
		AmountCNY:  cp.AmountCNY,
		EntryType:  "grant",
		SourceType: cp.GrantType,
		SourceID:   cp.ID,
		Note:       cp.Note,
		CreatedAt:  cp.CreatedAt,
	})
	return &cp, nil
}

func (s *commercialTestStore) RedeemCommercialInvite(uid int64, code string) (*types.CommercialSummary, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, invite := range s.invites {
		if invite.Code != code {
			continue
		}
		if invite.State != 0 {
			return nil, fmt.Errorf("invite code is disabled")
		}
		if invite.ExpiresAt != nil && time.Now().After(*invite.ExpiresAt) {
			return nil, fmt.Errorf("invite code has expired")
		}
		if invite.RedeemedCount >= invite.MaxRedemptions {
			return nil, fmt.Errorf("invite code has no remaining redemptions")
		}
		if _, ok := s.redeemedInvites[uid][code]; ok {
			return nil, fmt.Errorf("invite code already redeemed")
		}
		for _, plan := range s.plans {
			if plan.ID != invite.PlanID {
				continue
			}
			if s.redeemedInvites[uid] == nil {
				s.redeemedInvites[uid] = map[string]struct{}{}
			}
			s.redeemedInvites[uid][code] = struct{}{}
			invite.RedeemedCount++
			startsAt := time.Now().UTC()
			expiresAt := startsAt.AddDate(0, 0, plan.DurationDays)
			s.entitlements[uid] = append(s.entitlements[uid], &types.CommercialEntitlement{
				ID: s.next(), UID: uid, PlanID: plan.ID, PlanSlug: plan.Slug, PlanName: plan.Name,
				Source: "invite", SourceRef: code, State: "active", StartsAt: startsAt, ExpiresAt: &expiresAt,
			})
			for model, amount := range plan.ModelBudgets {
				if _, err := s.GrantCommercialQuota(&types.CommercialQuotaGrant{
					UID:          uid,
					PlanID:       plan.ID,
					InviteCodeID: invite.ID,
					GrantType:    "invite",
					Model:        model,
					AmountCNY:    amount,
					EffectiveAt:  startsAt,
					ExpiresAt:    &expiresAt,
					Note:         "invite " + code,
				}); err != nil {
					return nil, err
				}
			}
			if plan.MonthlyBudget > 0 {
				if _, err := s.GrantCommercialQuota(&types.CommercialQuotaGrant{
					UID:          uid,
					PlanID:       plan.ID,
					InviteCodeID: invite.ID,
					GrantType:    "invite",
					Model:        "*",
					AmountCNY:    plan.MonthlyBudget,
					EffectiveAt:  startsAt,
					ExpiresAt:    &expiresAt,
					Note:         "invite " + code,
				}); err != nil {
					return nil, err
				}
			}
			return s.GetCommercialSummary(uid)
		}
	}
	return nil, fmt.Errorf("invite code not found")
}

func (s *commercialTestStore) GetCommercialSummary(uid int64) (*types.CommercialSummary, error) {
	plans, _ := s.ListCommercialPlans(false)
	summary := &types.CommercialSummary{
		UID:           uid,
		Plans:         plans,
		Ledger:        s.ledger[uid],
		TotalsByModel: map[string]float64{},
	}
	now := time.Now()
	for _, entitlement := range s.entitlements[uid] {
		if entitlement.State == "active" && !entitlement.StartsAt.After(now) && (entitlement.ExpiresAt == nil || entitlement.ExpiresAt.After(now)) {
			summary.Entitlements = append(summary.Entitlements, entitlement)
		}
	}
	for _, grant := range s.grants[uid] {
		if grant.EffectiveAt.After(now) || (grant.ExpiresAt != nil && !grant.ExpiresAt.After(now)) {
			continue
		}
		summary.Grants = append(summary.Grants, grant)
		summary.TotalsByModel[grant.Model] += grant.AmountCNY
		summary.TotalCNY += grant.AmountCNY
	}
	return summary, nil
}

func TestAccountAdminCommercialPlanInviteAndGrant(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)

	planReq := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/plans", strings.NewReader(`{
		"slug":"teacher-trial",
		"name":"教师试用包",
		"internal_quota_tokens":5000000,
		"model_budgets":{"MiniMax-M3":500},
		"duration_days":30
	}`))
	planReq.RemoteAddr = "127.0.0.1:40200"
	planReq.Header.Set("Content-Type", "application/json")
	planRec := httptest.NewRecorder()
	handler.HandleCommercialPlans(planRec, planReq)
	if planRec.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planRec.Code, planRec.Body.String())
	}
	if len(store.plans) != 1 || store.plans[0].InternalQuotaTokens != 5_000_000 {
		t.Fatalf("internal plan capacity was not saved: %#v", store.plans)
	}
	packageStarts := time.Now().UTC().Add(-time.Hour)
	packageExpires := packageStarts.AddDate(0, 0, 30)
	store.entitlements[38] = append(store.entitlements[38], &types.CommercialEntitlement{
		ID: 99, UID: 38, PlanID: store.plans[0].ID, PlanSlug: store.plans[0].Slug, PlanName: store.plans[0].Name,
		Source: "invite", SourceRef: "MANUAL-SETUP", State: "active", StartsAt: packageStarts, ExpiresAt: &packageExpires,
	})

	inviteReq := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(`{"code":"SCHOOL2026","plan_id":1,"max_redemptions":3}`))
	inviteReq.RemoteAddr = "127.0.0.1:40200"
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	handler.HandleCommercialInvites(inviteRec, inviteReq)
	if inviteRec.Code != http.StatusOK {
		t.Fatalf("invite status=%d body=%s", inviteRec.Code, inviteRec.Body.String())
	}

	grantReq := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/grants", strings.NewReader(`{"uid":38,"model":"minimax-m3","amount_cny":100}`))
	grantReq.RemoteAddr = "127.0.0.1:40200"
	grantReq.Header.Set("Content-Type", "application/json")
	grantRec := httptest.NewRecorder()
	handler.HandleCommercialGrant(grantRec, grantReq)
	if grantRec.Code != http.StatusOK {
		t.Fatalf("grant status=%d body=%s", grantRec.Code, grantRec.Body.String())
	}

	summaryReq := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/users?uid=38", nil)
	summaryReq.RemoteAddr = "127.0.0.1:40200"
	summaryRec := httptest.NewRecorder()
	handler.HandleCommercialUserSummary(summaryRec, summaryReq)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summaryRec.Code, summaryRec.Body.String())
	}
	var body struct {
		Summary types.CommercialSummary `json:"summary"`
	}
	if err := json.Unmarshal(summaryRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if body.Summary.TotalsByModel["MiniMax-M3"] != 100 {
		t.Fatalf("unexpected summary totals: %+v", body.Summary.TotalsByModel)
	}
	if len(body.Summary.Grants) != 1 || body.Summary.Grants[0].GrantType != "bonus" || body.Summary.Grants[0].ExpiresAt == nil || !body.Summary.Grants[0].ExpiresAt.Equal(packageExpires) {
		t.Fatalf("bonus grant did not inherit package expiry: %+v", body.Summary.Grants)
	}
}

func TestResolveCommercialBonusGrantRequiresActivePackageModelAndBoundedExpiry(t *testing.T) {
	now := time.Date(2026, 8, 12, 6, 0, 0, 0, time.UTC)
	firstExpiry := now.Add(10 * 24 * time.Hour)
	latestExpiry := now.Add(30 * 24 * time.Hour)
	unrelatedExpiry := now.Add(90 * 24 * time.Hour)
	summary := &types.CommercialSummary{
		Plans: []*types.CommercialPlan{
			{ID: 1, ModelBudgets: map[string]float64{"gpt-5.6-terra": 5250, "gpt-5.6-sol": 5250}},
			{ID: 2, ModelBudgets: map[string]float64{"MiniMax-M3": 500}},
		},
		Entitlements: []*types.CommercialEntitlement{
			{PlanID: 1, State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &firstExpiry},
			{PlanID: 1, State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &latestExpiry},
			{PlanID: 2, State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &unrelatedExpiry},
		},
	}

	model, expiresAt, err := resolveCommercialBonusGrant(summary, "GPT-5.6-TERRA", "", now)
	if err != nil || model != "gpt-5.6-terra" || !expiresAt.Equal(latestExpiry) {
		t.Fatalf("default bonus resolution model=%q expires=%v err=%v", model, expiresAt, err)
	}
	model, expiresAt, err = resolveCommercialBonusGrant(summary, "MiniMax-M3", "", now)
	if err != nil || model != "MiniMax-M3" || !expiresAt.Equal(unrelatedExpiry) {
		t.Fatalf("model-scoped bonus resolution model=%q expires=%v err=%v", model, expiresAt, err)
	}
	customExpiry := now.Add(5 * 24 * time.Hour)
	_, expiresAt, err = resolveCommercialBonusGrant(summary, "gpt-5.6-sol", customExpiry.Format(time.RFC3339), now)
	if err != nil || !expiresAt.Equal(customExpiry) {
		t.Fatalf("custom bonus expiry=%v err=%v", expiresAt, err)
	}
	if _, _, err := resolveCommercialBonusGrant(summary, "deepseek-v4-flash", "", now); err == nil || !strings.Contains(err.Error(), "not included") {
		t.Fatalf("inactive package model should be rejected: %v", err)
	}
	if _, _, err := resolveCommercialBonusGrant(summary, "gpt-5.6-terra", now.Add(31*24*time.Hour).Format(time.RFC3339), now); err == nil || !strings.Contains(err.Error(), "cannot exceed") {
		t.Fatalf("expiry beyond package should be rejected: %v", err)
	}
	if _, _, err := resolveCommercialBonusGrant(&types.CommercialSummary{}, "gpt-5.6-terra", "", now); err == nil || !strings.Contains(err.Error(), "active package") {
		t.Fatalf("missing package should be rejected: %v", err)
	}
}

func TestValidateCommercialRelayRequiredModelsIncludesInviteAndBonus(t *testing.T) {
	summary := &types.CommercialSummary{
		Grants: []*types.CommercialQuotaGrant{
			{GrantType: "invite", Model: "gpt-5.6-terra", AmountCNY: 100},
			{GrantType: "bonus", Model: "gpt-5.6-sol", AmountCNY: 20},
			{GrantType: "manual", Model: "legacy-model", AmountCNY: 10},
		},
	}
	relayUser := &commercialRelayUsageUser{
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{Model: "gpt-5.6-terra", Provider: "gpt56", AllowedModels: []string{"gpt-5.6-terra", "gpt-5.6-sol"}},
		}},
	}

	err := validateCommercialRelayRequiredModels(summary, relayUser, nil)
	if err == nil || !strings.Contains(err.Error(), "gpt-5.6-sol") {
		t.Fatalf("missing bonus model mapping should be reported: %v", err)
	}
	relayUser.Limits.ModelLimits = append(relayUser.Limits.ModelLimits,
		commercialRelayModelLimit{Model: "gpt-5.6-sol", Provider: "gpt56", AllowedModels: []string{"gpt-5.6-terra", "gpt-5.6-sol"}},
	)
	if err := validateCommercialRelayRequiredModels(summary, relayUser, nil); err != nil {
		t.Fatalf("mapped invite and bonus models should validate: %v", err)
	}
}

func TestRelayCommercialRedeemInviteUsesRequestUID(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "m3-trial",
		Name:         "M3 试用",
		ModelBudgets: map[string]float64{"MiniMax-M3": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "M3TEST", PlanID: planID, MaxRedemptions: 1}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	handler := NewRelayCommercialHandler(store)
	req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"m3test"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(116)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleRedeemInvite(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK      bool                         `json:"ok"`
		Summary relayCommercialPublicSummary `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK || len(body.Summary.Models) != 1 || body.Summary.Models[0] != "MiniMax-M3" {
		t.Fatalf("unexpected redemption summary: %+v", body)
	}
	if len(body.Summary.Entitlements) != 1 || body.Summary.Entitlements[0].PlanName != "M3 试用" || body.Summary.Entitlements[0].Source != "invite" {
		t.Fatalf("invite did not return the bound package entitlement: %+v", body.Summary.Entitlements)
	}
	if _, err := store.GetCommercialSummary(116); err != nil {
		t.Fatalf("request uid was not redeemed: %v", err)
	}
	for _, sensitive := range []string{"uid", "plans", "grants", "ledger", "totals_by_model", "total_cny", "amount_cny", "budget_cny"} {
		if strings.Contains(rec.Body.String(), `"`+sensitive+`"`) {
			t.Fatalf("commercial response leaked %s: %s", sensitive, rec.Body.String())
		}
	}
}

func TestRelayCommercialRejectsDuplicateInviteRedemption(t *testing.T) {
	store := newCommercialTestStore()
	planID, err := store.CreateCommercialPlan(&types.CommercialPlan{
		Slug:         "m3-trial",
		Name:         "M3 试用",
		ModelBudgets: map[string]float64{"MiniMax-M3": 500},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := store.CreateCommercialInviteCode(&types.CommercialInviteCode{Code: "M3TEST", PlanID: planID, MaxRedemptions: 2}); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	handler := NewRelayCommercialHandler(store)
	firstReq := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"M3TEST"}`))
	firstReq = firstReq.WithContext(context.WithValue(firstReq.Context(), uidKey, int64(116)))
	firstReq.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	handler.HandleRedeemInvite(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"M3TEST"}`))
	secondReq = secondReq.WithContext(context.WithValue(secondReq.Context(), uidKey, int64(116)))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	handler.HandleRedeemInvite(secondRec, secondReq)
	if secondRec.Code != http.StatusBadRequest || !strings.Contains(secondRec.Body.String(), `"error":"invite code could not be redeemed"`) {
		t.Fatalf("second status=%d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if strings.Contains(secondRec.Body.String(), "already redeemed") {
		t.Fatalf("duplicate redemption exposed internal error: %s", secondRec.Body.String())
	}

	otherReq := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"M3TEST"}`))
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), uidKey, int64(117)))
	otherReq.Header.Set("Content-Type", "application/json")
	otherRec := httptest.NewRecorder()
	handler.HandleRedeemInvite(otherRec, otherReq)
	if otherRec.Code != http.StatusOK {
		t.Fatalf("other user status=%d body=%s", otherRec.Code, otherRec.Body.String())
	}
}

func TestRelayCommercialCanBeDisabledForPublicUsers(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewRelayCommercialHandler(store, false)

	req := httptest.NewRequest(http.MethodPost, "/api/relay/invite/redeem", strings.NewReader(`{"code":"M3TEST"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(116)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleRedeemInvite(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	summaryReq := httptest.NewRequest(http.MethodGet, "/api/relay/commercial", nil)
	summaryReq = summaryReq.WithContext(context.WithValue(summaryReq.Context(), uidKey, int64(116)))
	summaryRec := httptest.NewRecorder()
	handler.HandleSummary(summaryRec, summaryReq)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summaryRec.Code, summaryRec.Body.String())
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(summaryRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if body.Enabled {
		t.Fatalf("expected disabled commercial payload: %s", summaryRec.Body.String())
	}
}

func TestRelayCommercialAllowlistEnablesSingleUser(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewRelayCommercialHandlerWithOptions(store, RelayCommercialOptions{
		PublicEnabled: false,
		TestUIDs:      map[int64]bool{38: true},
	})

	allowedReq := httptest.NewRequest(http.MethodGet, "/api/relay/commercial", nil)
	allowedReq = allowedReq.WithContext(context.WithValue(allowedReq.Context(), uidKey, int64(38)))
	allowedRec := httptest.NewRecorder()
	handler.HandleSummary(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK {
		t.Fatalf("allowed status=%d body=%s", allowedRec.Code, allowedRec.Body.String())
	}
	var allowedBody struct {
		Enabled bool   `json:"enabled"`
		Rollout string `json:"rollout"`
	}
	if err := json.Unmarshal(allowedRec.Body.Bytes(), &allowedBody); err != nil {
		t.Fatalf("decode allowed summary: %v", err)
	}
	if !allowedBody.Enabled || allowedBody.Rollout != "allowlist" {
		t.Fatalf("expected allowlist enabled payload: %+v", allowedBody)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/api/relay/commercial", nil)
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), uidKey, int64(39)))
	otherRec := httptest.NewRecorder()
	handler.HandleSummary(otherRec, otherReq)
	var otherBody struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherBody); err != nil {
		t.Fatalf("decode other summary: %v", err)
	}
	if otherBody.Enabled {
		t.Fatalf("expected non-allowlisted user disabled: %s", otherRec.Body.String())
	}
}

func TestRelayCommercialEnforceAllowlistAppliesPerUser(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewRelayCommercialHandlerWithOptions(store, RelayCommercialOptions{
		PublicEnabled: true,
		EnforceUIDs:   map[int64]bool{38: true},
	})

	allowedReq := httptest.NewRequest(http.MethodGet, "/api/relay/commercial", nil)
	allowedReq = allowedReq.WithContext(context.WithValue(allowedReq.Context(), uidKey, int64(38)))
	allowedRec := httptest.NewRecorder()
	handler.HandleSummary(allowedRec, allowedReq)
	var allowedBody struct {
		EnforceEnabled bool   `json:"enforce_enabled"`
		Note           string `json:"note"`
	}
	if err := json.Unmarshal(allowedRec.Body.Bytes(), &allowedBody); err != nil {
		t.Fatalf("decode allowed summary: %v", err)
	}
	if !allowedBody.EnforceEnabled {
		t.Fatalf("expected uid 38 to be enforce-enabled: %s", allowedRec.Body.String())
	}
	if strings.Contains(allowedBody.Note, "内测") {
		t.Fatalf("public enforced summary must not use gray-release note: %q", allowedBody.Note)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/api/relay/commercial", nil)
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), uidKey, int64(39)))
	otherRec := httptest.NewRecorder()
	handler.HandleSummary(otherRec, otherReq)
	var otherBody struct {
		EnforceEnabled bool `json:"enforce_enabled"`
	}
	if err := json.Unmarshal(otherRec.Body.Bytes(), &otherBody); err != nil {
		t.Fatalf("decode other summary: %v", err)
	}
	if otherBody.EnforceEnabled {
		t.Fatalf("expected uid 39 to stay dry-run only: %s", otherRec.Body.String())
	}
}

func TestAccountAdminCommercialRelayEnforceAllowlist(t *testing.T) {
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, newCommercialTestStore())
	handler.SetCommercialRelayAdmin(nil, false, map[int64]bool{38: true})

	if !handler.commercialRelayEnforcedFor(38) {
		t.Fatalf("expected uid 38 to be enforce-enabled")
	}
	if handler.commercialRelayEnforcedFor(39) {
		t.Fatalf("expected uid 39 to stay dry-run only")
	}

	handler.SetCommercialRelayAdmin(nil, true)
	if !handler.commercialRelayEnforcedFor(39) {
		t.Fatalf("expected global enforce to enable all users")
	}
}

func TestCommercialRelayDryRunBuildsBudgetDiff(t *testing.T) {
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"MiniMax-M3":        500,
			"deepseek-v4-flash": 100,
		},
		TotalCNY: 600,
	}
	relayUser := &commercialRelayUsageUser{
		UID:        38,
		Username:   "ck",
		Configured: true,
		Key:        &commercialRelayKeySummary{Prefix: "sk-bf-aa...1234", State: "active"},
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider:      "minimax-m3-anthropic",
				Model:         "MiniMax-M3",
				AllowedModels: []string{"MiniMax-M3"},
				Budget:        commercialRelayBudget{MaxLimit: 500, CurrentUsage: 12.5, ResetDuration: "1M"},
			},
			{
				Provider:      "deepseek-anthropic",
				Model:         "deepseek-v4-flash",
				AllowedModels: []string{"deepseek-v4-flash"},
				Budget:        commercialRelayBudget{MaxLimit: 50, CurrentUsage: 2, ResetDuration: "1M"},
			},
			{
				Provider:      "deepseek-openai",
				Model:         "deepseek-v4-flash",
				AllowedModels: []string{"deepseek-v4-flash"},
				Budget:        commercialRelayBudget{MaxLimit: 75, CurrentUsage: 3, ResetDuration: "1M"},
			},
			{
				Provider:      "glm-anthropic",
				Model:         "glm-5.3-flash",
				AllowedModels: []string{"glm-5.3-flash"},
				Budget:        commercialRelayBudget{MaxLimit: 500, CurrentUsage: 0, ResetDuration: "1M"},
			},
		}},
	}

	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)

	if !dryRun.RelayKeyConfigured || dryRun.RelayUsername != "ck" {
		t.Fatalf("unexpected relay metadata: %+v", dryRun)
	}
	if row := findCommercialRelayComparison(t, dryRun, "MiniMax-M3"); row.Status != "match" || row.RelayUsage != 12.5 {
		t.Fatalf("unexpected m3 row: %+v", row)
	}
	if row := findCommercialRelayComparison(t, dryRun, "deepseek-v4-flash"); row.Status != "mismatch" || row.Delta != 25 {
		t.Fatalf("unexpected deepseek row: %+v", row)
	}
	if row := findCommercialRelayComparison(t, dryRun, "glm-5.3-flash"); row.Status != "relay_only" || row.RelayLimit != 500 {
		t.Fatalf("unexpected glm row: %+v", row)
	}
	if len(dryRun.ProposedUpdates) != 2 {
		t.Fatalf("expected two proposed updates, got %+v", dryRun.ProposedUpdates)
	}
	for _, provider := range []string{"deepseek-anthropic", "deepseek-openai"} {
		update := findCommercialRelayUpdate(t, dryRun, provider)
		if update.MaxLimit != 100 {
			t.Fatalf("unexpected proposed update for %s: %+v", provider, update)
		}
	}
}

func TestCommercialRelayDryRunAggregatesSharedGPT56BudgetOnce(t *testing.T) {
	models := []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"gpt-5.6-terra": 87.5,
			"gpt-5.6-sol":   87.5,
			"gpt-5.6-luna":  87.5,
		},
		TotalCNY: 262.5,
	}
	limits := make([]commercialRelayModelLimit, 0, len(models))
	for _, model := range models {
		limits = append(limits, commercialRelayModelLimit{
			Provider:      "newcli-codex-openai",
			Model:         model,
			AllowedModels: models,
			SharedBudget:  true,
			Budget:        commercialRelayBudget{MaxLimit: 100, CurrentUsage: 12.5, ResetDuration: "1M"},
		})
	}
	relayUser := &commercialRelayUsageUser{
		UID:        38,
		Username:   "ck",
		Configured: true,
		Limits:     commercialRelayLimits{ModelLimits: limits},
	}

	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)

	if len(dryRun.ProposedUpdates) != 1 {
		t.Fatalf("expected one shared provider budget update, got %+v", dryRun.ProposedUpdates)
	}
	update := dryRun.ProposedUpdates[0]
	if update.Provider != "newcli-codex-openai" || update.MaxLimit != 262.5 {
		t.Fatalf("expected one 262.5 CNY shared budget, got %+v", update)
	}
	for _, model := range models {
		row := findCommercialRelayComparison(t, dryRun, model)
		if row.CommercialLimit != 262.5 {
			t.Fatalf("expected %s to reference the shared 262.5 CNY limit, got %+v", model, row)
		}
	}
}

func TestCommercialRelayDryRunFlagsAliasDriftWhenBestAliasMatches(t *testing.T) {
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"deepseek-v4-flash": 100.05,
		},
		TotalCNY: 100.05,
	}
	relayUser := &commercialRelayUsageUser{
		UID:        38,
		Username:   "ck",
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider:      "deepseek-anthropic",
				Model:         "deepseek-v4-flash",
				AllowedModels: []string{"deepseek-v4-flash"},
				Budget:        commercialRelayBudget{MaxLimit: 100.05, CurrentUsage: 0, ResetDuration: "1M"},
			},
			{
				Provider:      "deepseek-openai",
				Model:         "deepseek-v4-flash",
				AllowedModels: []string{"deepseek-v4-flash"},
				Budget:        commercialRelayBudget{MaxLimit: 100, CurrentUsage: 0, ResetDuration: "1M"},
			},
		}},
	}

	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)

	row := findCommercialRelayComparison(t, dryRun, "deepseek-v4-flash")
	if row.Status != "mismatch" || row.Delta <= 0 {
		t.Fatalf("expected alias drift to show mismatch, got %+v", row)
	}
	if len(dryRun.ProposedUpdates) != 1 {
		t.Fatalf("expected one alias sync update, got %+v", dryRun.ProposedUpdates)
	}
	update := findCommercialRelayUpdate(t, dryRun, "deepseek-openai")
	if update.MaxLimit != 100.05 {
		t.Fatalf("expected deepseek-openai to sync to 100.05, got %+v", update)
	}
}

func TestCommercialRelayDryRunFlagsOverLimitWithoutNoopSync(t *testing.T) {
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"MiniMax-M3": 500,
		},
		TotalCNY: 500,
	}
	relayUser := &commercialRelayUsageUser{
		UID:        38,
		Username:   "ck",
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider:      "minimax-m3-anthropic",
				Model:         "MiniMax-M3",
				AllowedModels: []string{"MiniMax-M3"},
				Budget:        commercialRelayBudget{MaxLimit: 500, CurrentUsage: 745.63, ResetDuration: "1M"},
			},
		}},
	}

	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)

	row := findCommercialRelayComparison(t, dryRun, "MiniMax-M3")
	if row.Status != "over_limit" || row.Remaining != 0 {
		t.Fatalf("expected over_limit row with zero remaining, got %+v", row)
	}
	if len(dryRun.ProposedUpdates) != 0 {
		t.Fatalf("over-limit with matching relay limit should not propose noop sync: %+v", dryRun.ProposedUpdates)
	}
}

func TestCommercialRelayDryRunCanLowerOverLimitRelayBudget(t *testing.T) {
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"MiniMax-M3": 500,
		},
		TotalCNY: 500,
	}
	relayUser := &commercialRelayUsageUser{
		UID:        38,
		Username:   "ck",
		Configured: true,
		Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
			{
				Provider:      "minimax-m3-anthropic",
				Model:         "MiniMax-M3",
				AllowedModels: []string{"MiniMax-M3"},
				Budget:        commercialRelayBudget{MaxLimit: 1000, CurrentUsage: 745.63, ResetDuration: "1M"},
			},
		}},
	}

	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)

	row := findCommercialRelayComparison(t, dryRun, "MiniMax-M3")
	if row.Status != "over_limit" || row.Remaining != 0 {
		t.Fatalf("expected over_limit row with zero remaining, got %+v", row)
	}
	if len(dryRun.ProposedUpdates) != 1 {
		t.Fatalf("expected one down-limit sync update, got %+v", dryRun.ProposedUpdates)
	}
	if dryRun.ProposedUpdates[0].MaxLimit != 500 {
		t.Fatalf("expected sync to lower relay max to 500, got %+v", dryRun.ProposedUpdates[0])
	}
}

func findCommercialRelayComparison(t *testing.T, dryRun *commercialRelayDryRun, model string) commercialRelayBudgetComparison {
	t.Helper()
	for _, row := range dryRun.Comparisons {
		if row.Model == model {
			return row
		}
	}
	t.Fatalf("comparison for %s not found: %+v", model, dryRun.Comparisons)
	return commercialRelayBudgetComparison{}
}

func findCommercialRelayUpdate(t *testing.T, dryRun *commercialRelayDryRun, provider string) commercialRelayProviderBudgetUpdate {
	t.Helper()
	for _, update := range dryRun.ProposedUpdates {
		if update.Provider == provider {
			return update
		}
	}
	t.Fatalf("proposed update for %s not found: %+v", provider, dryRun.ProposedUpdates)
	return commercialRelayProviderBudgetUpdate{}
}
