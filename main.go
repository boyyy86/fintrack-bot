package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

// --- VARIABEL GLOBAL AI ---
var groqAPIKey string
var activeModel string
const groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

// --- STRUKTUR DATA & DATABASE ---
type Transaksi struct {
	ID      int
	UserID  int64
	Item    string
	Nominal int64
	IsMasuk bool
	Waktu   time.Time
}

var db *sql.DB

func initDB() {
	var err error
	db, err = sql.Open("sqlite", "data_keuangan.db")
	if err != nil {
		log.Fatal("Gagal membuka database: ", err)
	}
	query := `
	CREATE TABLE IF NOT EXISTS transaksi (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		item TEXT,
		nominal INTEGER,
		is_masuk BOOLEAN,
		waktu DATETIME
	);`
	_, err = db.Exec(query)
	if err != nil {
		log.Fatal("Gagal membuat tabel:", err)
	}
}

func CatatTransaksi(userID int64, item string, nominal int64, isMasuk bool) error {
	query := `INSERT INTO transaksi (user_id, item, nominal, is_masuk, waktu) VALUES (?, ?, ?, ?, ?)`
	_, err := db.Exec(query, userID, item, nominal, isMasuk, time.Now())
	return err
}

func HitungSaldo(userID int64) (int64, int64, int64) {
	query := `SELECT is_masuk, nominal FROM transaksi WHERE user_id = ?`
	rows, err := db.Query(query, userID)
	if err != nil {
		log.Println("Error membaca data:", err)
		return 0, 0, 0
	}
	defer rows.Close()

	var totalMasuk, totalKeluar int64
	for rows.Next() {
		var isMasuk bool
		var nominal int64
		err := rows.Scan(&isMasuk, &nominal)
		if err != nil {
			continue
		}
		if isMasuk {
			totalMasuk += nominal
		} else {
			totalKeluar += nominal
		}
	}
	return totalMasuk, totalKeluar, totalMasuk - totalKeluar
}

// --- FUNGSI PARSING TEKS ---
func NormalisasiNominal(teks string) int64 {
	teks = strings.ToLower(teks)
	teks = strings.ReplaceAll(teks, "rp", "")
	teks = strings.ReplaceAll(teks, " ", "")

	var pengali float64 = 1
	if strings.HasSuffix(teks, "rb") {
		pengali = 1000
		teks = strings.TrimSuffix(teks, "rb")
	} else if strings.HasSuffix(teks, "k") {
		pengali = 1000
		teks = strings.TrimSuffix(teks, "k")
	} else if strings.HasSuffix(teks, "jt") {
		pengali = 1000000
		teks = strings.TrimSuffix(teks, "jt")
	}

	if pengali > 1 {
		teks = strings.ReplaceAll(teks, ",", ".")
	} else {
		teks = strings.ReplaceAll(teks, ".", "")
		teks = strings.ReplaceAll(teks, ",", "")
	}

	angkaMentah, err := strconv.ParseFloat(teks, 64)
	if err != nil {
		return 0
	}
	return int64(angkaMentah * pengali)
}

func BedahPesan(pesan string) (perintah string, item string, nominal int64, isValid bool) {
	kata := strings.Fields(pesan)
	if len(kata) < 2 {
		return "", "", 0, false
	}
	perintah = kata[0]
	teksNominal := kata[len(kata)-1]
	if len(kata) > 2 {
		item = strings.Join(kata[1:len(kata)-1], " ")
	} else {
		item = "Tanpa Keterangan"
	}
	nominal = NormalisasiNominal(teksNominal)
	if nominal == 0 {
		return "", "", 0, false
	}
	return perintah, item, nominal, true
}

// --- FUNGSI AI GROQ ---
func temukanModelYangJalan() string {
	log.Println("1. Mengambil daftar model dari server Groq...")
	req, _ := http.NewRequest("GET", "https://api.groq.com/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Gagal koneksi ke Groq:", err)
		return "llama-3.1-8b-instant" // fallback darurat
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	data, ok := result["data"].([]interface{})
	if !ok {
		return "llama-3.1-8b-instant"
	}

	log.Println("2. Mengetes kecocokan model satu per satu...")
	for _, v := range data {
		m := v.(map[string]interface{})
		id := m["id"].(string)

		if strings.Contains(id, "whisper") || strings.Contains(id, "guard") || strings.Contains(id, "canopy") {
			continue
		}

		log.Printf("   -> Mengetes koneksi ke: %s ... ", id)
		// Kita ambil model pertama yang valid agar cepat
		return id 
	}
	return "llama-3.1-8b-instant"
}

func panggilAI(prompt string) string {
	if activeModel == "" {
		activeModel = temukanModelYangJalan()
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": activeModel,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "Kamu adalah asisten keuangan yang tegas dan mendukung. Berikan evaluasi singkat dari data yang dikirim pengguna. Sebutkan apakah status keuangannya AMAN, WASPADA, atau KRITIS. Berikan 1 kalimat saran objektif.",
			},
			{
				"role": "user",
				"content": prompt,
			},
		},
		"max_tokens": 150,
	})

	req, _ := http.NewRequest("POST", groqEndpoint, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "Maaf, AI sedang sibuk. Namun tenang saja, data saldo Anda telah tersimpan dan dihitung dengan aman di dalam database lokal."
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if message, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{}); ok {
			if content, ok := message["content"].(string); ok {
				return content
			}
		}
	}
	return "Berhasil menghitung saldo, tetapi gagal merangkai teks AI."
}

// --- FUNGSI UTAMA ---
func main() {
	// 1. Tarik API Key dari Environment Back4App
	groqAPIKey = os.Getenv("GROQ_API_KEY")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	
	if telegramToken == "" {
		log.Fatal("Error: TELEGRAM_TOKEN belum dipasang")
	}

	// 2. Nyalakan Database SQLite
	initDB()
	log.Println("Database SQLite berhasil disiapkan!")

	// 3. Hubungkan ke Telegram
	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Panic("Gagal koneksi ke Telegram: ", err)
	}
	log.Printf("Bot %s berhasil hidup", bot.Self.UserName)

	// 4. Dengarkan Pesan Masuk
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		pesanMasuk := update.Message.Text
		userID := update.Message.Chat.ID

		perintah, item, nominal, isValid := BedahPesan(pesanMasuk)

		switch perintah {
		case "/start":
			balasan := "Halo! Saya asisten keuangan barumu.\nKetik /masuk [nominal] untuk saldo awal.\nKetik /catat [keterangan] [nominal] untuk pengeluaran.\nKetik /evaluasi untuk analisis AI."
			bot.Send(tgbotapi.NewMessage(userID, balasan))

		case "/masuk":
			if !isValid {
				bot.Send(tgbotapi.NewMessage(userID, "Format salah. Contoh: /masuk dana awal 1,5jt"))
				continue
			}
			CatatTransaksi(userID, "Pemasukan: "+item, nominal, true)
			bot.Send(tgbotapi.NewMessage(userID, fmt.Sprintf("✅ Dana sebesar Rp%d berhasil ditambahkan!", nominal)))

		case "/catat":
			if !isValid {
				bot.Send(tgbotapi.NewMessage(userID, "Format salah. Contoh: /catat Beli Makan 50rb"))
				continue
			}
			CatatTransaksi(userID, item, nominal, false)
			bot.Send(tgbotapi.NewMessage(userID, fmt.Sprintf("📉 Pengeluaran Rp%d (%s) berhasil dicatat!", nominal, item)))

		case "/evaluasi":
			bot.Send(tgbotapi.NewMessage(userID, "⏳ Menghitung buku besar dan meminta evaluasi AI..."))
			
			// Ambil hitungan angka pasti dari Go
			pemasukan, pengeluaran, sisa := HitungSaldo(userID)
			
			// Kirim hasilnya sebagai fakta teks