package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2m.local/contracts"
)

func generateBalanceCodes(t *testing.T, handler http.Handler, adminToken string, count int, amount string) contracts.GenerateRedeemCodesResponse {
	t.Helper()
	w := do(t, handler, http.MethodPost, "/api/v1/admin/redeem-codes", adminToken, map[string]any{
		"type": "balance", "count": count, "amount": amount, "currency": "CNY",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("generate codes: %d %s", w.Code, w.Body.String())
	}
	var response contracts.GenerateRedeemCodesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode generation response: %v", err)
	}
	return response
}

func TestRedeemCodeLifecycle(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "redeem-admin@example.com", contracts.UserRolePlatformAdmin)
	user := createLoginUser(t, authSvc, "redeem-user@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	userToken, _, _ := authSvc.Login(ctx, user.Email, "password123")

	generated := generateBalanceCodes(t, handler, adminToken, 3, "25.00")
	if len(generated.Codes) != 3 || len(generated.Items) != 3 || generated.BatchID == "" {
		t.Fatalf("unexpected generation response: %+v", generated)
	}
	for _, item := range generated.Items {
		if item.AmountMicros != 25_000_000 || item.Status != contracts.RedeemCodeUnused || item.CodePrefix == "" {
			t.Fatalf("unexpected generated item: %+v", item)
		}
	}

	// The list view must expose prefixes only, never plaintext or hashes.
	list := do(t, handler, http.MethodGet, "/api/v1/admin/redeem-codes?batch_id="+generated.BatchID, adminToken, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list codes: %d %s", list.Code, list.Body.String())
	}
	for _, plaintext := range generated.Codes {
		if strings.Contains(list.Body.String(), plaintext) {
			t.Fatalf("plaintext code leaked into the list response")
		}
		if strings.Contains(list.Body.String(), redeemCodeHash(plaintext)) {
			t.Fatalf("code hash leaked into the list response")
		}
	}

	// Redeem the first code: wallet credited, journal recorded.
	redeem := do(t, handler, http.MethodPost, "/api/v1/redeem", userToken, map[string]any{"code": generated.Codes[0]})
	if redeem.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", redeem.Code, redeem.Body.String())
	}
	wallet, err := st.GetWallet(ctx, user.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 25_000_000 {
		t.Fatalf("wallet must hold 25.00 after redeem, err=%v wallet=%+v", err, wallet)
	}
	journals, err := st.ListWalletJournals(ctx, user.ID, 10)
	if err != nil {
		t.Fatalf("list journals: %v", err)
	}
	hasRedeemJournal := false
	for _, journal := range journals {
		if journal.Kind == contracts.WalletJournalRedeem && journal.AmountMicros == 25_000_000 {
			hasRedeemJournal = true
		}
	}
	if !hasRedeemJournal {
		t.Fatalf("redeem journal missing: %+v", journals)
	}

	// A second redeem of the same code must fail without crediting again.
	again := do(t, handler, http.MethodPost, "/api/v1/redeem", userToken, map[string]any{"code": generated.Codes[0]})
	if again.Code != http.StatusBadRequest || !strings.Contains(again.Body.String(), "redeem_rejected") {
		t.Fatalf("double redeem must be rejected, got %d %s", again.Code, again.Body.String())
	}
	wallet, _ = st.GetWallet(ctx, user.ID, "CNY")
	if wallet.AvailableMicros != 25_000_000 {
		t.Fatalf("double redeem must not credit twice, wallet=%+v", wallet)
	}

	// Disable an unused code, then it can no longer be redeemed; delete it.
	var disabledID string
	refreshed := do(t, handler, http.MethodGet, "/api/v1/admin/redeem-codes?batch_id="+generated.BatchID+"&status=unused", adminToken, nil)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh list: %d %s", refreshed.Code, refreshed.Body.String())
	}
	listPage := contracts.RedeemCodePage{}
	if err := json.Unmarshal(refreshed.Body.Bytes(), &listPage); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, item := range listPage.Items {
		if item.Status == contracts.RedeemCodeUnused {
			disabledID = item.ID
			break
		}
	}
	if disabledID == "" {
		t.Fatalf("no unused code found to disable")
	}
	if w := do(t, handler, http.MethodPost, "/api/v1/admin/redeem-codes/"+disabledID+"/disable", adminToken, nil); w.Code != http.StatusOK {
		t.Fatalf("disable code: %d %s", w.Code, w.Body.String())
	}
	var disabledPlaintext string
	for _, plaintext := range generated.Codes {
		code, hashErr := st.GetRedeemCodeByHash(ctx, redeemCodeHash(plaintext))
		if hashErr == nil && code.ID == disabledID {
			disabledPlaintext = plaintext
			break
		}
	}
	if disabledPlaintext == "" {
		t.Fatalf("could not map disabled code back to plaintext")
	}
	if w := do(t, handler, http.MethodPost, "/api/v1/redeem", userToken, map[string]any{"code": disabledPlaintext}); w.Code != http.StatusBadRequest {
		t.Fatalf("disabled code must not redeem, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, handler, http.MethodDelete, "/api/v1/admin/redeem-codes/"+disabledID, adminToken, nil); w.Code != http.StatusOK {
		t.Fatalf("delete disabled code: %d %s", w.Code, w.Body.String())
	}

	// Non-admin cannot reach the admin surface.
	if w := do(t, handler, http.MethodGet, "/api/v1/admin/redeem-codes", userToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin list must 403, got %d %s", w.Code, w.Body.String())
	}
}

func TestRedeemRoutesFailClosedWithoutPaymentsFlag(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetBusinessFeatureFlags(BusinessFeatureFlags{})
	handler := srv.Routes()
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "redeem-gate-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/redeem"},
		{http.MethodGet, "/api/v1/admin/redeem-codes"},
	} {
		w := do(t, handler, tc.method, tc.path, adminToken, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "feature_disabled") {
			t.Fatalf("%s %s must fail closed, got %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestCreateAndRedeemIsIdempotent(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "car-admin@example.com", contracts.UserRolePlatformAdmin)
	user := createLoginUser(t, authSvc, "car-user@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	carRequest := func(idempotencyKey string, userID int64, amount string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(contracts.CreateAndRedeemRequest{UserID: userID, Amount: amount, Currency: "CNY"})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+adminToken)
		r.Header.Set("Idempotency-Key", idempotencyKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	first := carRequest("car-key-1", user.ID, "30.00")
	if first.Code != http.StatusCreated {
		t.Fatalf("create-and-redeem: %d %s", first.Code, first.Body.String())
	}
	wallet, err := st.GetWallet(ctx, user.ID, "CNY")
	if err != nil || wallet.AvailableMicros != 30_000_000 {
		t.Fatalf("wallet must hold 30.00, err=%v wallet=%+v", err, wallet)
	}

	replay := carRequest("car-key-1", user.ID, "30.00")
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replay":true`) {
		t.Fatalf("replay must return the original result, got %d %s", replay.Code, replay.Body.String())
	}
	wallet, _ = st.GetWallet(ctx, user.ID, "CNY")
	if wallet.AvailableMicros != 30_000_000 {
		t.Fatalf("replay must not credit twice, wallet=%+v", wallet)
	}

	if w := carRequest("car-key-1", user.ID, "50.00"); w.Code != http.StatusConflict {
		t.Fatalf("same key with a different payload must 409, got %d %s", w.Code, w.Body.String())
	}
	if w := carRequest("", user.ID, "30.00"); w.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key must 400, got %d %s", w.Code, w.Body.String())
	}
}
