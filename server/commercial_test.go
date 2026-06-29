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

type commercialTestStore struct {
	nextID          int64
	plans           []*types.CommercialPlan
	invites         []*types.CommercialInviteCode
	grants          map[int64][]*types.CommercialQuotaGrant
	ledger          map[int64][]*types.CommercialLedgerEntry
	redeemedInvites map[int64]map[string]struct{}
}

func newCommercialTestStore() *commercialTestStore {
	return &commercialTestStore{
		nextID:          1,
		grants:          map[int64][]*types.CommercialQuotaGrant{},
		ledger:          map[int64][]*types.CommercialLedgerEntry{},
		redeemedInvites: map[int64]map[string]struct{}{},
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
			for model, amount := range plan.ModelBudgets {
				if _, err := s.GrantCommercialQuota(&types.CommercialQuotaGrant{
					UID:          uid,
					PlanID:       plan.ID,
					InviteCodeID: invite.ID,
					GrantType:    "invite",
					Model:        model,
					AmountCNY:    amount,
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
		Grants:        s.grants[uid],
		Ledger:        s.ledger[uid],
		TotalsByModel: map[string]float64{},
	}
	for _, grant := range summary.Grants {
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

	inviteReq := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(`{"code":"SCHOOL2026","plan_id":1,"max_redemptions":3}`))
	inviteReq.RemoteAddr = "127.0.0.1:40200"
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	handler.HandleCommercialInvites(inviteRec, inviteReq)
	if inviteRec.Code != http.StatusOK {
		t.Fatalf("invite status=%d body=%s", inviteRec.Code, inviteRec.Body.String())
	}

	grantReq := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/grants", strings.NewReader(`{"uid":38,"model":"deepseek-v4-flash","amount_cny":100}`))
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
	if body.Summary.TotalsByModel["deepseek-v4-flash"] != 100 {
		t.Fatalf("unexpected summary totals: %+v", body.Summary.TotalsByModel)
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
		OK      bool                    `json:"ok"`
		Summary types.CommercialSummary `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK || body.Summary.UID != 116 || body.Summary.TotalsByModel["MiniMax-M3"] != 500 {
		t.Fatalf("unexpected redemption summary: %+v", body)
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
	if secondRec.Code != http.StatusBadRequest {
		t.Fatalf("second status=%d body=%s", secondRec.Code, secondRec.Body.String())
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
