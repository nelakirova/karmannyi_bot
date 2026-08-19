package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/google/uuid"
)

type YooKassaPaymentResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Paid   bool   `json:"paid"`

	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`

	Confirmation struct {
		Type            string `json:"type"`
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`

	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

func createYooKassaPayment(
	orderID string,
	email string,
) (*YooKassaPaymentResponse, error) {

	shopID := os.Getenv("YOOKASSA_SHOP_ID")
	secretKey := os.Getenv("YOOKASSA_SECRET_KEY")

	if shopID == "" {
		return nil, fmt.Errorf("YOOKASSA_SHOP_ID не установлен")
	}

	if secretKey == "" {
		return nil, fmt.Errorf("YOOKASSA_SECRET_KEY не установлен")
	}

	requestBody := map[string]interface{}{
		"amount": map[string]string{
			"value":    "99.00",
			"currency": "RUB",
		},

		"capture": true,

		"confirmation": map[string]string{
			"type":       "redirect",
			"return_url": "https://t.me/karmannyi_bot",
		},

		"description": "Доступ в канал «8–10 лет Карманный воспитатель»",

		"metadata": map[string]string{
			"order_id": orderID,
		},

		"receipt": map[string]interface{}{
			"customer": map[string]string{
				"email": email,
			},

			"items": []map[string]interface{}{
				{
					"description": "Доступ в канал «8–10 лет Карманный воспитатель»",

					"quantity": 1,

					"amount": map[string]string{
						"value":    "99.00",
						"currency": "RUB",
					},

					"vat_code":       11,
					"payment_mode":  "full_payment",
					"payment_subject": "service",
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf(
			"ошибка формирования JSON: %w",
			err,
		)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		"https://api.yookassa.ru/v3/payments",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"ошибка создания HTTP-запроса: %w",
			err,
		)
	}

	req.SetBasicAuth(shopID, secretKey)

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	req.Header.Set(
		"Idempotence-Key",
		uuid.New().String(),
	)

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf(
			"ошибка запроса к ЮKassa: %w",
			err,
		)
	}

	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"ошибка чтения ответа ЮKassa: %w",
			err,
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"ЮKassa вернула HTTP %d: %s",
			resp.StatusCode,
			string(responseBody),
		)
	}

	var payment YooKassaPaymentResponse

	if err := json.Unmarshal(
		responseBody,
		&payment,
	); err != nil {
		return nil, fmt.Errorf(
			"ошибка разбора ответа ЮKassa: %w; ответ: %s",
			err,
			string(responseBody),
		)
	}

	return &payment, nil
}

func getYooKassaPayment(paymentID string) (*YooKassaPaymentResponse, error) {
	shopID := os.Getenv("YOOKASSA_SHOP_ID")
	secretKey := os.Getenv("YOOKASSA_SECRET_KEY")

	if shopID == "" {
		return nil, fmt.Errorf("YOOKASSA_SHOP_ID не установлен")
	}

	if secretKey == "" {
		return nil, fmt.Errorf("YOOKASSA_SECRET_KEY не установлен")
	}

	req, err := http.NewRequest(
		http.MethodGet,
		"https://api.yookassa.ru/v3/payments/"+paymentID,
		nil,
	)

	if err != nil {
		return nil, fmt.Errorf(
			"ошибка создания запроса к ЮKassa: %w",
			err,
		)
	}

	req.SetBasicAuth(shopID, secretKey)

	resp, err := (&http.Client{}).Do(req)

	if err != nil {
		return nil, fmt.Errorf(
			"ошибка запроса к ЮKassa: %w",
			err,
		)
	}

	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)

	if err != nil {
		return nil, fmt.Errorf(
			"ошибка чтения ответа ЮKassa: %w",
			err,
		)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(
			"ЮKassa вернула HTTP %d: %s",
			resp.StatusCode,
			string(responseBody),
		)
	}

	var payment YooKassaPaymentResponse

	if err := json.Unmarshal(
		responseBody,
		&payment,
	); err != nil {
		return nil, fmt.Errorf(
			"ошибка разбора платежа ЮKassa: %w",
			err,
		)
	}

	return &payment, nil
}