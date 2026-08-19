package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func initDatabase() error {
	var err error

	db, err = sql.Open("sqlite", "karmannyi.db")
	if err != nil {
		return err
	}

	// Проверяем соединение с БД.
	if err := db.Ping(); err != nil {
		return err
	}

	// Основная таблица заказов.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id TEXT NOT NULL UNIQUE,
			telegram_user_id INTEGER NOT NULL,
			amount INTEGER NOT NULL,
			status TEXT NOT NULL,
			yookassa_payment_id TEXT,
			customer_email TEXT,
			invite_link TEXT,
			created_at DATETIME NOT NULL
		)
	`)

	if err != nil {
		return err
	}

	// ------------------------------------------------------------
	// МИГРАЦИЯ СТАРОЙ БАЗЫ
	// ------------------------------------------------------------
	//
	// У тебя karmannyi.db уже существует и была создана
	// без invite_link.
	//
	// Поэтому проверяем наличие колонки и добавляем её,
	// если её ещё нет.
	//

	var (
		hasInviteLink bool
		hasProductID  bool
	)

	rows, err := db.Query(`PRAGMA table_info(orders)`)
	if err != nil {
		return err
	}

	for rows.Next() {
		var (
			cid        int
			name       string
			dataType   string
			notNull    int
			defaultV   interface{}
			primaryKey int
		)

		if err := rows.Scan(
			&cid,
			&name,
			&dataType,
			&notNull,
			&defaultV,
			&primaryKey,
		); err != nil {
			rows.Close()
			return err
		}

		switch name {
		case "invite_link":
			hasInviteLink = true
		case "product_id":
			hasProductID = true
		}
	}

	rows.Close()

	if !hasInviteLink {
		_, err := db.Exec(`
			ALTER TABLE orders
			ADD COLUMN invite_link TEXT
		`)

		if err != nil {
			return fmt.Errorf(
				"не удалось добавить invite_link: %w",
				err,
			)
		}

		fmt.Println("База данных обновлена: добавлено поле invite_link")
	}

	// product_id — какой из 12 курсов купил пользователь.
	// У старых заказов (созданных до появления каталога) это поле
	// будет пустым — на такой случай в processSuccessfulPayment
	// есть запасной вариант (legacyProduct).
	if !hasProductID {
		_, err := db.Exec(`
			ALTER TABLE orders
			ADD COLUMN product_id TEXT
		`)

		if err != nil {
			return fmt.Errorf(
				"не удалось добавить product_id: %w",
				err,
			)
		}

		fmt.Println("База данных обновлена: добавлено поле product_id")
	}

	return nil
}

// ============================================================
// СОЗДАНИЕ ЗАКАЗА
// ============================================================

func createOrder(
	telegramUserID int64,
	productID string,
	amount int,
) (string, error) {
	orderID := fmt.Sprintf(
		"order_%d_%d",
		telegramUserID,
		time.Now().UnixNano(),
	)

	_, err := db.Exec(`
		INSERT INTO orders (
			order_id,
			telegram_user_id,
			amount,
			status,
			product_id,
			created_at
		)
		VALUES (?, ?, ?, ?, ?, ?)
	`,
		orderID,
		telegramUserID,
		amount,
		"pending",
		productID,
		time.Now(),
	)

	if err != nil {
		return "", err
	}

	return orderID, nil
}

// ============================================================
// E-MAIL
// ============================================================

func updateOrderEmail(orderID string, email string) error {
	_, err := db.Exec(`
		UPDATE orders
		SET customer_email = ?
		WHERE order_id = ?
	`, email, orderID)

	return err
}

// ============================================================
// PAYMENT ID
// ============================================================

func updateYooKassaPaymentID(
	orderID string,
	paymentID string,
) error {
	_, err := db.Exec(`
		UPDATE orders
		SET yookassa_payment_id = ?
		WHERE order_id = ?
	`, paymentID, orderID)

	return err
}

// ============================================================
// INVITE LINK
// ============================================================

func updateInviteLink(
	orderID string,
	inviteLink string,
) error {
	_, err := db.Exec(`
		UPDATE orders
		SET invite_link = ?
		WHERE order_id = ?
	`, inviteLink, orderID)

	return err
}

// ============================================================
// ORDER
// ============================================================

type Order struct {
	OrderID           string
	TelegramUserID    int64
	Amount            int
	Status            string
	YooKassaPaymentID string
	CustomerEmail     string
	InviteLink        string
	ProductID         string
}

// ============================================================
// ПОИСК ЗАКАЗА ПО PAYMENT ID
// ============================================================

func getOrderByPaymentID(
	paymentID string,
) (*Order, error) {

	var order Order

	err := db.QueryRow(`
		SELECT
			order_id,
			telegram_user_id,
			amount,
			status,
			yookassa_payment_id,
			customer_email,
			COALESCE(invite_link, ''),
			COALESCE(product_id, '')
		FROM orders
		WHERE yookassa_payment_id = ?
		LIMIT 1
	`, paymentID).Scan(
		&order.OrderID,
		&order.TelegramUserID,
		&order.Amount,
		&order.Status,
		&order.YooKassaPaymentID,
		&order.CustomerEmail,
		&order.InviteLink,
		&order.ProductID,
	)

	if err != nil {
		return nil, err
	}

	return &order, nil
}

// ============================================================
// СТАТУС
// ============================================================

func updateOrderStatus(
	orderID string,
	status string,
) error {
	_, err := db.Exec(`
		UPDATE orders
		SET status = ?
		WHERE order_id = ?
	`, status, orderID)

	return err
}

// ============================================================
// ЗАКАЗЫ, КОТОРЫЕ НУЖНО ПРОВЕРИТЬ
// ============================================================

func getOrdersToCheck() ([]Order, error) {
	rows, err := db.Query(`
		SELECT
			order_id,
			telegram_user_id,
			amount,
			status,
			yookassa_payment_id,
			COALESCE(customer_email, ''),
			COALESCE(invite_link, ''),
			COALESCE(product_id, '')
		FROM orders
		WHERE
			yookassa_payment_id IS NOT NULL
			AND yookassa_payment_id != ''
			AND (
				invite_link IS NULL
				OR invite_link = ''
			)
			AND status != 'canceled'
		ORDER BY created_at ASC
	`)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var orders []Order

	for rows.Next() {
		var order Order

		if err := rows.Scan(
			&order.OrderID,
			&order.TelegramUserID,
			&order.Amount,
			&order.Status,
			&order.YooKassaPaymentID,
			&order.CustomerEmail,
			&order.InviteLink,
			&order.ProductID,
		); err != nil {
			return nil, err
		}

		orders = append(orders, order)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return orders, nil
}

// ============================================================
// ПОСЛЕДНИЙ ЗАКАЗ ПОЛЬЗОВАТЕЛЯ
// ============================================================

func getLastOrderByUserID(
	telegramUserID int64,
) (*Order, error) {

	var order Order

	err := db.QueryRow(`
		SELECT
			order_id,
			telegram_user_id,
			amount,
			status,
			COALESCE(yookassa_payment_id, ''),
			COALESCE(customer_email, ''),
			COALESCE(invite_link, ''),
			COALESCE(product_id, '')
		FROM orders
		WHERE telegram_user_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, telegramUserID).Scan(
		&order.OrderID,
		&order.TelegramUserID,
		&order.Amount,
		&order.Status,
		&order.YooKassaPaymentID,
		&order.CustomerEmail,
		&order.InviteLink,
		&order.ProductID,
	)

	if err != nil {
		return nil, err
	}

	return &order, nil
}