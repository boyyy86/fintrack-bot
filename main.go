package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os" // Tambahkan package os ini
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Ambil kunci rahasia secara aman dari Cloud Server
var telegramToken = os.Getenv("TELEGRAM_TOKEN")
var groqAPIKey = os.Getenv("GROQ_API_KEY")
const groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

var activeModel = ""
var userMemory = make(map[int64][]map[string]string)

func temukanModelYangJalan() string {
	log.Println("1. Mengambil daftar model dari server Groq...")
	req, _ := http.NewRequest("GET", "https://api.groq.com/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Gagal koneksi ke Groq:", err)
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
		
		testReqBody, _ := json.Marshal(map[string]interface{}{
			"model": id,
			"messages": []map[string]string{{"role": "user", "content": "Hai"}},
			"max_tokens": 5,
		})
		
		tReq, _ := http.NewRequest("POST", groqEndpoint, bytes.NewBuffer(testReqBody))
		tReq.Header.Set("Content-Type", "application/json")
		tReq.Header.Set("Authorization", "Bearer "+groqAPIKey)
		
		tResp, tErr := client.Do(tReq)
		if tErr == nil {
			defer tResp.Body.Close()
			if tResp.StatusCode == 200 {
				log.Println("BERHASIL TEMBUS!")
				return id
			} else {
				log.Println("Ditolak/Error.")
			}
		}
	}
	return "llama-3.1-8b-instant"
}

func main() {
	activeModel = temukanModelYangJalan()
	log.Println("\n=============================================")
	log.Println("🤖 MODEL FINAL YANG DIPAKAI:", activeModel)
	log.Println("=============================================\n")

	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = false
    log.Printf("Sistem Online. Authorized on account %s", bot.Self.UserName)

    // === KODE DUMMY WEB SERVER DIMASUKKAN DI SINI ===
    go func() {
        http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
            w.Write([]byte("Fintrack Bot Sedang Menyala 24/7!"))
        })
        port := os.Getenv("PORT")
        if port == "" {
            port = "8080"
        }
        log.Println("Web server dummy berjalan di port:", port)
        http.ListenAndServe(":"+port, nil)
    }()
    // ================================================

    u := tgbotapi.NewUpdate(0)
    u.Timeout = 60
    updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			userText := update.Message.Text
			chatID := update.Message.Chat.ID
			log.Printf("[%s] %s", update.Message.From.UserName, userText)

			// 1. FITUR PESAN SAMBUTAN (ONBOARDING)
			if userText == "/start" || userText == "/help" {
				welcomeMsg := `Halo! 👋 Saya adalah *Asisten Keuangan Pribadi (Financial Planner)* Anda yang terintegrasi dengan AI.

Saya bertugas memantau arus kas Anda, menghitung persentase pengeluaran harian, dan memberikan peringatan zona kelayakan finansial (🟢 Aman, 🟡 Waspada, 🔴 Kritis).

📖 *PANDUAN PERINTAH DASAR:*
📥 */masuk [item] [nominal]* - Mencatat saldo awal / pemasukan baru (Misal: /masuk Saldo Awal 500000)
📤 */catat [item] [nominal]* - Mencatat pengeluaran (Misal: /catat Beli Makan 50000)
📊 */laporan* - Menampilkan ringkasan buku besar saat ini.
💡 */evaluasi* - Minta AI menganalisis kesehatan keuangan Anda.
🔄 */reset* - Menghapus seluruh memori untuk memulai pembukuan dari nol.

Mari kita mulai! Silakan ketik perintah */masuk* untuk mengatur saldo awal Anda.`
				
				msg := tgbotapi.NewMessage(chatID, welcomeMsg)
				msg.ParseMode = "Markdown"
				bot.Send(msg)
				continue
			}

			// 2. FITUR RESET DATA MANUAL
			if strings.HasPrefix(userText, "/reset") {
				delete(userMemory, chatID)
				msg := tgbotapi.NewMessage(chatID, "🔄 *Data buku besar telah dikosongkan. Saldo kembali Rp 0.*")
				msg.ParseMode = "Markdown"
				bot.Send(msg)
				continue
			}

			// 3. JIKA BUKAN PERINTAH SISTEM, KIRIM KE AI
			aiResponse := panggilAI(userText, chatID)

			msg := tgbotapi.NewMessage(chatID, aiResponse)
			msg.ParseMode = "Markdown"
			bot.Send(msg)
		}
	}
}

func panggilAI(input string, chatID int64) string {
	systemPrompt := `Anda adalah Asisten Keuangan Pribadi (Financial Planner) yang sangat analitis, teliti, dan kritis.
Tugas Anda mencatat arus kas secara detail dan memberikan evaluasi pengeluaran secara proaktif menggunakan indikator zona warna.
WAJIB baca riwayat chat sebelumnya untuk MENGHITUNG secara presisi: Total Pemasukan, Total Pengeluaran, dan Sisa Saldo.

Setiap kali menerima pesan/perintah, balas HANYA dengan 3 blok ini tanpa basa-basi:

🧾 **[BUKTI PENCATATAN TRANSAKSI]**
* **Kategori** : [Tebak kategorinya: 🍔 Konsumsi / 🚗 Transport / 🛒 Belanja / 🎮 Hiburan / 💵 Pendapatan / 🚨 Darurat]
* **Item**     : [Nama barang/aktivitas]
* **Mutasi**   : [🟢 Masuk / 🔴 Keluar]
* **Nominal**  : Rp [Format Angka]

📊 **[BUKU BESAR BERJALAN]**
| Keterangan | Total Terakumulasi |
| :--- | :--- |
| 🟢 Total Pemasukan | Rp ... |
| 🔴 Total Pengeluaran | Rp ... |
| 💰 **SISA SALDO AKTIF** | **Rp ...** |

💡 **[ANALISIS & KONDISI FINANSIAL]**
[Buat 1 paragraf analisis mendalam. ANDA WAJIB MENGANDUNG 3 POIN INI: 
1. Hitung persentase pengeluaran terhadap total pemasukan. 
2. Tentukan status keuangan menggunakan aturan ini (Tuliskan secara tebal):
   - 🟢 **ZONA HIJAU (Aman)** jika pengeluaran 0% - 40%.
   - 🟡 **ZONA KUNING (Waspada)** jika pengeluaran 41% - 70%.
   - 🔴 **ZONA MERAH (Kritis)** jika pengeluaran > 70%.
3. Berikan nasihat berdasarkan zona tersebut. Jika masuk ZONA MERAH, Anda WAJIB MELARANG pengguna menggunakan uangnya lagi sama sekali, berikan teguran keras agar sisa saldo dibekukan! Gunakan bahasa Indonesia yang tegas dan peduli.]`

	history := userMemory[chatID]
	if len(history) == 0 {
		history = append(history, map[string]string{"role": "system", "content": systemPrompt})
	}
	
	history = append(history, map[string]string{"role": "user", "content": input})

	if len(history) > 15 {
		history = append(history[:1], history[len(history)-14:]...)
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": activeModel,
		"messages": history,
	})

	req, _ := http.NewRequest("POST", groqEndpoint, bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+groqAPIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "Error: Gagal menghubungi API Groq."
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "Gagal memproses data. AI sedang sibuk, coba lagi."
	}

	message := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	content := message["content"].(string)

	history = append(history, map[string]string{"role": "assistant", "content": content})
	userMemory[chatID] = history

	return content
}