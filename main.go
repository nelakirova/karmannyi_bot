package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	adminTelegramID int64 = 194229955
)

// Глобальный экземпляр Telegram-бота.
// Используется в startPaymentChecker/processSuccessfulPayment,
// чтобы после подтверждения оплаты отправить покупателю
// сообщение с invite-ссылкой.
var botInstance *tgbotapi.BotAPI

// Пользователи, от которых бот сейчас ожидает e-mail —
// значение карты это ID выбранного курса (product.ID),
// чтобы после получения e-mail знать, за что создавать платёж.
var awaitingEmail = make(map[int64]string)

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
	//
	// Если задана переменная TELEGRAM_PROXY_URL — все запросы к Telegram
	// (включая long polling) идут через этот прокси. Это нужно, когда
	// сам бот работает на сервере в России, а Telegram из России
	// замедлен/ограничен Роскомнадзором: запросы к api.telegram.org
	// заворачиваются на маленький сервер за границей.
	//
	// Формат: http://логин:пароль@ip_прокси:порт
	// Если переменная не задана — бот стучится в Telegram напрямую
	// (обычное поведение, как раньше).
	bot, err := newTelegramBot(token)
	if err != nil {
		log.Fatal(err)
	}

	// Сохраняем экземпляр бота глобально,
	// чтобы его могли использовать фоновые функции
	// (startPaymentChecker / processSuccessfulPayment).
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
// СОЗДАНИЕ TELEGRAM-БОТА (С ПОДДЕРЖКОЙ ПРОКСИ)
// ============================================================

// newTelegramBot создаёт tgbotapi.BotAPI. Если задана переменная
// окружения TELEGRAM_PROXY_URL, все HTTP-запросы к Telegram
// (в том числе long polling) идут через указанный HTTP(S)-прокси.
// Формат значения: http://логин:пароль@ip_прокси:порт
func newTelegramBot(token string) (*tgbotapi.BotAPI, error) {
	proxyRaw := os.Getenv("TELEGRAM_PROXY_URL")

	if proxyRaw == "" {
		return tgbotapi.NewBotAPI(token)
	}

	proxyURL, err := url.Parse(proxyRaw)

	if err != nil {
		return nil, fmt.Errorf(
			"некорректный TELEGRAM_PROXY_URL: %w",
			err,
		)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		// Long polling ждёт обновления до 60 секунд (см. u.Timeout
		// ниже) — таймаут клиента должен быть заведомо больше.
		Timeout: 90 * time.Second,
	}

	log.Println("Telegram: используется прокси", proxyURL.Host)

	return tgbotapi.NewBotAPIWithClient(
		token,
		tgbotapi.APIEndpoint,
		client,
	)
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
	// Пользователь отправляет e-mail для чека
	// --------------------------------------------------------

	if productID, ok := awaitingEmail[userID]; ok {
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

		delete(awaitingEmail, userID)

		startCheckout(
			bot,
			message.Chat.ID,
			userID,
			productID,
			email,
		)

		return
	}

	// --------------------------------------------------------
	// Команды
	// --------------------------------------------------------

	switch {

	case message.Text == "/start":
		sendStartMessage(bot, message.Chat.ID)

	case message.Text == "/buy":
		sendAgeGroupMenu(bot, message.Chat.ID)

	case message.Text == "/help":
		sendHelpMessage(bot, message.Chat.ID)

	case strings.HasPrefix(message.Text, "/testlink"):
		productID := strings.TrimSpace(
			strings.TrimPrefix(message.Text, "/testlink"),
		)
		sendTestInviteLink(bot, message.Chat.ID, productID)

	case message.Text == "/status":
		sendStatusMessage(bot, message.Chat.ID)

	case message.Text == "/relink":
		sendFreshInviteLink(bot, message.Chat.ID, message.From.ID)
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
		"У нас есть закрытые каналы с материалами для родителей — " +
		"подобраны по возрасту ребёнка, на разный срок доступа.\n\n" +
		"Выберите возраст ребёнка, чтобы посмотреть варианты и оформить доступ."

	button := tgbotapi.NewInlineKeyboardButtonData(
		"Получить доступ",
		"choose_age",
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
// ШАГ 1: ВЫБОР ВОЗРАСТА
// ============================================================

func sendAgeGroupMenu(
	bot *tgbotapi.BotAPI,
	chatID int64,
) {
	text := "Выберите возраст ребёнка:"

	var rows [][]tgbotapi.InlineKeyboardButton

	for _, group := range ageGroups() {
		button := tgbotapi.NewInlineKeyboardButtonData(
			group.Label,
			"age:"+group.Slug,
		)

		rows = append(
			rows,
			tgbotapi.NewInlineKeyboardRow(button),
		)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// ШАГ 2: ВЫБОР СРОКА ДОСТУПА
// ============================================================

func sendDurationMenu(
	bot *tgbotapi.BotAPI,
	chatID int64,
	ageSlug string,
) {
	items := productsForAgeSlug(ageSlug)

	if len(items) == 0 {
		msg := tgbotapi.NewMessage(
			chatID,
			"Такой возрастной группы не найдено. Попробуйте /buy ещё раз.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	text := "«" + items[0].AgeLabel + "» — выберите срок доступа:"

	var rows [][]tgbotapi.InlineKeyboardButton

	for _, p := range items {
		label := fmt.Sprintf(
			"%s — %d ₽",
			p.DurationLabel,
			p.PriceRub,
		)

		button := tgbotapi.NewInlineKeyboardButtonData(
			label,
			"buy:"+p.ID,
		)

		rows = append(
			rows,
			tgbotapi.NewInlineKeyboardRow(button),
		)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

// ============================================================
// ШАГ 3: ОФОРМЛЕНИЕ ЗАКАЗА И СОЗДАНИЕ ПЛАТЕЖА
// ============================================================

func startCheckout(
	bot *tgbotapi.BotAPI,
	chatID int64,
	userID int64,
	productID string,
	email string,
) {
	product := getProductByID(productID)

	orderID, err := createOrder(userID, product.ID, product.PriceRub)

	if err != nil {
		log.Println("Ошибка создания заказа:", err)

		msg := tgbotapi.NewMessage(
			chatID,
			"Не удалось создать заказ. Попробуйте ещё раз.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	if err := updateOrderEmail(orderID, email); err != nil {
		log.Println("Ошибка сохранения e-mail:", err)
		// Не критично для оплаты — продолжаем.
	}

	payment, err := createYooKassaPayment(orderID, product, email)

	if err != nil {
		log.Println("Ошибка создания платежа ЮKassa:", err)

		msg := tgbotapi.NewMessage(
			chatID,
			"Не удалось создать платёж. 😔\n\n"+
				"Попробуйте ещё раз через некоторое время.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	if err := updateYooKassaPaymentID(orderID, payment.ID); err != nil {
		log.Println("Ошибка сохранения payment_id:", err)

		msg := tgbotapi.NewMessage(
			chatID,
			"Платёж создан, но произошла техническая ошибка. "+
				"Пожалуйста, обратитесь в поддержку.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	button := tgbotapi.NewInlineKeyboardButtonURL(
		fmt.Sprintf("💳 Оплатить %d ₽", product.PriceRub),
		payment.Confirmation.ConfirmationURL,
	)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(button),
	)

	text := "Заказ создан ✅\n\n" +
		"Курс: " + product.Title + "\n" +
		fmt.Sprintf("Сумма: %d ₽\n", product.PriceRub) +
		"E-mail для чека: " + email + "\n\n" +
		"Нажмите кнопку ниже, чтобы перейти к оплате."

	msg := tgbotapi.NewMessage(chatID, text)
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
	data := callback.Data

	switch {

	case data == "choose_age":
		sendAgeGroupMenu(bot, callback.Message.Chat.ID)

	case strings.HasPrefix(data, "age:"):
		ageSlug := strings.TrimPrefix(data, "age:")
		sendDurationMenu(bot, callback.Message.Chat.ID, ageSlug)

	case strings.HasPrefix(data, "buy:"):
		productID := strings.TrimPrefix(data, "buy:")

		// Запоминаем выбранный курс и просим e-mail —
		// он обязателен для чека (54-ФЗ, требование ЮKassa).
		awaitingEmail[callback.From.ID] = productID

		msg := tgbotapi.NewMessage(
			callback.Message.Chat.ID,
			"📧 Остался последний шаг — укажите e-mail для чека.\n\n"+
				"ЮKassa отправит на него электронную квитанцию "+
				"после оплаты.\n\n"+
				"Например:\nexample@mail.ru",
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
		"/status — статус вашего последнего заказа\n" +
		"/relink — получить новую ссылку на канал, если старая не сработала (истекла, была использована и т.п.)\n\n" +
		"Если у вас возникли вопросы с оплатой или доступом к каналу, обратитесь в поддержку."

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
		product := getProductByID(order.ProductID)

		msg := tgbotapi.NewMessage(
			chatID,
			"Оплата подтверждена ✅\n\n"+
				"Курс: "+product.Title+"\n\n"+
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
// ПЕРЕВЫПУСК INVITE-ССЫЛКИ (/relink)
// ============================================================
//
// Если у пользователя оплаченный заказ, но старая ссылка почему-то
// не сработала (истекла, была отозвана, technical glitch и т.п.) —
// эта команда создаёт НОВУЮ ссылку для того же заказа, без повторной
// оплаты. Полезно и как самопомощь пользователю, и как способ
// проверить, воспроизводится ли проблема на свежей ссылке.

func sendFreshInviteLink(
	bot *tgbotapi.BotAPI,
	chatID int64,
	userID int64,
) {
	order, err := getLastOrderByUserID(userID)

	if err != nil {
		msg := tgbotapi.NewMessage(
			chatID,
			"Заказов пока нет. Оформите доступ командой /buy.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	if order.Status != "paid" {
		msg := tgbotapi.NewMessage(
			chatID,
			"Команда доступна только для оплаченных заказов.\n\n"+
				"Проверьте статус: /status",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	product := getProductByID(order.ProductID)

	inviteLink, err := createChannelInviteLink(bot, product.ChannelID)

	if err != nil {
		log.Printf(
			"Не удалось перевыпустить invite-ссылку для заказа %s: %v",
			order.OrderID,
			err,
		)

		msg := tgbotapi.NewMessage(
			chatID,
			"Не удалось создать новую ссылку. Попробуйте ещё раз "+
				"через некоторое время или обратитесь в поддержку.",
		)

		if _, err := bot.Send(msg); err != nil {
			log.Println("Ошибка отправки:", err)
		}

		return
	}

	if err := updateInviteLink(order.OrderID, inviteLink); err != nil {
		log.Printf(
			"Не удалось сохранить новую invite-ссылку для заказа %s: %v",
			order.OrderID,
			err,
		)
	}

	text := "Новая ссылка создана ✅\n\n" +
		"Курс: " + product.Title + "\n\n" +
		"Ссылка действительна 24 часа и одноразовая — " +
		"переходите сразу:\n\n" +
		inviteLink

	msg := tgbotapi.NewMessage(chatID, text)

	if _, err := bot.Send(msg); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}

func createChannelInviteLink(
	bot *tgbotapi.BotAPI,
	channelID int64,
) (string, error) {

	config := tgbotapi.CreateChatInviteLinkConfig{
		ChatConfig: tgbotapi.ChatConfig{
			ChatID: channelID,
		},

		Name: "Покупка «Карманный воспитатель»",

		// Одна ссылка = максимум один вошедший пользователь.
		MemberLimit: 1,

		// Явный срок действия — 24 часа с момента создания.
		// Раньше это поле не задавалось вовсе (ссылка не истекала
		// по времени), что не совпадало с ожиданиями и затрудняло
		// диагностику проблем со ссылками.
		ExpireDate: int(time.Now().Add(24 * time.Hour).Unix()),
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

// productID можно передать аргументом команды, например:
// "/testlink 8_10_1m". Если аргумент не указан — тестируется
// legacyProduct (тот же канал, что был единственным раньше).
func sendTestInviteLink(
	bot *tgbotapi.BotAPI,
	chatID int64,
	productID string,
) {
	product := getProductByID(productID)

	inviteLink, err := createChannelInviteLink(bot, product.ChannelID)

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
		"Курс: " + product.Title + "\n" +
		"Ссылка на канал:\n" +
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

	// Определяем, к какому каналу нужен доступ.
	product := getProductByID(order.ProductID)

	// Создаём персональную ссылку.
	inviteLink, err := createChannelInviteLink(bot, product.ChannelID)

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
		"Спасибо за покупку курса «" + product.Title + "».\n\n" +
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