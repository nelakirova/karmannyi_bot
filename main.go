package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	productName  = "8–10 лет «Карманный воспитатель»"
	productPrice = "99 ₽"

	adminTelegramID int64 = 194229955

	channelID int64 = -1004292539378
)

// Глобальный экземпляр Telegram-бота.
// Он нужен webhookHandler, чтобы после успешной оплаты
// отправить покупателю сообщение с invite-ссылкой.
var botInstance *tgbotapi.BotAPI

// Пользователи, от которых бот сейчас ожидает e-mail.
var awaitingEmail = make(map[int64]bool)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println(".env не найден, используем переменные окружения")
	}

	// Инициализируем базу данных.
	if err := initDatabase(); err != nil {
		log.Fatal("Ошибка базы данных:", err)
	}

	defer db.Close()

	// Получаем токен Telegram-бота.
	token := os.Getenv("TELEGRAM_BOT_TOKEN")

	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN не установлен")
	}

	// Создаём Telegram-бота.
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	// Сохраняем экземпляр бота глобально,
	// чтобы его мог использовать webhookHandler.
	botInstance = bot

	log.Printf("Бот запущен: @%s", bot.Self.UserName)

	// Запускаем автоматическую проверку платежей ЮKassa.
	go startPaymentChecker(bot)

	// Запускаем получение обновлений Telegram.
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {

		// Обычное сообщение Telegram.
		if update.Message != nil {

			// Временная диагностика.
			// Если переслать сообщение из канала боту,
			// Telegram может показать ID канала.
			if update.Message.ForwardFromChat != nil {
				log.Printf(
					"ID канала: %d",
					update.Message.ForwardFromChat.ID,
				)
			}

			handleMessage(bot, update.Message)
		}

		// Нажатие inline-кнопки.
		if update.CallbackQuery != nil {
			handleButton(bot, update.CallbackQuery)
		}
	}
}

// ============================================================
// ОБРАБОТКА СООБЩЕНИЙ TELEGRAM
// ============================================================

func handleMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	log.Printf(
		"Пользователь: ID=%d, username=@%s",
		message.From.ID,
		message.From.UserName,
	)

	userID := message.From.ID

	// --------------------------------------------------------
	// Пользователь отправляет e-mail
	// --------------------------------------------------------

	if awaitingEmail[userID] {
		email := strings.TrimSpace(message.Text)

		parsedEmail, err := mail.ParseAddress(email)

		if err != nil || parsedEmail.Address != email {
			msg := tgbotapi.NewMessage(
				message.Chat.ID,
				"Похоже, это не e-mail. 😕\n\n"+
					"Пожалуйста, отправьте e-mail в формате:\n"+
					"example@mail.ru",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		// Создаём заказ после получения корректного e-mail.
		orderID, err := createOrder(userID)

		if err != nil {
			log.Println("Ошибка создания заказа:", err)

			msg := tgbotapi.NewMessage(
				message.Chat.ID,
				"Не удалось создать заказ. Попробуйте ещё раз.",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		// Сохраняем e-mail.
		if err := updateOrderEmail(orderID, email); err != nil {
			log.Println("Ошибка сохранения e-mail:", err)

			msg := tgbotapi.NewMessage(
				message.Chat.ID,
				"Не удалось сохранить e-mail. Попробуйте ещё раз.",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		// E-mail получен.
		delete(awaitingEmail, userID)

		// Создаём платёж ЮKassa.
		payment, err := createYooKassaPayment(
			orderID,
			email,
		)

		if err != nil {
			log.Println(
				"Ошибка создания платежа ЮKassa:",
				err,
			)

			msg := tgbotapi.NewMessage(
				message.Chat.ID,
				"Не удалось создать платёж. 😔\n\n"+
					"Попробуйте ещё раз через некоторое время.",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		// Сохраняем ID платежа ЮKassa.
		if err := updateYooKassaPaymentID(
			orderID,
			payment.ID,
		); err != nil {
			log.Println(
				"Ошибка сохранения payment_id:",
				err,
			)

			msg := tgbotapi.NewMessage(
				message.Chat.ID,
				"Платёж создан, но произошла техническая ошибка. "+
					"Пожалуйста, обратитесь в поддержку.",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		// Кнопка оплаты.
		button := tgbotapi.NewInlineKeyboardButtonURL(
			"💳 Оплатить 99 ₽",
			payment.Confirmation.ConfirmationURL,
		)

		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(button),
		)

		text := "Заказ создан ✅\n\n" +
			"Сумма: 99 ₽\n" +
			"E-mail: " + email + "\n\n" +
			"Нажмите кнопку ниже, чтобы перейти к оплате."

		msg := tgbotapi.NewMessage(
			message.Chat.ID,
			text,
		)

		msg.ReplyMarkup = keyboard

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	// --------------------------------------------------------
	// Команды
	// --------------------------------------------------------

	switch message.Text {

	case "/start":
		sendStartMessage(bot, message.Chat.ID)

	case "/buy":
		sendPurchaseMessage(bot, message.Chat.ID)

	case "/help":
		sendHelpMessage(bot, message.Chat.ID)

	case "/testlink":
		sendTestInviteLink(bot, message.Chat.ID)

	case "/status":
		sendStatusMessage(bot, message.Chat.ID)
	}
}

// ============================================================
// START
// ============================================================

func sendStartMessage(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	text := "Привет! 👋\n\n" +
		"Добро пожаловать в «Карманный воспитатель».\n\n" +
		"Закрытый канал «" + productName + "» — доступ к материалам для родителей детей 8–10 лет.\n\n" +
		"Стоимость доступа: " + productPrice + "\n\n" +
		"После успешной оплаты вы получите персональную ссылку для входа в закрытый канал."

	button := tgbotapi.NewInlineKeyboardButtonData(
		"Получить доступ",
		"buy",
	)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(button),
	)

	msg := tgbotapi.NewMessage(
		chatID,
		text,
	)

	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// ПОКУПКА
// ============================================================

func sendPurchaseMessage(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	text := "Доступ в закрытый канал\n\n" +
		"«" + productName + "»\n\n" +
		"Стоимость: " + productPrice + "\n\n" +
		"После оплаты бот автоматически проверит платёж и предоставит доступ к каналу."

	button := tgbotapi.NewInlineKeyboardButtonData(
		"Оплатить 99 ₽",
		"pay",
	)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(button),
	)

	msg := tgbotapi.NewMessage(
		chatID,
		text,
	)

	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// INLINE-КНОПКИ
// ============================================================

func handleButton(
	bot *tgbotapi.BotAPI,
	callback *tgbotapi.CallbackQuery,
) {
	switch callback.Data {

	case "buy":
		sendPurchaseMessage(
			bot,
			callback.Message.Chat.ID,
		)

	case "pay":

		// Теперь бот ждёт e-mail.
		awaitingEmail[callback.From.ID] = true

		msg := tgbotapi.NewMessage(
			callback.Message.Chat.ID,
			"📧 Для оформления оплаты введите ваш e-mail.\n\n"+
				"На него ЮKassa отправит электронный чек после оплаты.\n\n"+
				"Например:\n"+
				"example@mail.ru",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}
	}

	callbackResponse := tgbotapi.NewCallback(
		callback.ID,
		"",
	)

	if _, err := bot.Request(callbackResponse); err != nil {
		log.Println("Ошибка callback:", err)
	}
}

// ============================================================
// HELP
// ============================================================

func sendHelpMessage(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	text := "Помощь\n\n" +
		"Если у вас возникли вопросы с оплатой или доступом к каналу, воспользуйтесь командой /status или обратитесь в поддержку."

	msg := tgbotapi.NewMessage(
		chatID,
		text,
	)

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// STATUS
// ============================================================

func sendStatusMessage(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	userID := chatID

	order, err := getLastOrderByUserID(userID)

	if err != nil {
		if err == sql.ErrNoRows {
			msg := tgbotapi.NewMessage(
				chatID,
				"У вас пока нет заказов.\n\n"+
					"Нажмите /buy, чтобы оформить доступ.",
			)

			if _, err := bot.Send(msg); err != nil {
				log.Println("Ошибка отправки:", err)
			}

			return
		}

		log.Println("Ошибка получения заказа:", err)

		msg := tgbotapi.NewMessage(
			chatID,
			"Не удалось проверить статус заказа. Попробуйте ещё раз.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	if order.InviteLink != "" {
		msg := tgbotapi.NewMessage(
			chatID,
			"Оплата подтверждена ✅\n\n"+
				"Ваш доступ в канал:\n\n"+
				order.InviteLink,
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	switch order.Status {

	case "pending":
		msg := tgbotapi.NewMessage(
			chatID,
			"Ваш платёж ещё ожидает оплаты. ⏳\n\n"+
				"После оплаты доступ будет выдан автоматически.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

	case "paid":
		msg := tgbotapi.NewMessage(
			chatID,
			"Оплата получена ✅\n\n"+
				"Сейчас создаём вашу ссылку на канал. "+
				"Попробуйте /status через несколько секунд.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

	case "canceled":
		msg := tgbotapi.NewMessage(
			chatID,
			"Последний платёж был отменён.\n\n"+
				"Вы можете оформить новый заказ командой /buy.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

	default:
		msg := tgbotapi.NewMessage(
			chatID,
			"Статус заказа: "+order.Status,
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}
	}
}

// ============================================================
// WEBHOOK ЮKASSA
// ============================================================

func webhookHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"Method Not Allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	defer r.Body.Close()

	// Структура уведомления ЮKassa.
	var notification struct {
		Type  string `json:"type"`
		Event string `json:"event"`

		Object struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Paid   bool   `json:"paid"`
		} `json:"object"`
	}

	if err := json.NewDecoder(
		r.Body,
	).Decode(&notification); err != nil {

		log.Println(
			"Ошибка разбора webhook:",
			err,
		)

		http.Error(
			w,
			"Bad Request",
			http.StatusBadRequest,
		)

		return
	}

	log.Printf(
		"Webhook: type=%s event=%s payment_id=%s status=%s",
		notification.Type,
		notification.Event,
		notification.Object.ID,
		notification.Object.Status,
	)

	// Нас интересует только успешная оплата.
	if notification.Event != "payment.succeeded" {
		w.WriteHeader(http.StatusOK)
		return
	}

	paymentID := notification.Object.ID

	if paymentID == "" {
		log.Println("Webhook не содержит payment_id")

		http.Error(
			w,
			"Payment ID missing",
			http.StatusBadRequest,
		)

		return
	}

	// --------------------------------------------------------
	// 1. Проверяем платёж напрямую через API ЮKassa.
	// --------------------------------------------------------

	payment, err := getYooKassaPayment(paymentID)

	if err != nil {
		log.Println(
			"Не удалось проверить платёж:",
			err,
		)

		http.Error(
			w,
			"Payment verification failed",
			http.StatusInternalServerError,
		)

		return
	}

	if payment.Status != "succeeded" || !payment.Paid {
		log.Printf(
			"Платёж %s не подтверждён: status=%s paid=%v",
			paymentID,
			payment.Status,
			payment.Paid,
		)

		w.WriteHeader(http.StatusOK)
		return
	}

	// --------------------------------------------------------
	// 2. Ищем наш заказ по payment_id.
	// --------------------------------------------------------

	order, err := getOrderByPaymentID(paymentID)

	if err != nil {
		log.Println(
			"Заказ для payment_id не найден:",
			err,
		)

		http.Error(
			w,
			"Order not found",
			http.StatusInternalServerError,
		)

		return
	}

	log.Printf(
		"Найден заказ: order=%s user=%d status=%s",
		order.OrderID,
		order.TelegramUserID,
		order.Status,
	)

	// --------------------------------------------------------
	// 3. Защита от повторного webhook.
	// --------------------------------------------------------

	if order.InviteLink != "" {
		log.Printf(
			"Доступ уже выдан для заказа %s",
			order.OrderID,
		)

		w.WriteHeader(http.StatusOK)
		return
	}

	// --------------------------------------------------------
	// 4. Помечаем заказ как оплаченный.
	// --------------------------------------------------------

	if err := updateOrderStatus(
		order.OrderID,
		"paid",
	); err != nil {

		log.Println(
			"Ошибка обновления статуса заказа:",
			err,
		)

		http.Error(
			w,
			"Database error",
			http.StatusInternalServerError,
		)

		return
	}

	// --------------------------------------------------------
	// 5. Создаём персональную invite-ссылку.
	// --------------------------------------------------------

	inviteLink, err := createChannelInviteLink(
		botInstance,
	)

	if err != nil {
		log.Println(
			"Ошибка создания invite link:",
			err,
		)

		http.Error(
			w,
			"Invite link error",
			http.StatusInternalServerError,
		)

		return
	}

	// --------------------------------------------------------
	// 6. Сохраняем invite-ссылку в базе.
	// --------------------------------------------------------

	if err := updateInviteLink(
		order.OrderID,
		inviteLink,
	); err != nil {

		log.Println(
			"Ошибка сохранения invite link:",
			err,
		)

		http.Error(
			w,
			"Database error",
			http.StatusInternalServerError,
		)

		return
	}

	// --------------------------------------------------------
	// 7. Отправляем ссылку покупателю.
	// --------------------------------------------------------

	text := "Оплата получена! 🎉\n\n" +
		"Спасибо за покупку «Карманного воспитателя».\n\n" +
		"Ваш персональный доступ в закрытый канал:\n\n" +
		inviteLink + "\n\n" +
		"Ссылка предназначена только для вас."

	msg := tgbotapi.NewMessage(
		order.TelegramUserID,
		text,
	)

	if _, err := botInstance.Send(msg); err != nil {

		log.Println(
			"Ошибка отправки ссылки пользователю:",
			err,
		)

		// Важно:
		// платёж уже подтверждён,
		// а invite-ссылка уже сохранена в БД.
		// Поэтому здесь не возвращаем ошибку ЮKassa.
	}

	log.Printf(
		"ДОСТУП ВЫДАН: order=%s user=%d invite=%s",
		order.OrderID,
		order.TelegramUserID,
		inviteLink,
	)

	// Сообщаем ЮKassa, что webhook обработан.
	w.WriteHeader(http.StatusOK)
}

// ============================================================
// СОЗДАНИЕ INVITE-ССЫЛКИ TELEGRAM
// ============================================================

func createChannelInviteLink(
	bot *tgbotapi.BotAPI,
) (string, error) {

	config := tgbotapi.CreateChatInviteLinkConfig{
		ChatConfig: tgbotapi.ChatConfig{
			ChatID: channelID,
		},

		Name: "Покупка «Карманный воспитатель»",

		// Одна ссылка = максимум один вошедший пользователь.
		MemberLimit: 1,
	}

	response, err := bot.Request(config)

	if err != nil {
		return "", err
	}

	var inviteLink tgbotapi.ChatInviteLink

	if err := json.Unmarshal(
		response.Result,
		&inviteLink,
	); err != nil {

		return "",
			fmt.Errorf(
				"ошибка разбора ответа Telegram: %w",
				err,
			)
	}

	return inviteLink.InviteLink, nil
}

// ============================================================
// ТЕСТ INVITE-ССЫЛКИ
// ============================================================

func sendTestInviteLink(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	inviteLink, err := createChannelInviteLink(bot)

	if err != nil {

		log.Println(
			"Ошибка создания invite link:",
			err,
		)

		msg := tgbotapi.NewMessage(
			chatID,
			"Не удалось создать ссылку: "+err.Error(),
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	text := "Тестовая ссылка создана ✅\n\n" +
		"Вот ссылка на закрытый канал:\n" +
		inviteLink

	msg := tgbotapi.NewMessage(
		chatID,
		text,
	)

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// АВТОМАТИЧЕСКАЯ ПРОВЕРКА ОПЛАТ
// ============================================================

func startPaymentChecker(
	bot *tgbotapi.BotAPI,
) {
	log.Println("Проверка платежей ЮKassa запущена")

	// Проверяем сразу после запуска.
	checkPendingPayments(bot)

	// Затем каждые 10 секунд.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		checkPendingPayments(bot)
	}
}

func checkPendingPayments(
	bot *tgbotapi.BotAPI,
) {
	orders, err := getOrdersToCheck()

	if err != nil {
		log.Println(
			"Ошибка получения заказов:",
			err,
		)

		return
	}

	if len(orders) == 0 {
		return
	}

	log.Printf(
		"Проверяем платежей: %d",
		len(orders),
	)

	for _, order := range orders {

		if order.YooKassaPaymentID == "" {
			continue
		}

		payment, err := getYooKassaPayment(
			order.YooKassaPaymentID,
		)

		if err != nil {
			log.Printf(
				"Ошибка проверки платежа %s: %v",
				order.YooKassaPaymentID,
				err,
			)

			continue
		}

		log.Printf(
			"Заказ %s: payment=%s status=%s paid=%v",
			order.OrderID,
			order.YooKassaPaymentID,
			payment.Status,
			payment.Paid,
		)

		// --------------------------------------------------------
		// ПЛАТЁЖ УСПЕШЕН
		// --------------------------------------------------------

		if payment.Status == "succeeded" &&
			payment.Paid {

			processSuccessfulPayment(
				bot,
				&order,
			)

			continue
		}

		// --------------------------------------------------------
		// ПЛАТЁЖ ОТМЕНЁН
		// --------------------------------------------------------

		if payment.Status == "canceled" {

			if err := updateOrderStatus(
				order.OrderID,
				"canceled",
			); err != nil {

				log.Printf(
					"Ошибка обновления отменённого заказа %s: %v",
					order.OrderID,
					err,
				)
			}

			continue
		}
	}
}

// ============================================================
// ОБРАБОТКА УСПЕШНОЙ ОПЛАТЫ
// ============================================================

func processSuccessfulPayment(
	bot *tgbotapi.BotAPI,
	order *Order,
) {
	// Если ссылка уже существует — повторно ничего не создаём.
	if order.InviteLink != "" {
		return
	}

	log.Printf(
		"Оплата подтверждена: order=%s user=%d",
		order.OrderID,
		order.TelegramUserID,
	)

	// Помечаем заказ как оплаченный.
	if err := updateOrderStatus(
		order.OrderID,
		"paid",
	); err != nil {

		log.Printf(
			"Ошибка обновления статуса заказа %s: %v",
			order.OrderID,
			err,
		)

		return
	}

	// Создаём персональную ссылку.
	inviteLink, err := createChannelInviteLink(bot)

	if err != nil {
		log.Printf(
			"Не удалось создать invite-ссылку для заказа %s: %v",
			order.OrderID,
			err,
		)

		return
	}

	// Сохраняем ссылку.
	if err := updateInviteLink(
		order.OrderID,
		inviteLink,
	); err != nil {

		log.Printf(
			"Не удалось сохранить invite-ссылку для заказа %s: %v",
			order.OrderID,
			err,
		)

		return
	}

	// Отправляем пользователю.
	text := "Оплата получена! 🎉\n\n" +
		"Спасибо за покупку «Карманного воспитателя».\n\n" +
		"Ваш персональный доступ в закрытый канал:\n\n" +
		inviteLink + "\n\n" +
		"Нажмите на ссылку, чтобы присоединиться к каналу."

	msg := tgbotapi.NewMessage(
		order.TelegramUserID,
		text,
	)

	if _, err := bot.Send(msg); err != nil {
		log.Printf(
			"Ошибка отправки invite-ссылки пользователю %d: %v",
			order.TelegramUserID,
			err,
		)

		return
	}

	log.Printf(
		"ДОСТУП ВЫДАН: order=%s user=%d",
		order.OrderID,
		order.TelegramUserID,
	)
}
