package product

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

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

	// Fetch product first to get image URLs for Cloudinary deletion
	p, err := h.repo.GetByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to fetch product", http.StatusInternalServerError)
		return
	}

	// Delete from database
	err = h.repo.Delete(r.Context(), id)
	if err != nil {
		http.Error(w, "failed to delete product", http.StatusInternalServerError)
		return
	}

	// Delete from Cloudinary (async, log errors but don't fail the request)
	go deleteCloudinaryImages(p)

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

// deleteCloudinaryImages deletes all Cloudinary images associated with a product.
// It extracts the public_id from each Cloudinary URL and calls the destroy API.
// Runs in a goroutine — errors are logged but do not affect the HTTP response.
func deleteCloudinaryImages(p *models.Product) {
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey    := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")

	if cloudName == "" || apiKey == "" || apiSecret == "" {
		log.Println("[cloudinary] skipping image deletion: CLOUDINARY_CLOUD_NAME, CLOUDINARY_API_KEY or CLOUDINARY_API_SECRET not set")
		return
	}

	// Collect all image URLs
	var imageURLs []string
	if p.ImageURL != "" {
		imageURLs = append(imageURLs, p.ImageURL)
	}
	if p.Images != "" {
		var extras []string
		if err := json.Unmarshal([]byte(p.Images), &extras); err == nil {
			imageURLs = append(imageURLs, extras...)
		}
	}

	for _, imgURL := range imageURLs {
		publicID := extractCloudinaryPublicID(imgURL)
		if publicID == "" {
			log.Printf("[cloudinary] could not extract public_id from URL: %s", imgURL)
			continue
		}
		if err := destroyCloudinaryAsset(cloudName, apiKey, apiSecret, publicID); err != nil {
			log.Printf("[cloudinary] failed to delete %s: %v", publicID, err)
		} else {
			log.Printf("[cloudinary] deleted %s", publicID)
		}
	}
}

// extractCloudinaryPublicID parses a Cloudinary delivery URL and returns the public_id.
// Example URL: https://res.cloudinary.com/demo/image/upload/v1234567890/folder/image.jpg
// Returns: folder/image  (no extension, no version segment)
func extractCloudinaryPublicID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.Contains(u.Host, "cloudinary.com") {
		return ""
	}

	// Path looks like: /<cloud>/image/upload/[v<version>/]<public_id>.<ext>
	// Split on "/upload/" to isolate the public_id portion
	parts := strings.SplitN(u.Path, "/upload/", 2)
	if len(parts) != 2 {
		return ""
	}

	tail := parts[1] // e.g. "v1234567890/folder/image.jpg" or "folder/image.jpg"

	// Strip leading version segment (v followed by digits)
	segments := strings.SplitN(tail, "/", 2)
	if len(segments) == 2 && len(segments[0]) > 1 && segments[0][0] == 'v' {
		allDigits := true
		for _, c := range segments[0][1:] {
			if c < '0' || c > '9' { allDigits = false; break }
		}
		if allDigits {
			tail = segments[1]
		}
	}

	// Strip file extension
	tail = strings.TrimSuffix(tail, path.Ext(tail))
	return tail
}

// destroyCloudinaryAsset calls the Cloudinary Admin API to delete an asset by public_id.
func destroyCloudinaryAsset(cloudName, apiKey, apiSecret, publicID string) error {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())

	// Signature: SHA1 of "public_id=<id>&timestamp=<ts><api_secret>"
	sigPayload := fmt.Sprintf("public_id=%s&timestamp=%s%s", publicID, timestamp, apiSecret)
	h := sha1.New()
	h.Write([]byte(sigPayload))
	signature := fmt.Sprintf("%x", h.Sum(nil))

	apiURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/destroy", cloudName)

	form := url.Values{}
	form.Set("public_id", publicID)
	form.Set("timestamp", timestamp)
	form.Set("api_key",   apiKey)
	form.Set("signature", signature)

	resp, err := http.PostForm(apiURL, form)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	// Cloudinary returns 200 even on failure — must check the body
	var result struct {
		Result string `json:"result"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if result.Error.Message != "" {
		return fmt.Errorf("cloudinary error: %s", result.Error.Message)
	}
	if result.Result != "ok" {
		return fmt.Errorf("unexpected result: %s", result.Result)
	}
	return nil
}
