package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/robfig/cron/v3"
	"github.com/wcharczuk/go-chart/v2"
	_ "modernc.org/sqlite"
)

// --- VARIABEL GLOBAL ---
var db *sql.DB
var groqAPIKey string
var bot *tgbotapi.BotAPI
var adminID int64

// --- 1. DATABASE & KEAMANAN ---
func initDB() {
	var err error
	db, err = sql.Open("sqlite", "data_keuangan.db")
	if err != nil {
		log.Fatal("Gagal membuka database: ", err)
	}

	// Skema tabel utama
	query := `
	CREATE TABLE IF NOT EXISTS transaksi (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		item TEXT,
		nominal INTEGER,
		kategori TEXT,
		is_masuk BOOLEAN,
		waktu DATETIME
	);`
	_, err = db.Exec(query)
	if err != nil {
		log.Fatal("Gagal membuat tabel:", err)
	}

	// Auto-Migrate: Tambahkan kolom kategori jika database dari versi lama
	db.Exec(`ALTER TABLE transaksi ADD COLUMN kategori TEXT DEFAULT 'Umum'`)
}

func CatatTransaksi(userID int64, item string, nominal int64, kategori string, isMasuk bool) error {
	query := `INSERT INTO transaksi (user_id, item, nominal, kategori, is_masuk, waktu) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := db.Exec(query, userID, item, nominal, kategori, isMasuk, time.Now())
	return err
}

func HitungSaldo(userID int64) (int64, int64, int64) {
	rows, err := db.Query(`SELECT is_masuk, nominal FROM transaksi WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0
	}
	defer rows.Close()

	var totalMasuk, totalKeluar int64
	for rows.Next() {
		var isMasuk bool
		var nominal int64
		if err := rows.Scan(&isMasuk, &nominal); err == nil {
			if isMasuk {
				totalMasuk += nominal
			} else {
				totalKeluar += nominal
			}
		}
	}
	return totalMasuk, totalKeluar, totalMasuk - totalKeluar
}

// --- 2. FITUR EKSPOR CSV ---
func EksporDataCSV(userID int64) ([]byte, error) {
	rows, err := db.Query(`SELECT waktu, is_masuk, kategori, item, nominal FROM transaksi WHERE user_id = ? ORDER BY waktu ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writer.Write([]string{"Tanggal", "Tipe", "Kategori", "Item", "Nominal"}) // Header

	for rows.Next() {
		var waktu time.Time
		var isMasuk bool
		var kategori, item string
		var nominal int64

		if err := rows.Scan(&waktu, &isMasuk, &kategori, &item, &nominal); err == nil {
			tipe := "Keluar"
			if isMasuk {
				tipe = "Masuk"
			}
			writer.Write([]string{
				waktu.Format("2006-01-02 15:04"),
				tipe,
				kategori,
				item,
				fmt.Sprintf("%d", nominal),
			})
		}
	}
	writer.Flush()
	return buf.Bytes(), nil
}

// --- 3. FITUR GRAFIK VISUAL ---
func BuatGrafikPengeluaran(userID int64) ([]byte, error) {
	rows, err := db.Query(`SELECT kategori, SUM(nominal) FROM transaksi WHERE user_id = ? AND is_masuk = false GROUP BY kategori`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chartValues []chart.Value
	for rows.Next() {
		var kategori string
		var total float64
		if err := rows.Scan(&kategori, &total); err == nil {
			chartValues = append(chartValues, chart.Value{Value: total, Label: kategori})
		}
	}

	if len(chartValues) == 0 {
		return nil, fmt.Errorf("Belum ada data pengeluaran")
	}

	pie := chart.PieChart{
		Width:  512,
		Height: 512,
		Values: chartValues,
	}

	var buf bytes.Buffer
	err = pie.Render(chart.PNG, &buf)
	return buf.Bytes(), err
}

// --- 4. OTAK NLP (NATURAL LANGUAGE PROCESSING) VIA GROQ ---
type NLPResult struct {
	Item     string `json:"item"`
	Nominal  int64  `json:"nominal"`
	Kategori string `json:"kategori"`
}

func EkstrakPesanDenganAI(pesan string) (NLPResult, error) {
	var hasil NLPResult
	prompt := fmt.Sprintf(`Ekstrak data pengeluaran berikut ke dalam format JSON. 
Data pengguna: "%s"
Hanya kembalikan JSON mentah tanpa markdown, tanpa penjelasan. Kategori yang tersedia: Makanan, Transportasi, Hiburan, Tagihan, Lainnya.
Format wajib: {"item": "nama barang", "nominal": angka_tanpa_titik, "kategori": "nama_kategori"}`, pesan)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": "llama-3.1-8b-instant",
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1, // Dibuat dingin agar tidak berhalusinasi teks
	})

	req, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return hasil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if msg, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				// Bersihkan jika AI masih bandel menyertakan markdown
				content = strings.TrimPrefix(content, "```json")
				content = strings.TrimPrefix(content, "```")
				content = strings.TrimSuffix(content, "```")

				err := json.Unmarshal([]byte(content), &hasil)
				return hasil, err
			}
		}
	}
	return hasil, fmt.Errorf("Gagal mengekstrak")
}

// Fungsi Evaluasi AI Lama (dipertahankan)
func panggilAIEvaluasi(prompt string) string {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": "llama-3.1-8b-instant",
		"messages": []map[string]string{
			{"role": "system", "content": "Kamu asisten keuangan tegas. Evaluasi status (Aman/Waspada/Kritis) dan beri saran 1 kalimat."},
			{"role": "user", "content": prompt},
		},
	})
	req, _ := http.NewRequest("POST", "[https://api.groq.com/openai/v1/chat/completions](https://api.groq.com/openai/v1/chat/completions)", bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "AI sedang sibuk."
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if msg, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				return content
			}
		}
	}
	return "Gagal merangkai teks evaluasi."
}

// --- 5. SCHEDULER PENGINGAT (CRON JOB) ---
func mulaiPengingatOtomatis() {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	c := cron.New(cron.WithLocation(loc))

	// Berdetak setiap hari jam 20:00 WIB
	c.AddFunc("0 20 * * *", func() {
		if adminID != 0 {
			pesan := "Malam! 🌙 Jangan lupa catat pengeluaranmu hari ini ya, sebelum beristirahat."
			bot.Send(tgbotapi.NewMessage(adminID, pesan))
		}
	})
	c.Start()
}

// --- FUNGSI UTAMA (MAIN LOOP) ---
func main() {
	// Fitur Keamanan Ekstrem: Anti-Crash (Goroutine Recover)
	defer func() {
		if r := recover(); r != nil {
			log.Println("Sistem memulihkan diri dari Crash fatal:", r)
		}
	}()

	groqAPIKey = os.Getenv("GROQ_API_KEY")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	adminIDStr := os.Getenv("ADMIN_CHAT_ID")

	if adminIDStr != "" {
		adminID, _ = strconv.ParseInt(adminIDStr, 10, 64)
	}

	initDB()
	log.Println("Database & Kategori Siap!")

	var err error
	bot, err = tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Panic("Gagal koneksi ke Telegram")
	}

	mulaiPengingatOtomatis()
	log.Printf("Bot %s siap dengan sistem NLP, Scheduler, dan Grafik!", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		pesanMasuk := update.Message.Text
		userID := update.Message.Chat.ID

		// Fitur Keamanan: Whitelist Chat ID
		if adminID != 0 && userID != adminID {
			bot.Send(tgbotapi.NewMessage(userID, "Maaf, bot ini terkunci untuk penggunaan pribadi."))
			continue
		}

		// Jika pengguna belum set ADMIN_CHAT_ID di env, kita tangkap dari chat pertama
		if adminID == 0 {
			adminID = userID
			log.Println("Admin ID terdaftar otomatis:", adminID)
		}

		// Rute Perintah
		if strings.HasPrefix(pesanMasuk, "/") {
			kata := strings.Fields(pesanMasuk)
			perintah := kata[0]

			switch perintah {
			case "/start":
				bot.Send(tgbotapi.NewMessage(userID, "Sistem Otonom Aktif.\n/masuk [nominal]\n/evaluasi\n/ekspor (Unduh CSV)\n/laporan (Grafik Pie)\nAtau cukup ketik cerita santai, misal: 'Tadi makan bakso 25rb'."))

			case "/masuk":
				if len(kata) < 2 {
					bot.Send(tgbotapi.NewMessage(userID, "Contoh: /masuk 1500000"))
					continue
				}
				nominal, _ := strconv.ParseInt(kata[1], 10, 64)
				CatatTransaksi(userID, "Pemasukan Saldo", nominal, "Pemasukan", true)
				bot.Send(tgbotapi.NewMessage(userID, fmt.Sprintf("✅ Dana Rp%d berhasil ditambahkan!", nominal)))

			case "/ekspor":
				fileBytes, err := EksporDataCSV(userID)
				if err != nil {
					bot.Send(tgbotapi.NewMessage(userID, "Gagal membuat file CSV."))
					continue
				}
				file := tgbotapi.FileBytes{Name: "Laporan_Keuangan.csv", Bytes: fileBytes}
				msg := tgbotapi.NewDocument(userID, file)
				msg.Caption = "Ini data mentahmu, silakan buka di Excel."
				bot.Send(msg)

			case "/laporan":
				bot.Send(tgbotapi.NewMessage(userID, "🎨 Sedang menggambar grafik pengeluaran..."))
				imgBytes, err := BuatGrafikPengeluaran(userID)
				if err != nil {
					bot.Send(tgbotapi.NewMessage(userID, "Belum cukup data untuk membuat grafik."))
					continue
				}
				photo := tgbotapi.FileBytes{Name: "grafik.png", Bytes: imgBytes}
				msg := tgbotapi.NewPhoto(userID, photo)
				msg.Caption = "Persentase Pengeluaran Berdasarkan Kategori"
				bot.Send(msg)

			case "/evaluasi":
				bot.Send(tgbotapi.NewMessage(userID, "⏳ Menghitung & meminta evaluasi AI..."))
				pemasukan, pengeluaran, sisa := HitungSaldo(userID)
				fakta := fmt.Sprintf("Data absolut: Masuk Rp%d, Keluar Rp%d, Sisa Rp%d.", pemasukan, pengeluaran, sisa)
				bot.Send(tgbotapi.NewMessage(userID, panggilAIEvaluasi(fakta)))

			default:
				bot.Send(tgbotapi.NewMessage(userID, "Perintah tidak dikenali."))
			}
			continue
		}

		// JIKA BUKAN PERINTAH "/", ANGGAP SEBAGAI INPUT BAHASA NATURAL NLP
		bot.Send(tgbotapi.NewMessage(userID, "🧠 AI sedang membedah pesanmu..."))

		hasilNLP, err := EkstrakPesanDenganAI(pesanMasuk)
		if err != nil || hasilNLP.Nominal == 0 {
			bot.Send(tgbotapi.NewMessage(userID, "Maaf, AI gagal menangkap nominal/kategori dari pesanmu. Coba bahasa yang lebih jelas."))
			continue
		}

		// Simpan hasil terjemahan AI ke SQLite
		CatatTransaksi(userID, hasilNLP.Item, hasilNLP.Nominal, hasilNLP.Kategori, false)

		balasan := fmt.Sprintf("✅ **Tercatat Otomatis**\nItem: %s\nNominal: Rp%d\nKategori: %s", hasilNLP.Item, hasilNLP.Nominal, hasilNLP.Kategori)
		bot.Send(tgbotapi.NewMessage(userID, balasan))
	}
}
