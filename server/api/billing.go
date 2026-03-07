package api

import (
	"encoding/json"
	"math"
	"net/http"

	"gitlab.okg.com/sre_ai/nexbox/store"
)

// balanceResponse extends UserBalance with a human-readable currency amount.
type balanceResponse struct {
	UserID         string  `json:"user_id"`
	BalanceTokens  int64   `json:"balance_tokens"`
	ReservedTokens int64   `json:"reserved_tokens"`
	TotalRecharged int64   `json:"total_recharged"`
	BalanceCNY     float64 `json:"balance_cny"` // balance_tokens / 1_000_000 * price_per_million
}

// tokensToMoney converts tokens to currency amount using the configured price.
func (s *Server) tokensToMoney(tokens int64) float64 {
	price := s.Config.Get().TokenPricePerMillion
	if price <= 0 {
		return 0
	}
	result := float64(tokens) / 1_000_000 * price
	// Round to 2 decimal places
	return math.Round(result*100) / 100
}

// HandleGetBalance GET /api/balance
// Returns the current user's token balance and recent transactions.
func (s *Server) HandleGetBalance(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r)

	bal, err := s.Store.GetBalance(userID)
	if err != nil {
		http.Error(w, "failed to get balance: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if bal == nil {
		// No balance row yet — return zeroes
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(balanceResponse{
			UserID: userID,
		})
		return
	}

	resp := balanceResponse{
		UserID:         bal.UserID,
		BalanceTokens:  bal.BalanceTokens,
		ReservedTokens: bal.ReservedTokens,
		TotalRecharged: bal.TotalRecharged,
		BalanceCNY:     s.tokensToMoney(bal.BalanceTokens),
	}

	// Also fetch recent transactions (last 20)
	txs, err := s.Store.ListTransactions(userID, 20, 0)
	if err != nil {
		// Non-fatal: return balance without transactions
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}
	if txs == nil {
		txs = []store.TokenTransaction{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"balance":      resp,
		"transactions": txs,
	})
}

// HandleAdminRecharge POST /api/admin/recharge
// Admin-only: manually add tokens to a user's balance.
func (s *Server) HandleAdminRecharge(w http.ResponseWriter, r *http.Request) {
	adminID := GetUserID(r)

	var req struct {
		UserID string `json:"user_id"`
		Tokens int64  `json:"tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	if req.Tokens <= 0 {
		http.Error(w, "tokens must be positive", http.StatusBadRequest)
		return
	}

	if err := s.Store.RechargeBalance(req.UserID, req.Tokens, adminID); err != nil {
		http.Error(w, "failed to recharge: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return updated balance
	bal, err := s.Store.GetBalance(req.UserID)
	if err != nil || bal == nil {
		// Recharge succeeded but we cannot fetch the fresh balance; return a
		// partial success response instead of the opaque 204 No Content.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":      true,
			"message": "recharged but balance fetch failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(balanceResponse{
		UserID:         bal.UserID,
		BalanceTokens:  bal.BalanceTokens,
		ReservedTokens: bal.ReservedTokens,
		TotalRecharged: bal.TotalRecharged,
		BalanceCNY:     s.tokensToMoney(bal.BalanceTokens),
	})
}
