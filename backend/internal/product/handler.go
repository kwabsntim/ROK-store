package product

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/kwabsntim/rok-store/internal/models"
)

// Handler holds dependencies for product HTTP handlers.
type Handler struct {
	repo *Repository
}

// NewHandler creates a product Handler.
func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// ---- Public handlers ----

// ListProducts handles GET /api/products
// Query params: category (string), page (int), page_size (int)
func (h *Handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))

	products, total, err := h.repo.ListAll(r.Context(), category, page, pageSize)
	if err != nil {
		http.Error(w, "failed to fetch products", http.StatusInternalServerError)
		return
	}
	if products == nil {
		products = []models.Product{}
	}

	respondJSON(w, http.StatusOK, models.PaginatedProductsResponse{
		Products: products,
		Total:    total,
		Page:     max(page, 1),
		PageSize: pageSize,
	})
}

// ListNewest handles GET /api/products/newest
func (h *Handler) ListNewest(w http.ResponseWriter, r *http.Request) {
	products, err := h.repo.ListNewest(r.Context())
	if err != nil {
		http.Error(w, "failed to fetch newest products", http.StatusInternalServerError)
		return
	}
	if products == nil {
		products = []models.Product{}
	}
	respondJSON(w, http.StatusOK, products)
}

// GetProduct handles GET /api/products/{id}
func (h *Handler) GetProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "product id is required", http.StatusBadRequest)
		return
	}

	p, err := h.repo.GetByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to fetch product", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, p)
}

// ---- Admin handlers (require admin JWT, wired in main.go) ----

// CreateProduct handles POST /api/admin/products
func (h *Handler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	var req models.CreateProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Category == "" || req.Price <= 0 {
		http.Error(w, "name, category and a positive price are required", http.StatusBadRequest)
		return
	}

	p, err := h.repo.Create(r.Context(), req)
	if err != nil {
		http.Error(w, "failed to create product", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, p)
}

// UpdateProduct handles PUT /api/admin/products/{id}
func (h *Handler) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "product id is required", http.StatusBadRequest)
		return
	}

	var req models.UpdateProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Category == "" || req.Price <= 0 {
		http.Error(w, "name, category and a positive price are required", http.StatusBadRequest)
		return
	}

	p, err := h.repo.Update(r.Context(), id, req)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to update product", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, p)
}

// DeleteProduct handles DELETE /api/admin/products/{id}
func (h *Handler) DeleteProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "product id is required", http.StatusBadRequest)
		return
	}

	err := h.repo.Delete(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to delete product", http.StatusInternalServerError)
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
