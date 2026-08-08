package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/kwabsntim/rok-store/internal/auth"
	"github.com/kwabsntim/rok-store/internal/cart"
	"github.com/kwabsntim/rok-store/internal/models"
)

// Handler handles checkout initialization and Paystack webhook processing.
type Handler struct {
	db             *sql.DB
	paystackClient *Client
	cartRepo       *cart.Repository
}

// NewHandler creates a payment Handler.
func NewHandler(db *sql.DB, cartRepo *cart.Repository) *Handler {
	return &Handler{
		db:             db,
		paystackClient: NewClient(),
		cartRepo:       cartRepo,
	}
}

// Checkout handles POST /api/checkout.
// Supports both guest and authenticated payloads as defined in the spec.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	var req models.CheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ShippingAddress == "" {
		http.Error(w, "shipping_address is required", http.StatusBadRequest)
		return
	}

	user := auth.GetUserFromContext(r)

	var email string
	var cartItems []models.GuestCartItem

	if user != nil {
		// Authenticated checkout: use email from JWT and load cart from DB.
		email = user.Email
		dbItems, _, err := h.cartRepo.GetByUserID(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "failed to load cart", http.StatusInternalServerError)
			return
		}
		if len(dbItems) == 0 {
			http.Error(w, "cart is empty", http.StatusBadRequest)
			return
		}
		for _, item := range dbItems {
			cartItems = append(cartItems, models.GuestCartItem{
				ProductID: item.ProductID,
				Quantity:  item.Quantity,
			})
		}
	} else {
		// Guest checkout: email and cart items must be in the request body.
		if req.Email == "" {
			http.Error(w, "email is required for guest checkout", http.StatusBadRequest)
			return
		}
		if len(req.CartItems) == 0 {
			http.Error(w, "cart_items are required for guest checkout", http.StatusBadRequest)
			return
		}
		email = req.Email
		cartItems = req.CartItems
	}

	// Run checkout inside a transaction to prevent race conditions on stock.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start transaction", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Validate stock and calculate total from current DB prices.
	type lineItem struct {
		productID       string
		quantity        int
		priceAtPurchase float64
	}
	var lines []lineItem
	var total float64

	for _, item := range cartItems {
		var price float64
		var stock int
		var productName string
		err := tx.QueryRowContext(r.Context(),
			`SELECT price, stock, name FROM products WHERE id = ?`, item.ProductID,
		).Scan(&price, &stock, &productName)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("one of your cart items is no longer available (removed from store)"), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "failed to fetch product", http.StatusInternalServerError)
			return
		}
		if stock < item.Quantity {
			http.Error(w,
				fmt.Sprintf("only %d left in stock for \"%s\" — please reduce the quantity in your cart", stock, productName),
				http.StatusConflict,
			)
			return
		}
		total += price * float64(item.Quantity)
		lines = append(lines, lineItem{item.ProductID, item.Quantity, price})
	}

	// Decrement stock for each item inside the same transaction.
	for _, line := range lines {
		_, err = tx.ExecContext(r.Context(),
			`UPDATE products SET stock = stock - ?, updated_at = ? WHERE id = ?`,
			line.quantity, time.Now(), line.productID,
		)
		if err != nil {
			http.Error(w, "failed to reserve stock", http.StatusInternalServerError)
			return
		}
	}

	// Generate a unique Paystack reference.
	reference := "ROK-" + uuid.NewString()

	// Create pending order.
	orderID := uuid.NewString()
	now := time.Now()

	var userIDVal sql.NullString
	var guestEmailVal sql.NullString
	if user != nil {
		userIDVal = sql.NullString{String: user.ID, Valid: true}
	} else {
		guestEmailVal = sql.NullString{String: email, Valid: true}
	}

	_, err = tx.ExecContext(r.Context(),
		`INSERT INTO orders (id, user_id, guest_email, shipping_address, total_amount, payment_status, payment_reference, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		orderID, userIDVal, guestEmailVal, req.ShippingAddress, total, reference, now, now,
	)
	if err != nil {
		http.Error(w, "failed to create order", http.StatusInternalServerError)
		return
	}

	// Insert order line items.
	for _, line := range lines {
		_, err = tx.ExecContext(r.Context(),
			`INSERT INTO order_items (id, order_id, product_id, quantity, price_at_purchase)
			 VALUES (?, ?, ?, ?, ?)`,
			uuid.NewString(), orderID, line.productID, line.quantity, line.priceAtPurchase,
		)
		if err != nil {
			http.Error(w, "failed to create order items", http.StatusInternalServerError)
			return
		}
	}

	if err = tx.Commit(); err != nil {
		http.Error(w, "failed to commit order", http.StatusInternalServerError)
		return
	}

	// Build the callback URL so Paystack redirects the user to the success page
	// after payment. FRONTEND_URL must be set to your Vercel domain in production,
	// e.g. https://rok-skates.vercel.app
	// In development, set it to your local live-server origin, e.g. http://localhost:5500
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		log.Println("[checkout] WARNING: FRONTEND_URL env var not set — Paystack callback will not redirect correctly")
		frontendURL = "https://rok-store.vercel.app"
	}
	callbackURL := frontendURL + "/templates/checkout-success.html"

	// Call Paystack — convert GHS total to pesewas (×100).
	paystackResp, err := h.paystackClient.InitializeTransaction(InitializeTransactionRequest{
		Email:       email,
		AmountKobo:  int64(total * 100),
		Reference:   reference,
		CallbackURL: callbackURL,
		Metadata: map[string]string{
			"order_id": orderID,
		},
	})
	if err != nil {
		// Order created but payment init failed — mark as failed.
		h.db.Exec( //nolint:errcheck
			`UPDATE orders SET payment_status='failed', updated_at=? WHERE id=?`,
			time.Now(), orderID,
		)
		http.Error(w, "failed to initialize payment: "+err.Error(), http.StatusBadGateway)
		return
	}

	respondJSON(w, http.StatusOK, models.CheckoutResponse{
		AuthorizationURL: paystackResp.AuthorizationURL,
		Reference:        reference,
		OrderID:          orderID,
	})
}

// WebhookHandler handles POST /api/payments/webhook.
func (h *Handler) WebhookHandler(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("X-Paystack-Signature")
	if !VerifyWebhookSignature(payload, signature) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Respond immediately to Paystack before doing any heavy work.
	w.WriteHeader(http.StatusOK)

	// Process asynchronously. Use context.Background() — r.Context() is
	// cancelled the moment the 200 response is written above, which would
	// cause every DB call in this goroutine to silently fail.
	go func() {
		ctx := context.Background()

		var event struct {
			Event string `json:"event"`
			Data  struct {
				Reference string `json:"reference"`
				Status    string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Printf("[webhook] failed to parse event: %v", err)
			return
		}
		if event.Event != "charge.success" {
			return
		}

		reference := event.Data.Reference

		// Fetch the matching order.
		var orderID string
		var userID sql.NullString
		err := h.db.QueryRowContext(ctx,
			`SELECT id, user_id FROM orders WHERE payment_reference = ?`, reference,
		).Scan(&orderID, &userID)
		if err != nil {
			log.Printf("[webhook] order not found for reference %s: %v", reference, err)
			return
		}

		// Mark order as paid.
		if _, err = h.db.ExecContext(ctx,
			`UPDATE orders SET payment_status='paid', updated_at=? WHERE id=?`,
			time.Now(), orderID,
		); err != nil {
			log.Printf("[webhook] failed to mark order %s as paid: %v", orderID, err)
			return
		}

		// Clear DB cart for registered users.
		if userID.Valid {
			if err = h.cartRepo.ClearByUserID(ctx, userID.String); err != nil {
				log.Printf("[webhook] failed to clear cart for user %s: %v", userID.String, err)
			}
		}

		log.Printf("[webhook] order %s marked as paid (ref: %s)", orderID, reference)
	}()
}

// VerifyPayment handles GET /api/payments/verify/{reference}.
// Used as a client-side fallback when the webhook hasn't arrived yet —
// e.g. when the user is redirected back from Paystack's hosted page.
func (h *Handler) VerifyPayment(w http.ResponseWriter, r *http.Request) {
	reference := r.PathValue("reference")
	if reference == "" {
		http.Error(w, "reference is required", http.StatusBadRequest)
		return
	}

	// Check our own DB first — avoid a round-trip to Paystack if we already
	// know the outcome (webhook may have arrived before the user redirected).
	var orderID string
	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, payment_status FROM orders WHERE payment_reference = ?`, reference,
	).Scan(&orderID, &currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if currentStatus == "paid" {
		respondJSON(w, http.StatusOK, map[string]string{
			"order_id": orderID,
			"status":   "paid",
			"source":   "db",
		})
		return
	}

	// Status is still pending — ask Paystack directly.
	paystackStatus, err := h.paystackClient.VerifyTransaction(reference)
	if err != nil {
		http.Error(w, "failed to verify with Paystack: "+err.Error(), http.StatusBadGateway)
		return
	}

	// If Paystack confirms success, update our DB proactively so the webhook
	// race doesn't matter.
	if paystackStatus == "success" {
		ctx := context.Background()
		if _, err = h.db.ExecContext(ctx,
			`UPDATE orders SET payment_status='paid', updated_at=? WHERE id=?`,
			time.Now(), orderID,
		); err != nil {
			log.Printf("[verify] failed to update order %s to paid: %v", orderID, err)
		}
		currentStatus = "paid"
	} else if paystackStatus == "failed" {
		currentStatus = "failed"
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"order_id": orderID,
		"status":   currentStatus,
		"source":   "paystack",
	})
}

// ListOrders handles GET /api/admin/orders.
// Returns all orders (newest first) with their line items, for admin use.
func (h *Handler) ListOrders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT o.id, o.user_id, o.guest_email, o.shipping_address,
		        o.total_amount, o.payment_status, o.payment_reference,
		        o.created_at, u.name, u.email
		 FROM orders o
		 LEFT JOIN users u ON u.id = o.user_id
		 ORDER BY o.created_at DESC`,
	)
	if err != nil {
		http.Error(w, "failed to query orders", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type OrderItem struct {
		ID              string  `json:"id"`
		ProductID       string  `json:"product_id"`
		ProductName     string  `json:"product_name"`
		Quantity        int     `json:"quantity"`
		PriceAtPurchase float64 `json:"price_at_purchase"`
	}
	type Order struct {
		ID               string      `json:"id"`
		CustomerName     string      `json:"customer_name"`
		CustomerEmail    string      `json:"customer_email"`
		ShippingAddress  string      `json:"shipping_address"`
		TotalAmount      float64     `json:"total_amount"`
		PaymentStatus    string      `json:"payment_status"`
		PaymentReference string      `json:"payment_reference"`
		CreatedAt        string      `json:"created_at"`
		Items            []OrderItem `json:"items"`
	}

	var orders []Order
	for rows.Next() {
		var o Order
		var userID, guestEmail, userName, userEmail sql.NullString
		if err := rows.Scan(
			&o.ID, &userID, &guestEmail, &o.ShippingAddress,
			&o.TotalAmount, &o.PaymentStatus, &o.PaymentReference,
			&o.CreatedAt, &userName, &userEmail,
		); err != nil {
			http.Error(w, "failed to scan order", http.StatusInternalServerError)
			return
		}
		// Resolve customer identity: registered user takes priority over guest email.
		if userName.Valid {
			o.CustomerName = userName.String
		}
		if userEmail.Valid {
			o.CustomerEmail = userEmail.String
		} else if guestEmail.Valid {
			o.CustomerEmail = guestEmail.String
			if o.CustomerName == "" {
				o.CustomerName = "Guest"
			}
		}
		orders = append(orders, o)
	}
	if err = rows.Err(); err != nil {
		http.Error(w, "row iteration error", http.StatusInternalServerError)
		return
	}

	// Fetch line items for each order.
	for i, o := range orders {
		itemRows, err := h.db.QueryContext(r.Context(),
			`SELECT oi.id, oi.product_id, COALESCE(p.name, 'Deleted product'),
			        oi.quantity, oi.price_at_purchase
			 FROM order_items oi
			 LEFT JOIN products p ON p.id = oi.product_id
			 WHERE oi.order_id = ?`,
			o.ID,
		)
		if err != nil {
			continue
		}
		for itemRows.Next() {
			var it OrderItem
			if err := itemRows.Scan(&it.ID, &it.ProductID, &it.ProductName, &it.Quantity, &it.PriceAtPurchase); err == nil {
				orders[i].Items = append(orders[i].Items, it)
			}
		}
		itemRows.Close()
		if orders[i].Items == nil {
			orders[i].Items = []OrderItem{}
		}
	}

	if orders == nil {
		orders = []Order{}
	}
	respondJSON(w, http.StatusOK, orders)
}

// ---- helpers ----

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
