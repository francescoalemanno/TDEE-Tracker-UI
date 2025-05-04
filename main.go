package main

import (
	"embed"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"time"
)

const appVersion = "1.2.0"

type AppConfig struct {
	Port       int
	LogsFile   string
	ParamsFile string
	NoAutoOpen bool
}

func parseFlags() AppConfig {
	port := flag.Int("port", 8080, "Port to serve application on")
	logsFile := flag.String("logs", "", "Path to logs CSV file (overrides TDEE_LOGS_FILE env variable)")
	paramsFile := flag.String("params", "", "Path to parameters JSON file (overrides TDEE_PARAMS_FILE env variable)")
	noAutoOpen := flag.Bool("no-browser", false, "Don't automatically open browser")

	// Add help text
	flag.Usage = func() {
		fmt.Println("TDEE Tracker - Track your TDEE and weight")
		fmt.Println("\nUsage:")
		flag.PrintDefaults()
		fmt.Println("\nEnvironment variables:")
		fmt.Println("  TDEE_LOGS_FILE    Path to logs CSV file")
		fmt.Println("  TDEE_PARAMS_FILE  Path to parameters JSON file")
	}

	flag.Parse()

	return AppConfig{
		Port:       *port,
		LogsFile:   *logsFile,
		ParamsFile: *paramsFile,
		NoAutoOpen: *noAutoOpen,
	}
}

func findOrCreateFile(envVar, defaultName string, defaultContent []byte, overridePath string) string {
	// Check command-line override first

	if overridePath != "" {
		log.Println(defaultName, "loaded from", overridePath, "(command-line option)")
		return overridePath
	}

	// Rest of function remains the same...
	// 1. Check ENV
	if path := os.Getenv(envVar); path != "" {
		if _, err := os.Stat(path); err == nil {
			log.Println(defaultName, "loaded from", path)
			return path
		}
	}

	// 2. Check binary directory
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		path := filepath.Join(dir, defaultName)
		if _, err := os.Stat(path); err == nil {
			log.Println(defaultName, "loaded from", path)
			return path
		}
	}

	// 3. Check or create in home config
	if home, err := os.UserHomeDir(); err == nil {
		configDir := filepath.Join(home, ".config", "tdee-tracker")
		os.MkdirAll(configDir, 0755) // ensure directory exists

		fullPath := filepath.Join(configDir, defaultName)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			os.WriteFile(fullPath, defaultContent, 0644)
		}
		log.Println(defaultName, "loaded from", fullPath)
		return fullPath
	}

	// fallback
	log.Println(defaultName, "loaded from", defaultName)
	return defaultName
}

type LogEntry struct {
	Date   time.Time
	Weight float64
	Cals   float64
}

type Estimate struct {
	Date   time.Time
	Weight float64
	TDEE   float64
	SDw    float64
	SDtdee float64
}

type Params struct {
	InitialTDEE     float64
	CalPerFatKg     float64
	RsdTDEE         float64
	RsdObsCal       float64
	RsdObsWeight    float64
	RsdWeight       float64
	PfVarianceBoost float64
	GoalWeight      float64
}

var layout = "2006-01-02"

func loadLog() ([]LogEntry, error) {
	file, err := os.OpenFile(csvFile, os.O_CREATE|os.O_RDONLY, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	r := csv.NewReader(file)
	rows, _ := r.ReadAll()
	var entries []LogEntry
	for _, row := range rows {
		if len(row) != 3 {
			continue
		}
		t, err := time.Parse(layout, row[0])
		if err != nil {
			continue
		}
		w, _ := strconv.ParseFloat(row[1], 64)
		c, _ := strconv.ParseFloat(row[2], 64)
		entries = append(entries, LogEntry{Date: t, Weight: w, Cals: c})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Date.Before(entries[j].Date)
	})
	return entries, nil
}

func saveLog(entries []LogEntry) error {
	file, err := os.Create(csvFile)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	for _, e := range entries {
		w.Write([]string{e.Date.Format(layout), fmt.Sprintf("%.2f", e.Weight), fmt.Sprintf("%.0f", e.Cals)})
	}
	w.Flush()
	return w.Error()
}

func updateEntry(entries []LogEntry, newEntry LogEntry) ([]LogEntry, error) {
	for i, e := range entries {
		if e.Date.Equal(newEntry.Date) {
			entries[i] = newEntry
			return entries, nil
		}
	}
	return append(entries, newEntry), nil
}

func pf_m(E []LogEntry, P Params) []Estimate {
	if len(E) == 0 {
		return nil
	}
	mk := E[0].Cals
	if P.InitialTDEE > 0 {
		mk = P.InitialTDEE
	}
	wk := E[0].Weight
	kfat := P.CalPerFatKg
	kfat2 := kfat * kfat

	vm := math.Pow(mk*P.RsdTDEE, 2) + math.Pow(mk*P.RsdObsCal, 2)
	vw := math.Pow(wk*P.RsdObsWeight, 2) + math.Pow(wk*P.RsdWeight, 2)

	result := []Estimate{{
		Date:   E[0].Date,
		Weight: wk,
		TDEE:   mk,
		SDw:    math.Sqrt(vw),
		SDtdee: math.Sqrt(vm),
	}}

	for i := 1; i < len(E); i++ {
		dt := E[i].Date.Sub(E[i-1].Date).Hours() / 24.0
		dt2 := dt * dt
		c := E[i-1].Cals
		wo := E[i].Weight
		vwo := math.Pow(wo*P.RsdObsWeight, 2)

		vm += math.Pow(c*P.RsdTDEE, 2)
		vw += math.Pow(wo*P.RsdWeight, 2) + math.Pow(c*dt/kfat*P.RsdObsCal, 2)

		var mp, wp, vmp, vwp, Vresw float64
		for it := 0; it < 25; it++ {
			denom := dt2*vm + kfat2*(Vresw+vw+vwo)
			mp = (kfat2*mk*(Vresw+vw+vwo) + dt*vm*(c*dt+kfat*(wk-wo))) / denom
			wp = (kfat*(Vresw+vwo)*(c*dt-dt*mk+kfat*wk) + (dt2*vm+kfat2*vw)*wo) / denom
			vmp = (kfat2 * vm * (Vresw + vw + vwo)) / denom
			vwp = ((dt2*vm + kfat2*vw) * (Vresw + vwo)) / denom
			Vresw = math.Pow(wo-wp, 2) * P.PfVarianceBoost
		}

		mk = mp
		wk = wp
		vm = vmp
		vw = vwp

		result = append(result, Estimate{
			Date:   E[i].Date,
			Weight: wk,
			TDEE:   mk,
			SDw:    math.Sqrt(vw),
			SDtdee: math.Sqrt(vm),
		})
	}
	return result
}
func alpha_beta(x0, v0, t0 float64) (func(float64, float64, float64), func(float64) [2]float64) {
	xk := x0
	vk := v0
	tk := t0
	feed := func(T, X, alpha float64) {
		beta := 2.0*(2.0-alpha) - 4.0*math.Sqrt(1.0-alpha)
		dt := T - tk
		if dt < 0.01 {
			return
		}
		xpred := xk + vk*dt
		z := X - xpred
		xk += alpha * z
		vk += beta * z / dt
		tk += dt
	}
	state := func(T float64) [2]float64 {
		return [2]float64{xk + (T-tk)*vk, vk}
	}
	return feed, state
}
func alpha_beta_tdee(E []LogEntry, P Params) []Estimate {
	if len(E) == 0 {
		return nil
	}

	alpha := 1 / 16.0

	cal_feed, cal_obs := alpha_beta(E[0].Cals, 0.0, 0.0)
	w_feed, w_obs := alpha_beta(E[0].Weight, 0.0, 0.0)

	result := []Estimate{Estimate{
		Date:   E[0].Date,
		Weight: w_obs(0.0)[0],
		TDEE:   cal_obs(0.0)[0] - w_obs(0.0)[1]*P.CalPerFatKg,
		SDw:    math.Abs(P.RsdObsWeight * w_obs(0.0)[0]),
		SDtdee: math.Hypot(P.RsdObsWeight*w_obs(0.0)[1]*P.CalPerFatKg, P.RsdObsCal*cal_obs(0.0)[0]),
	}}

	for i := 1; i < len(E); i++ {
		dt := E[i].Date.Sub(E[0].Date).Hours() / 24.0
		c := E[i-1].Cals
		wo := E[i].Weight

		cal_feed(dt, c, alpha)
		w_feed(dt, wo, alpha)

		result = append(result, Estimate{
			Date:   E[i].Date,
			Weight: w_obs(dt)[0],
			TDEE:   cal_obs(dt)[0] - w_obs(dt)[1]*P.CalPerFatKg,
			SDw:    math.Abs(P.RsdObsWeight * w_obs(dt)[0]),
			SDtdee: math.Hypot(P.RsdObsWeight*w_obs(dt)[1]*P.CalPerFatKg, P.RsdObsCal*cal_obs(dt)[0]),
		})
	}

	return result
}

func handleLog(w http.ResponseWriter, r *http.Request) {
	entries, _ := loadLog()
	err := r.ParseForm()
	if err == nil && r.Method == http.MethodPost {
		dateStr := r.FormValue("date")
		weightStr := r.FormValue("weight")
		calsStr := r.FormValue("cals")

		t, err := time.Parse(layout, dateStr)
		if err == nil {
			wgt, _ := strconv.ParseFloat(weightStr, 64)
			cals, _ := strconv.ParseFloat(calsStr, 64)
			entry := LogEntry{Date: t, Weight: wgt, Cals: cals}
			entries, _ = updateEntry(entries, entry)
			saveLog(entries)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	params, _ := loadParams()

	estimates := alpha_beta_tdee(entries, params)

	var goalMsg string
	if len(estimates) > 0 {
		latest := estimates[len(estimates)-1]
		_, goalMsg = goalAdvice(latest.Weight, params.GoalWeight, latest.TDEE, params.CalPerFatKg)
	}
	tmpl.ExecuteTemplate(w, "index.html", struct {
		Entries    []LogEntry
		Estimates  []Estimate
		GoalAdvice string
		Version    string
	}{entries, estimates, goalMsg, appVersion})
}

//go:embed templates
var templateFS embed.FS

var tmpl *template.Template

func loadTemplates() {
	// Per caricare tutti i template da una directory
	tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"reverse": func(entries []LogEntry) []int {
			idx := make([]int, len(entries))
			for i := range entries {
				idx[i] = len(entries) - 1 - i
			}
			return idx
		},
		"recent": func(entries []Estimate) []Estimate {
			return entries[max(0, len(entries)-31):]
		},
		"recententries": func(entries []LogEntry) []LogEntry {
			return entries[max(0, len(entries)-31):]
		},
		"last_estimate": func(entries []Estimate) Estimate {
			e := Estimate{}
			if len(entries) > 0 {
				e = entries[len(entries)-1]
			}
			return e
		},
		"defaults": func(entries []LogEntry) [3]string {
			res := [3]string{time.Now().Format(layout), "", ""}
			if len(entries) > 0 {
				e := entries[len(entries)-1]
				if e.Date.Format(layout) == res[0] {
					res[1] = fmt.Sprintf("%.2f", e.Weight)
					res[2] = fmt.Sprintf("%.2f", e.Cals)
				}
			}
			return res
		},
	}).ParseFS(templateFS, "templates/*.html"))
}

func loadParams() (Params, error) {
	var p Params
	file, err := os.Open(paramsFile)
	if err != nil {
		// fallback to defaults

		return Params{
			InitialTDEE:     -1,
			CalPerFatKg:     7700,
			RsdTDEE:         0.01,
			RsdObsCal:       0.1,
			RsdObsWeight:    0.004,
			RsdWeight:       0.0001,
			PfVarianceBoost: 1.0 / 6.0,
		}, nil
	}
	defer file.Close()
	err = json.NewDecoder(file).Decode(&p)
	return p, err
}

func saveParams(p Params) error {
	file, err := os.Create(paramsFile)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(p)
}
func handleSettings(w http.ResponseWriter, r *http.Request) {
	p, _ := loadParams()

	if r.Method == http.MethodPost {
		r.ParseForm()
		parse := func(key string) float64 {
			v, _ := strconv.ParseFloat(r.FormValue(key), 64)
			return v
		}
		p = Params{
			InitialTDEE:     parse("InitialTDEE"),
			CalPerFatKg:     parse("CalPerFatKg"),
			RsdTDEE:         parse("RsdTDEE"),
			RsdObsCal:       parse("RsdObsCal"),
			RsdObsWeight:    parse("RsdObsWeight"),
			RsdWeight:       parse("RsdWeight"),
			PfVarianceBoost: parse("PfVarianceBoost"),
			GoalWeight:      parse("GoalWeight"),
		}
		saveParams(p)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tmpl.ExecuteTemplate(w, "settings.html", struct {
		Params
		Version string
	}{p, appVersion})
}

//go:embed style.css
var raw_css_data string

//go:embed chart.min.js
var chartJsCode string

func serveCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write([]byte(raw_css_data))
}
func serveChartJs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write([]byte(chartJsCode))
}
func goalAdvice(current, goal, tdee, calPerKg float64) (float64, string) {
	if goal <= 0 || calPerKg <= 0 {
		return 0, "Set a valid goal weight."
	}
	direction := "lose"
	if goal > current {
		direction = "gain"
	}
	if math.Abs(current-goal) < 0.1 {
		direction = "mantain"
	}

	delta := min(max(goal-current, -1.0), 1.0) * 500.0
	rate := math.Abs(delta * 7.0 / calPerKg)
	return tdee + delta, fmt.Sprintf("To %v weight (~%.2fkg/week), eat about %.0f kcal/day.", direction, rate, tdee+delta)
}

var (
	defaultParams = []byte(`{
		"InitialTDEE": -1,
		"CalPerFatKg": 7700,
		"RsdTDEE": 0.01,
		"RsdObsCal": 0.1,
		"RsdObsWeight": 0.004,
		"RsdWeight": 0.0001,
		"PfVarianceBoost": 0.0833,
		"GoalWeight": -1
	}`)

	// These will be initialized in main()
	csvFile    string
	paramsFile string
)

func main() {
	loadTemplates()
	// Parse command-line flags
	config := parseFlags()

	// Initialize file paths with command-line options
	csvFile = findOrCreateFile("TDEE_LOGS_FILE", "logs.csv", []byte{}, config.LogsFile)
	paramsFile = findOrCreateFile("TDEE_PARAMS_FILE", "params.json", defaultParams, config.ParamsFile)

	addr := fmt.Sprintf("http://localhost:%d", config.Port)

	http.HandleFunc("/", handleLog)
	http.HandleFunc("/style.css", serveCSS)
	http.HandleFunc("/chart.min.js", serveChartJs)
	http.HandleFunc("/settings", handleSettings)

	// Only open browser if not disabled
	if !config.NoAutoOpen {
		go func() {
			time.Sleep(500 * time.Millisecond) // Small delay to ensure server starts
			openBrowser(addr)
		}()
	}

	fmt.Printf("TDEE Tracker started!\n")
	fmt.Printf("Server address: %s\n", addr)
	fmt.Printf("Log file: %s\n", csvFile)
	fmt.Printf("Parameters file: %s\n", paramsFile)

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", config.Port), nil))
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // "linux", "freebsd", etc.
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}
