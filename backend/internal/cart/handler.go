package cart

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kwabsntim/rok-store/internal/auth"
	"github.com/kwabsntim/rok-store/internal/models"
)

// Handler holds dependencies for cart HTTP handlers.
type Handler struct {
	repo *Repository
}

// NewHandler creates a cart Handler.
func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// GetCart handles GET /api/cart
func (h *Handler) GetCart(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	items, total, err := h.repo.GetByUserID(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to fetch cart", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []models.CartItem{}
	}

	respondJSON(w, http.StatusOK, models.CartResponse{Items: items, TotalPrice: total})
}

// AddToCart handles POST /api/cart
func (h *Handler) AddToCart(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req models.AddCartItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ProductID == "" || req.Quantity <= 0 {
		http.Error(w, "product_id and a positive quantity are required", http.StatusBadRequest)
		return
	}

	item, err := h.repo.AddOrIncrement(r.Context(), user.ID, req.ProductID, req.Quantity)
	if err != nil {
		http.Error(w, "failed to add item to cart", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, item)
}

// UpdateCartItem handles PUT /api/cart/{product_id}
func (h *Handler) UpdateCartItem(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	productID := r.PathValue("product_id")
	if productID == "" {
		http.Error(w, "product_id is required", http.StatusBadRequest)
		return
	}

	var req models.UpdateCartItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Quantity <= 0 {
		http.Error(w, "quantity must be greater than 0", http.StatusBadRequest)
		return
	}

	item, err := h.repo.UpdateQuantity(r.Context(), user.ID, productID, req.Quantity)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "cart item not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to update cart item", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, item)
}

// RemoveCartItem handles DELETE /api/cart/{product_id}
func (h *Handler) RemoveCartItem(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	productID := r.PathValue("product_id")
	if productID == "" {
		http.Error(w, "product_id is required", http.StatusBadRequest)
		return
	}

	err := h.repo.Remove(r.Context(), user.ID, productID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "cart item not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to remove cart item", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
