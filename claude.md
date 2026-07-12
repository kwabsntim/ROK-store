# ROK Store - Skate Brand E-Commerce Backend Specification

Welcome to the development guide for the **ROK Store** backend. This document outlines the system architecture, database schema, API endpoints, authentication flows, payment integration, deployment details, security implementations, performance bottleneck mitigations, and a robust idempotency system to guide implementation.

---

## 1. Tech Stack Overview

- **Language:** Go (Golang) 1.22+ (utilizing the new `net/http` routing enhancements or `go-chi`)
- **Database:** SQLite for local development, migrating seamlessly to **Turso Cloud** (libsql driver) for production.
- **Authentication:** JSON Web Tokens (JWT) with Role-Based Access Control (RBAC).
- **Payment Processor:** Paystack API.
- **Hosting:** Render (Web Service for Go).
- **Environment Management:** `.env` file containing secrets, ports, and database credentials.

---

## 2. Directory Structure

A clean, idiomatic Go project layout is recommended to ensure scalability and ease of maintenance:

```text
rok-store-backend/
├── cmd/
│   └── api/
│       └── main.go             # Entry point: initializes DB, router, and starts server
├── internal/
│   ├── auth/
│   │   ├── handler.go          # Auth handlers (Login, Register)
│   │   ├── middleware.go       # JWT & RBAC Middlewares
│   │   └── service.go          # Token generation, password hashing (argon2id)
│   ├── cart/
│   │   ├── handler.go          # Cart CRUD handlers
│   │   └── repository.go       # SQL queries for Cart operations
│   ├── database/
│   │   ├── connection.go       # SQLite / Turso driver connection setup
│   │   └── migration.go        # Schema migrations
│   ├── idempotency/
│   │   ├── middleware.go       # Idempotency checks and caching
│   │   └── repository.go       # SQL queries for idempotency key tracking
│   ├── models/
│   │   └── models.go           # Shared data structures (User, Product, Cart, etc.)
│   ├── payment/
│   │   ├── client.go           # Paystack API wrapper
│   │   └── handler.go          # Webhook & transaction initialization handlers
│   └── product/
│       ├── handler.go          # Product endpoints (Admin CRUD & Public Reads)
│       └── repository.go       # SQL queries for Product management
├── .env.example
├── go.mod
├── go.sum
└── schema.sql                  # Database migration schema
```

---

## 3. Database Schema (SQLite / Turso)

Since Turso uses SQLite, we write our schema using SQLite syntax. UUIDs (TEXT) are used for primary keys for cloud compliance.

```sql
-- Enable foreign keys
PRAGMA foreign_keys = ON;

-- Users Table
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'customer', -- 'admin' or 'customer'
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Products Table
CREATE TABLE IF NOT EXISTS products (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    price REAL NOT NULL,
    description TEXT,
    size TEXT, -- e.g., "8.0", "8.25", "M", "L"
    category TEXT NOT NULL, -- e.g., "decks", "apparel", "wheels"
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Cart Items Table
CREATE TABLE IF NOT EXISTS cart_items (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE CASCADE,
    UNIQUE(user_id, product_id)
);

-- Orders Table
CREATE TABLE IF NOT EXISTS orders (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    total_amount REAL NOT NULL,
    payment_status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'paid', 'failed'
    payment_reference TEXT UNIQUE NOT NULL, -- Paystack transaction reference
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT
);

-- Order Items Table (Audit history, preserves prices at time of order)
CREATE TABLE IF NOT EXISTS order_items (
    id TEXT PRIMARY KEY,
    order_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    quantity INTEGER NOT NULL,
    price_at_purchase REAL NOT NULL,
    FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE RESTRICT
);

-- Idempotency Keys Table
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    request_path TEXT NOT NULL,
    response_code INTEGER,
    response_body TEXT,
    status TEXT NOT NULL DEFAULT 'started', -- 'started', 'completed', 'failed'
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

---

## 4. API Specification & Endpoints

### 4.1. Authentication (`/api/auth`)
- `POST /api/auth/register` - Public: Registers a new customer.
- `POST /api/auth/login` - Public: Validates credentials, returns JWT containing user context.

### 4.2. Public Products (`/api/products`)
- `GET /api/products` - Public: Lists all products with pagination and category filtering.
- `GET /api/products/newest` - Public: Returns the newest products ("Fresh Arrivals") sorted by `created_at DESC` (limited to top 8 items).
- `GET /api/products/:id` - Public: Gets a single product's details.

### 4.3. Admin Products CRUD (`/api/admin/products`)
*Requires JWT authentication and `role = 'admin'`.*
- `POST /api/admin/products` - Admin only: Creates a new product.
- `PUT /api/admin/products/:id` - Admin only: Updates an existing product.
- `DELETE /api/admin/products/:id` - Admin only: Deletes a product.

### 4.4. Shopping Cart (`/api/cart`)
*Requires JWT authentication.*
- `GET /api/cart` - Returns current user's cart items, product details, and the total price.
- `POST /api/cart` - Adds an item to the cart, or increments quantity if already exists.
- `PUT /api/cart/:product_id` - Updates quantity of a specific item.
- `DELETE /api/cart/:product_id` - Removes a product from the cart.

### 4.5. Checkout & Payments (`/api/checkout` & `/api/payments`)
*Requires JWT authentication for initialization.*
- `POST /api/checkout` - Customer initializes checkout.
  - **Requires `Idempotency-Key` header.**
  - Calculates total from cart items in the database.
  - Calls Paystack's Initialize Transaction API.
  - Creates a pending order in the database.
  - Returns `authorization_url` and `reference` to frontend.
- `POST /api/payments/webhook` - Public: Paystack webhook callback.
  - Validates webhook signature.
  - Processes `charge.success` events to mark order as `paid` and clear cart.

---

## 5. Potential Failure Points & Performance Bottlenecks

### 5.1. SQLite Concurrent Writes & Connection Limits
- **Issue:** SQLite serializes write transactions. Under high write loads (e.g., flash sales, bulk cart actions), Go's goroutines might block waiting to write, resulting in `database is locked` (`SQLITE_BUSY`) errors.
- **Mitigation:**
  1. Enable **Write-Ahead Logging (WAL) Mode** on the SQLite database:
     ```sql
     PRAGMA journal_mode = WAL;
     PRAGMA synchronous = NORMAL;
     ```
  2. Set a busy timeout in Go's driver: `_busy_timeout=5000` (waits up to 5 seconds before failing).
  3. Configure Go's SQL connection pool properly:
     ```go
     db.SetMaxOpenConns(1) // Mandatory for local file SQLite to avoid write conflicts
     ```
     *(Note: If using Turso, connection limits are managed via HTTP/WebSockets. However, concurrent writes are still serialized at the server level, requiring retry mechanisms for failed database operations).*

### 5.2. Paystack Webhook Failure or Latency
- **Issue:** Webhook deliveries can fail, be delayed, or be sent multiple times by Paystack. Heavy backend operations during webhooks (e.g., sending emails, invoicing) can time out the Paystack server callback.
- **Mitigation:**
  1. Webhook handlers must respond with `200 OK` immediately after validating the signature.
  2. Process state updates asynchronously using a Goroutine (or message queue) so you don't block the HTTP response thread.
  3. Query the Paystack API directly at `/transaction/verify/:reference` as a fallback when a customer returns to the site, in case the webhook hasn't arrived yet.

### 5.3. Race Conditions in Stock/Cart Verification
- **Issue:** A customer checks out when an item is *almost* out of stock. If two requests execute checkout concurrently, they might both read positive stock but proceed to over-allocate.
- **Mitigation:** Run the checkout process inside a database transaction (`db.BeginTx`). Execute a validation query inside the transaction to check stock availability before finalizing.

---

## 6. Security Guidelines & Implementation

### 6.1. Password Hashing (Argon2id)
Instead of standard bcrypt, use **Argon2id** (via `golang.org/x/crypto/argon2`) which is memory-hard and safer against GPU-based cracking.
- Recommended configuration: `time = 1`, `memory = 64 * 1024` (64MB), `threads = 4`, `keyLen = 32`.

### 6.2. JWT Verification and Token Hijacking Defense
- **Signing Key Security:** Never hardcode the signing key. Load it from environment variables. Rotate regularly.
- **Token Delivery:** 
  - For browser clients, store JWTs inside **HttpOnly, Secure, and SameSite=Strict cookies** instead of LocalStorage. This completely mitigates Cross-Site Scripting (XSS) token theft.
  - For mobile clients, use the standard `Authorization: Bearer <token>` header over secure TLS.

### 6.3. Rate Limiting
Prevent brute-force logins and endpoint abuse (like scraping products or flooding checkout) using token-bucket rate-limiting middleware (e.g., `golang.org/x/time/rate`).
- **Auth Routes (`/api/auth/login`, `/api/auth/register`):** Limit to 5 attempts per IP per minute.
- **Checkout Initialization (`/api/checkout`):** Limit to 3 requests per user per minute.
- **Public Product Read:** Limit to 100 requests per IP per minute.

### 6.4. Webhook Security (Payload Validation)
Validate the webhook payload signature to prevent attackers from sending fake payment success payloads.
```go
func VerifyPaystackSignature(payload []byte, headerSignature string, secret []byte) bool {
    mac := hmac.New(sha512.New, secret)
    mac.Write(payload)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(headerSignature))
}
```

### 6.5. SQL Injection Prevention
Ensure that **no raw string concatenation** is used to construct SQL queries. Always use parameterized queries:
```go
// Correct
db.QueryRowContext(ctx, "SELECT price FROM products WHERE id = ?", productID)

// Incorrect
db.QueryRowContext(ctx, fmt.Sprintf("SELECT price FROM products WHERE id = '%s'", productID))
```

---

## 7. Strong Idempotency System

To prevent double-charging and duplicate order generation (e.g., when a user clicks the "Checkout" button multiple times, or experienced network latency), implement an **Idempotency-Key** middleware for mutable operations like checkout.

### 7.1. Idempotency Key Flow

```mermaid
sequenceDiagram
    Client->>Middleware: POST /api/checkout (Idempotency-Key: <UUID>)
    rect rgb(30, 30, 40)
        Note over Middleware: Check DB: SELECT status, response_code, response_body FROM idempotency_keys
        alt Key Exists & status == 'completed'
            Middleware-->>Client: Cached Response (e.g. 200 OK)
        alt Key Exists & status == 'started'
            Middleware-->>Client: 409 Conflict (Request already in progress)
        alt Key Does Not Exist
            Middleware->>DB: INSERT key, status='started', expires_at
            Middleware->>Handler: Proceed to API Handler
            Note over Handler: Execute logic (DB updates, Paystack API call)
            Handler->>Middleware: API Response Payload
            Middleware->>DB: UPDATE key status='completed', response_code, response_body
            Middleware-->>Client: Send Response
        end
    end
```

### 7.2. Middleware Implementation Blueprint (Go)

```go
func IdempotencyMiddleware(db *sql.DB) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Only apply idempotency to write operations (POST, PUT)
            if r.Method != http.MethodPost && r.Method != http.MethodPut {
                next.ServeHTTP(w, r)
                return
            }

            key := r.Header.Get("Idempotency-Key")
            if key == "" {
                // If it's a critical route (e.g., checkout), require the key
                if r.URL.Path == "/api/checkout" {
                    http.Error(w, "Idempotency-Key header is required", http.StatusBadRequest)
                    return
                }
                next.ServeHTTP(w, r)
                return
            }

            userID := getUserIDFromContext(r.Context()) // Extracted from JWT

            var status string
            var cachedCode sql.NullInt32
            var cachedBody sql.NullString

            err := db.QueryRowContext(r.Context(), 
                `SELECT status, response_code, response_body 
                 FROM idempotency_keys WHERE key = ? AND user_id = ?`, 
                key, userID).Scan(&status, &cachedCode, &cachedBody)

            if err == nil {
                if status == "started" {
                    http.Error(w, "An identical request is currently processing", http.StatusConflict)
                    return
                }
                if status == "completed" && cachedCode.Valid {
                    w.Header().Set("Content-Type", "application/json")
                    w.Header().Set("X-Cache-Lookup", "HIT - Idempotent")
                    w.WriteHeader(int(cachedCode.Int32))
                    w.Write([]byte(cachedBody.String))
                    return
                }
            } else if err != sql.ErrNoRows {
                http.Error(w, "Database error", http.StatusInternalServerError)
                return
            }

            // Lock key: Insert as "started"
            expiry := time.Now().Add(24 * time.Hour) // Retain keys for 24h
            _, err = db.ExecContext(r.Context(), 
                `INSERT INTO idempotency_keys (key, user_id, request_path, status, expires_at) 
                 VALUES (?, ?, ?, 'started', ?)`, 
                key, userID, r.URL.Path, expiry)
            if err != nil {
                http.Error(w, "Failed to lock idempotency key", http.StatusInternalServerError)
                return
            }

            // Intercept writer response
            rec := httptest.NewRecorder()
            next.ServeHTTP(rec, r)

            // Cache response on success or complete failure
            statusCode := rec.Code
            responseBody := rec.Body.String()

            // Write back to real ResponseWriter
            for k, v := range rec.Result().Header {
                w.Header()[k] = v
            }
            w.WriteHeader(statusCode)
            w.Write([]byte(responseBody))

            // Update idempotency key status in DB
            db.ExecContext(r.Context(), 
                `UPDATE idempotency_keys 
                 SET status = 'completed', response_code = ?, response_body = ?, updated_at = CURRENT_TIMESTAMP
                 WHERE key = ? AND user_id = ?`, 
                statusCode, responseBody, key, userID)
        })
    }
}
```

---

## 8. Environment Variables (`.env`)

```ini
PORT=8080
ENV=development # development, production

# Local Database
DATABASE_URL=file:rokstore.db

# Turso Cloud Database (Production)
# DATABASE_URL=libsql://your-db-name.turso.io?authToken=your-auth-token

# JWT Secret
JWT_SECRET=your-super-secret-jwt-key

# Paystack Configuration
PAYSTACK_SECRET_KEY=sk_test_...
PAYSTACK_WEBHOOK_SECRET=webhook_secret_from_paystack_dashboard
```

---

## 9. Deployment Configuration

### 9.1. Render Hosting
1. Create a **Web Service** on Render.
2. Select **Go** as the environment.
3. Build Command: `go build -o bin/api cmd/api/main.go`
4. Start Command: `./bin/api`
5. Inject all Environment Variables in the Render dashboard.

### 9.2. Turso Database Setup
1. Create database: `turso db create rok-store-db`
2. Get database URL: `turso db show rok-store-db --url`
3. Generate token: `turso db tokens create rok-store-db`
4. Set the `DATABASE_URL` in Render using the format: `libsql://your-db.turso.io?authToken=your-token`.
