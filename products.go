package main

// ============================================================
// КАТАЛОГ КУРСОВ
// ============================================================
//
// Цены и ID каналов заполнены. Если появятся новые курсы или
// изменится цена/канал — правь прямо здесь, в списке products,
// и перезапусти бота (пересборка не нужна, если менял только
// эти значения — но саму программу собрать всё равно придётся,
// т.к. Go компилирует исходники в бинарник).
//
// Как узнать ChannelID нового канала:
//   1. Добавь бота администратором в канал.
//   2. Перешли (forward) боту любое сообщение из этого канала.
//   3. В логах сервера (journalctl -u karmannyi-bot -f) появится
//      строка "ID канала: -100XXXXXXXXXX" — это и есть ChannelID.

type Product struct {
	// ID — короткий уникальный код, используется в callback_data
	// инлайн-кнопок Telegram и хранится в БД (колонка product_id).
	ID string

	// AgeSlug — код возрастной группы, объединяет 3 курса
	// (1 месяц / 6 месяцев / 1 год) в одну группу при выборе.
	AgeSlug  string
	AgeLabel string // "5–7 лет" — показывается пользователю

	DurationLabel string // "1 месяц" — показывается пользователю

	// Title — полное название, идёт в описание платежа в ЮKassa
	// и в чек (если чек включён на стороне ЮKassa).
	Title string

	PriceRub int // цена в рублях

	// ChannelID — числовой ID приватного Telegram-канала,
	// куда бот создаёт invite-ссылку после оплаты.
	ChannelID int64
}

var products = []Product{
	{ID: "5_7_1m", AgeSlug: "5_7", AgeLabel: "5–7 лет", DurationLabel: "1 месяц",
		Title: "5–7 лет «Карманный воспитатель» курс на 1 месяц",
		PriceRub: 99, ChannelID: -1004225103026},

	{ID: "5_7_6m", AgeSlug: "5_7", AgeLabel: "5–7 лет", DurationLabel: "6 месяцев",
		Title: "5–7 лет «Карманный воспитатель» курс на 6 месяцев",
		PriceRub: 499, ChannelID: -1003963109194},

	{ID: "5_7_1y", AgeSlug: "5_7", AgeLabel: "5–7 лет", DurationLabel: "1 год",
		Title: "5–7 лет «Карманный воспитатель» курс на 1 год",
		PriceRub: 799, ChannelID: -1003921773824},

	{ID: "8_10_1m", AgeSlug: "8_10", AgeLabel: "8–10 лет", DurationLabel: "1 месяц",
		Title: "8–10 лет «Карманный воспитатель» курс на 1 месяц",
		PriceRub: 99, ChannelID: -1004292539378},

	{ID: "8_10_6m", AgeSlug: "8_10", AgeLabel: "8–10 лет", DurationLabel: "6 месяцев",
		Title: "8–10 лет «Карманный воспитатель» курс на 6 месяцев",
		PriceRub: 499, ChannelID: -1003715993316},

	{ID: "8_10_1y", AgeSlug: "8_10", AgeLabel: "8–10 лет", DurationLabel: "1 год",
		Title: "8–10 лет «Карманный воспитатель» курс на 1 год",
		PriceRub: 799, ChannelID: -1003907693561},

	{ID: "11_13_1m", AgeSlug: "11_13", AgeLabel: "11–13 лет", DurationLabel: "1 месяц",
		Title: "11–13 лет «Карманный воспитатель» курс на 1 месяц",
		PriceRub: 99, ChannelID: -1003929573275},

	{ID: "11_13_6m", AgeSlug: "11_13", AgeLabel: "11–13 лет", DurationLabel: "6 месяцев",
		Title: "11–13 лет «Карманный воспитатель» курс на 6 месяцев",
		PriceRub: 499, ChannelID: -1004404775570},

	{ID: "11_13_1y", AgeSlug: "11_13", AgeLabel: "11–13 лет", DurationLabel: "1 год",
		Title: "11–13 лет «Карманный воспитатель» курс на 1 год",
		PriceRub: 799, ChannelID: -1004407457101},

	{ID: "14_17_1m", AgeSlug: "14_17", AgeLabel: "14–17 лет", DurationLabel: "1 месяц",
		Title: "14–17 лет «Карманный воспитатель» курс на 1 месяц",
		PriceRub: 99, ChannelID: -1003941429252},

	{ID: "14_17_6m", AgeSlug: "14_17", AgeLabel: "14–17 лет", DurationLabel: "6 месяцев",
		Title: "14–17 лет «Карманный воспитатель» курс на 6 месяцев",
		PriceRub: 499, ChannelID: -1004472851676},

	{ID: "14_17_1y", AgeSlug: "14_17", AgeLabel: "14–17 лет", DurationLabel: "1 год",
		Title: "14–17 лет «Карманный воспитатель» курс на 1 год",
		PriceRub: 799, ChannelID: -1004355407269},
}

// legacyProduct — заказы, созданные ДО появления каталога
// (в старой БД), не имеют product_id. Чтобы такие заказы
// не сломались при обработке оплаты, для них используется
// этот курс по умолчанию (тот же, что был единственным раньше:
// «8–10 лет», 1 месяц).
var legacyProduct = Product{
	ID:            "legacy_8_10",
	AgeSlug:       "8_10",
	AgeLabel:      "8–10 лет",
	DurationLabel: "",
	Title:         "8–10 лет «Карманный воспитатель»",
	PriceRub:      99,
	ChannelID:     -1004292539378,
}

// getProductByID возвращает курс по его ID.
// Если ID пустой или не найден — возвращает legacyProduct
// (для обратной совместимости со старыми заказами).
func getProductByID(id string) Product {
	for _, p := range products {
		if p.ID == id {
			return p
		}
	}

	return legacyProduct
}

// ageGroup — возрастная группа с её курсами, для меню выбора.
type ageGroup struct {
	Slug     string
	Label    string
	Products []Product
}

// ageGroups возвращает все возрастные группы в порядке,
// в котором курсы объявлены в products, без дублей.
func ageGroups() []ageGroup {
	var groups []ageGroup
	seen := make(map[string]int) // slug -> индекс в groups

	for _, p := range products {
		if idx, ok := seen[p.AgeSlug]; ok {
			groups[idx].Products = append(groups[idx].Products, p)
			continue
		}

		seen[p.AgeSlug] = len(groups)

		groups = append(groups, ageGroup{
			Slug:     p.AgeSlug,
			Label:    p.AgeLabel,
			Products: []Product{p},
		})
	}

	return groups
}

// productsForAgeSlug возвращает курсы конкретной возрастной группы.
func productsForAgeSlug(slug string) []Product {
	var result []Product

	for _, p := range products {
		if p.AgeSlug == slug {
			result = append(result, p)
		}
	}

	return result
}