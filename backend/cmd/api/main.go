package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/kwabsntim/rok-store/internal/auth"
	"github.com/kwabsntim/rok-store/internal/cart"
	"github.com/kwabsntim/rok-store/internal/database"
	"github.com/kwabsntim/rok-store/internal/idempotency"
	"github.com/kwabsntim/rok-store/internal/payment"
	"github.com/kwabsntim/rok-store/internal/product"
	"github.com/kwabsntim/rok-store/internal/ratelimit"
)

func main() {
	// Load .env file (silently ignored in production if not present).
	if err := godotenv.Load(); err != nil {
		log.Println("[main] no .env file found, reading from environment")
	}

	// ---- Database ----
	db, err := database.Connect()
	if err != nil {
		log.Fatalf("[main] database connection failed: %v", err)
	}
	defer db.Close()

	if err = database.Migrate(db); err != nil {
		log.Fatalf("[main] migration failed: %v", err)
	}

	// ---- Repositories ----
	productRepo := product.NewRepository(db)
	cartRepo := cart.NewRepository(db)
	idempotencyRepo := idempotency.NewRepository(db)

	// ---- Handlers ----
	authHandler := auth.NewHandler(db)
	productHandler := product.NewHandler(productRepo)
	cartHandler := cart.NewHandler(cartRepo)
	paymentHandler := payment.NewHandler(db, cartRepo)

	// ---- Rate limiters (per spec) ----
	authLimiter := ratelimit.NewStore(5)      // 5 req/min per IP — auth routes
	checkoutLimiter := ratelimit.NewStore(3)  // 3 req/min per IP — checkout
	productsLimiter := ratelimit.NewStore(100) // 100 req/min per IP — public products

	// ---- Router (stdlib net/http, Go 1.22+ pattern matching) ----
	mux := http.NewServeMux()

	// -- Auth (rate-limited: 5/min) --
	mux.Handle("POST /api/auth/register",
		authLimiter.Middleware(http.HandlerFunc(authHandler.Register)))
	mux.Handle("POST /api/auth/login",
		authLimiter.Middleware(http.HandlerFunc(authHandler.Login)))
	mux.HandleFunc("POST /api/auth/logout", authHandler.Logout)

	// -- Public Products (rate-limited: 100/min) --
	mux.Handle("GET /api/products",
		productsLimiter.Middleware(http.HandlerFunc(productHandler.ListProducts)))
	mux.Handle("GET /api/products/newest",
		productsLimiter.Middleware(http.HandlerFunc(productHandler.ListNewest)))
	mux.Handle("GET /api/products/{id}",
		productsLimiter.Middleware(http.HandlerFunc(productHandler.GetProduct)))

	// -- Admin Products (require admin role, no extra rate limit) --
	adminMiddleware := auth.RequireRole("admin")
	mux.Handle("POST /api/admin/products",
		adminMiddleware(http.HandlerFunc(productHandler.CreateProduct)))
	mux.Handle("PUT /api/admin/products/{id}",
		adminMiddleware(http.HandlerFunc(productHandler.UpdateProduct)))
	mux.Handle("DELETE /api/admin/products/{id}",
		adminMiddleware(http.HandlerFunc(productHandler.DeleteProduct)))

	// -- Cart (require auth) --
	mux.Handle("GET /api/cart",
		auth.RequireAuth(http.HandlerFunc(cartHandler.GetCart)))
	mux.Handle("POST /api/cart",
		auth.RequireAuth(http.HandlerFunc(cartHandler.AddToCart)))
	mux.Handle("PUT /api/cart/{product_id}",
		auth.RequireAuth(http.HandlerFunc(cartHandler.UpdateCartItem)))
	mux.Handle("DELETE /api/cart/{product_id}",
		auth.RequireAuth(http.HandlerFunc(cartHandler.RemoveCartItem)))

	// -- Checkout (rate-limited: 3/min, optional auth, idempotency enforced) --
	idempotencyMiddleware := idempotency.Middleware(idempotencyRepo)
	mux.Handle("POST /api/checkout",
		checkoutLimiter.Middleware(
			auth.OptionalAuth(
				idempotencyMiddleware(
					http.HandlerFunc(paymentHandler.Checkout),
				),
			),
		),
	)

	// -- Payments --
	// Webhook: public, Paystack signature-verified.
	mux.HandleFunc("POST /api/payments/webhook", paymentHandler.WebhookHandler)
	// Verify: client-side fallback for when webhook hasn't arrived yet.
	mux.HandleFunc("GET /api/payments/verify/{reference}", paymentHandler.VerifyPayment)

	// -- Health / ping (used by self-ping to prevent Render free tier sleeping) --
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	})

	// ---- CORS + global middleware ----
	handler := corsMiddleware(mux)

	// ---- Background: idempotency key cleanup (runs every hour) ----
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := idempotencyRepo.Cleanup(ctx); err != nil {
				log.Printf("[cleanup] idempotency key cleanup failed: %v", err)
			} else {
				log.Println("[cleanup] expired idempotency keys removed")
			}
			cancel()
		}
	}()

	// ---- Background: release stock for orders stuck in 'pending' > 30 min ----
	// This handles the case where a user abandons the Paystack payment page
	// and never completes or cancels — stock would otherwise be held forever.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			// Find all orders that have been pending for more than 30 minutes.
			rows, err := db.QueryContext(ctx,
				`SELECT id FROM orders
				 WHERE payment_status = 'pending'
				   AND created_at <= datetime('now', '-30 minutes')`,
			)
			if err != nil {
				log.Printf("[stock-release] query failed: %v", err)
				cancel()
				continue
			}

			var expiredOrderIDs []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err == nil {
					expiredOrderIDs = append(expiredOrderIDs, id)
				}
			}
			rows.Close()

			for _, orderID := range expiredOrderIDs {
				// Restore stock for each line item in the expired order.
				_, err := db.ExecContext(ctx,
					`UPDATE products
					 SET stock = stock + oi.quantity,
					     updated_at = datetime('now')
					 FROM order_items oi
					 WHERE products.id = oi.product_id
					   AND oi.order_id = ?`,
					orderID,
				)
				if err != nil {
					log.Printf("[stock-release] failed to restore stock for order %s: %v", orderID, err)
					continue
				}

				// Mark the order as expired so it isn't processed again.
				_, err = db.ExecContext(ctx,
					`UPDATE orders SET payment_status = 'expired', updated_at = datetime('now') WHERE id = ?`,
					orderID,
				)
				if err != nil {
					log.Printf("[stock-release] failed to mark order %s as expired: %v", orderID, err)
					continue
				}

				log.Printf("[stock-release] expired order %s — stock restored", orderID)
			}

			cancel()
		}
	}()

	// ---- Background: self-ping every 14 min to prevent Render free tier sleeping ----
	appURL := os.Getenv("RENDER_EXTERNAL_URL")
	if appURL != "" {
		go func() {
			ticker := time.NewTicker(14 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				resp, err := http.Get(appURL + "/ping")
				if err != nil {
					log.Printf("[self-ping] failed: %v", err)
					continue
				}
				resp.Body.Close()
				log.Println("[self-ping] OK")
			}
		}()
	}

	// ---- Server ----
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ---- Graceful shutdown ----
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("[main] ROK Store API listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[main] server error: %v", err)
		}
	}()

	<-quit
	log.Println("[main] shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("[main] forced shutdown: %v", err)
	}
	log.Println("[main] server stopped")
}

// corsMiddleware handles CORS. Returns * in development so any origin works.
// In production set ALLOWED_ORIGIN to your Vercel domain.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", getAllowedOrigin(r))
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
		w.Header().Set("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getAllowedOrigin(r *http.Request) string {
	// Production: explicit origin from env var
	if allowed := os.Getenv("ALLOWED_ORIGIN"); allowed != "" {
		return allowed
	}
	// Development: allow all origins (no credentials header needed since we use Bearer tokens)
	return "*"
}
