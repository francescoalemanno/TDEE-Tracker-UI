package main

import (
	_ "embed"
	"encoding/csv"
	"encoding/json"
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

func findOrCreateFile(envVar, defaultName string, defaultContent []byte) string {
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

	csvFile    = findOrCreateFile("TDEE_LOGS_FILE", "logs.csv", []byte{})
	paramsFile = findOrCreateFile("TDEE_PARAMS_FILE", "params.json", defaultParams)
)

var layout = "2006-01-02"

func init() {

}

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
func alpha_beta(x0, v0, t0 float64) (func(float64, float64, float64, float64), func(float64) [2]float64) {
	xk := x0
	vk := v0
	tk := t0
	feed := func(T, X, alpha, beta float64) {
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
	alpha := 1 / 12.0
	beta := 2.0*(2.0-alpha) - 4.0*math.Sqrt(1.0-alpha)
	cal_feed, cal_obs := alpha_beta(E[0].Cals, 0.0, 0.0)
	w_feed, w_obs := alpha_beta(E[0].Weight, 0.0, 0.0)

	result := []Estimate{Estimate{
		Date:   E[0].Date,
		Weight: w_obs(0.0)[0],
		TDEE:   cal_obs(0.0)[0] - w_obs(0.0)[1]*P.CalPerFatKg,
		SDw:    math.Abs(P.RsdObsWeight * w_obs(0.0)[0] / math.Sqrt(1.0-alpha*alpha)),
		SDtdee: math.Hypot(P.RsdObsWeight*w_obs(0.0)[1]/math.Sqrt(1.0-beta*beta)*P.CalPerFatKg, P.RsdObsCal*cal_obs(0.0)[0]/math.Sqrt(1.0-alpha*alpha)),
	}}

	for i := 1; i < len(E); i++ {
		dt := E[i].Date.Sub(E[0].Date).Hours() / 24.0
		c := E[i-1].Cals
		wo := E[i].Weight

		cal_feed(dt, c, alpha, beta)
		w_feed(dt, wo, alpha, beta)

		result = append(result, Estimate{
			Date:   E[i].Date,
			Weight: w_obs(dt)[0],
			TDEE:   cal_obs(dt)[0] - w_obs(dt)[1]*P.CalPerFatKg,
			SDw:    math.Abs(P.RsdObsWeight * w_obs(dt)[0] / math.Sqrt(1.0-alpha*alpha)),
			SDtdee: math.Hypot(P.RsdObsWeight*w_obs(dt)[1]/math.Sqrt(1.0-beta*beta)*P.CalPerFatKg, P.RsdObsCal*cal_obs(dt)[0]/math.Sqrt(1.0-alpha*alpha)),
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

	estimates := pf_m(entries, params)

	var goalMsg string
	if len(estimates) > 0 {
		latest := estimates[len(estimates)-1]
		_, goalMsg = goalAdvice(latest.Weight, params.GoalWeight, latest.TDEE, params.CalPerFatKg)
	}
	tmpl.Execute(w, struct {
		Entries    []LogEntry
		Estimates  []Estimate
		GoalAdvice string
	}{entries, estimates, goalMsg})
}

var tmpl = template.Must(template.New("page").Funcs(template.FuncMap{
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
}).Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TDEE Tracker</title>
<link rel="stylesheet" href="/style.css">
</head>
<body class="bg-gray-50 min-h-screen">
<header class="bg-blue-100 shadow-md px-6 py-4 mb-6 flex justify-between items-center">
  <div class="text-xl font-bold text-gray-800">TDEE Tracker</div>
  <a href="/settings" class="text-gray-800 font-medium hover:underline">⚙️ Settings</a>
</header>

<div class="container mx-auto px-4 max-w-4xl">
  {{$def := defaults .Entries}}
  <div class="bg-white rounded-lg shadow p-6 mb-8">
    <h2 class="text-lg font-semibold mb-4 text-gray-700">Add New Entry</h2>
    <form method="post" class="space-y-4">
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Date</label>
        <input type="date" name="date" value="{{index $def 0}}" required class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Weight (kg)</label>
        <input type="number" name="weight" step="0.01" value="{{index $def 1}}" required class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Calories</label>
        <input type="number" name="cals" step="1" value="{{index $def 2}}" required class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
      </div>
      <button type="submit" class="w-full bg-blue-600 text-white py-2 px-4 rounded-md hover:bg-blue-700 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 transition duration-150">Save Entry</button>
    </form>
  </div>

  <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
    <div class="bg-white rounded-lg shadow p-6 md:col-span-1">
      <h3 class="text-lg font-semibold mb-4 text-gray-700">Current Stats</h3>
      <ul class="space-y-2">
        <li><span class="font-medium">Date:</span> {{ (last_estimate .Estimates).Date.Format "2006-01-02" }}</li>
        <li><span class="font-medium">TDEE:</span> {{ printf "%.0f" (last_estimate .Estimates).TDEE }} ± {{ printf "%.0f" (last_estimate .Estimates).SDtdee }} kcal/day</li>
        <li><span class="font-medium">Weight:</span> {{ printf "%.2f" (last_estimate .Estimates).Weight }} ± {{ printf "%.2f" (last_estimate .Estimates).SDw }} kg</li>
      </ul>
      
      <h3 class="text-lg font-semibold mt-6 mb-2 text-gray-700">Goal Weight Advice</h3>
      <p class="text-gray-600">{{.GoalAdvice}}</p>
    </div>
    
    <div class="bg-white rounded-lg shadow p-6 md:col-span-1">
		<h3 class="text-lg font-semibold mb-4 text-gray-700">TDEE Trend</h3>
		<div class="h-56">
			<canvas id="tdeeChart"></canvas>
		</div>
	</div>

	<div class="bg-white rounded-lg shadow p-6 md:col-span-1">
		<h3 class="text-lg font-semibold mb-4 text-gray-700">Weight Trend</h3>
		<div class="h-56">
			<canvas id="weightChart"></canvas>
		</div>
	</div>

  <div class="bg-white rounded-lg shadow p-6 overflow-x-auto md:col-span-3">
    <h2 class="text-lg font-semibold mb-4 text-gray-700">Log + Estimates</h2>
    <table class="min-w-full divide-y divide-gray-200">
      <thead class="bg-gray-50">
        <tr>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Date</th>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Weight (kg)</th>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Calories</th>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Est. TDEE</th>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Est. Weight</th>
          <th class="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">Action</th>
        </tr>
      </thead>
      <tbody class="bg-white divide-y divide-gray-200">
        {{range $i := .Entries | reverse}}
        <tr class="hover:bg-gray-50">
          <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-900">{{(index $.Estimates $i).Date.Format "2006-01-02"}}</td>
          <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{printf "%.2f" (index $.Entries $i).Weight}}</td>
          <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{printf "%.0f" (index $.Entries $i).Cals}}</td>
          <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{printf "%.0f" (index $.Estimates $i).TDEE}} ± {{printf "%.0f" (index $.Estimates $i).SDtdee}}</td>
          <td class="px-6 py-4 whitespace-nowrap text-sm text-gray-500">{{printf "%.2f" (index $.Estimates $i).Weight}} ± {{printf "%.2f" (index $.Estimates $i).SDw}}</td>
          <td class="px-6 py-4 whitespace-nowrap text-sm">
            <button onclick="openModal('{{(index $.Entries $i).Date.Format "2006-01-02"}}', '{{(index $.Entries $i).Weight}}', '{{(index $.Entries $i).Cals}}')" 
              class="text-blue-600 hover:text-blue-900 focus:outline-none focus:underline">
              Edit
            </button>
          </td>
        </tr>
        {{end}}
      </tbody>
    </table>
  </div>
</div>

<!-- Modal -->
<div id="editModal" class="hidden fixed inset-0 bg-gray-600 bg-opacity-50 overflow-y-auto h-full w-full z-50">
  <div class="relative top-20 mx-auto p-5 border w-96 shadow-lg rounded-md bg-white">
    <div class="flex justify-between items-center mb-4">
      <h3 class="text-lg font-medium text-gray-700">Edit Entry</h3>
      <button onclick="closeModal()" class="text-gray-400 hover:text-gray-500">
        <span class="text-2xl">&times;</span>
      </button>
    </div>
    <form method="post" class="space-y-4">
      <input type="hidden" id="edit-date" name="date">
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Weight (kg)</label>
        <input type="number" id="edit-weight" name="weight" step="0.01" required 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
      </div>
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Calories</label>
        <input type="number" id="edit-cals" name="cals" step="1" required 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
      </div>
      <button type="submit" 
        class="w-full bg-blue-600 text-white py-2 px-4 rounded-md hover:bg-blue-700 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 transition duration-150">
        Update Entry
      </button>
    </form>
  </div>
</div>

<script>
function openModal(date, weight, cals) {
  document.getElementById('edit-date').value = date;
  document.getElementById('edit-weight').value = weight;
  document.getElementById('edit-cals').value = cals;
  document.getElementById('editModal').classList.remove('hidden');
}

function closeModal() {
  document.getElementById('editModal').classList.add('hidden');
}

window.onclick = function(event) {
  const modal = document.getElementById('editModal');
  if (event.target == modal) {
    closeModal();
  }
}

document.addEventListener("DOMContentLoaded", function () {
  const tdeeChart = document.getElementById("tdeeChart");
  if (tdeeChart) {
    tdeeChart.parentElement.scrollLeft = tdeeChart.scrollWidth;
  }
  
  const weightChart = document.getElementById("weightChart");
  if (weightChart) {
    weightChart.parentElement.scrollLeft = weightChart.scrollWidth;
  }
});
</script>
<script src="/chart.min.js"></script>
<script>
document.addEventListener("DOMContentLoaded", function () {
  // Prepare data for charts
  const dates = [{{range .Estimates | recent}}"{{.Date.Format "2006-01-02"}}",{{end}}];
  const tdeeValues = [{{range .Estimates | recent}}{{printf "%.1f" .TDEE}},{{end}}];
  const calsValues = [{{range .Entries | recententries}}{{printf "%.1f" .Cals}},{{end}}];
  const tdeeErrors = [{{range .Estimates | recent}}{{printf "%.1f" .SDtdee}},{{end}}];
  const weightValues = [{{range .Estimates | recent}}{{printf "%.2f" .Weight}},{{end}}];
  const weightErrors = [{{range .Estimates | recent}}{{printf "%.2f" .SDw}},{{end}}];
  
  // TDEE Chart
  const tdeeCtx = document.getElementById('tdeeChart').getContext('2d');
  new Chart(tdeeCtx, {
    type: 'line',
    data: {
      labels: dates,
      datasets: [{
        label: 'TDEE (kcal)',
        data: tdeeValues,
        borderColor: 'rgb(59, 130, 246)',
        backgroundColor: 'rgba(59, 130, 246, 0.1)',
        borderWidth: 2,
        pointRadius: 3,
        tension: 0.1,
        fill: true
      }, {
        label: 'Cals (kcal)',
        data: calsValues,
        borderColor: 'rgba(255, 183, 0,0.5)',
        backgroundColor: 'rgba(255, 197, 109, 0.5)',
        borderWidth: 1,
        pointRadius: 2,
        tension: 0.1,
        fill: true
      },]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        tooltip: {
          callbacks: {
            label: function(context) {
              const index = context.dataIndex;
              return ` + "`" + `TDEE: ${tdeeValues[index]} ± ${tdeeErrors[index]} kcal` + "`" + `;
            }
          }
        }
      },
      scales: {
        y: {
          title: {
            display: true,
            text: 'TDEE (kcal)'
          }
        },
        x: {
          title: {
            display: true,
            text: 'Date'
          }
        }
      }
    }
  });
  
  // Weight Chart
  const weightCtx = document.getElementById('weightChart').getContext('2d');
  new Chart(weightCtx, {
    type: 'line',
    data: {
      labels: dates,
      datasets: [{
        label: 'Weight (kg)',
        data: weightValues,
        borderColor: 'rgb(34, 197, 94)',
        backgroundColor: 'rgba(34, 197, 94, 0.1)',
        borderWidth: 2,
        pointRadius: 3,
        tension: 0.1,
        fill: true
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        tooltip: {
          callbacks: {
            label: function(context) {
              const index = context.dataIndex;
              return ` + "`" + `Weight: ${weightValues[index]} ± ${weightErrors[index]} kg` + "`" + `;
            }
          }
        }
      },
      scales: {
        y: {
          title: {
            display: true,
            text: 'Weight (kg)'
          }
        },
        x: {
          title: {
            display: true,
            text: 'Date'
          }
        }
      }
    }
  });
});
</script>
</body>
</html>
`))
var tmplSettings = template.Must(template.New("settings").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TDEE Tracker Settings</title>
<link rel="stylesheet" href="/style.css">
</head>
<body class="bg-gray-50 min-h-screen">
<header class="bg-blue-100 shadow-md px-6 py-4 mb-6 flex justify-between items-center">
  <div class="text-xl font-bold text-gray-800">TDEE Tracker Settings</div>
  <a href="/" class="text-gray-800 font-medium hover:underline">← Back to Tracker</a>
</header>

<div class="container mx-auto px-4 max-w-lg">
  <div class="bg-white rounded-lg shadow p-6 mb-8">
    <h2 class="text-lg font-semibold mb-6 text-gray-700">Configure Tracker Parameters</h2>
    <form method="post" class="space-y-4">
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Initial TDEE</label>
        <input type="number" name="InitialTDEE" step="1" value="{{.InitialTDEE}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Set to -1 to use first entry's calories</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Calories per Fat Kg</label>
        <input type="number" name="CalPerFatKg" step="1" value="{{.CalPerFatKg}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Default is 7700 kcal/kg</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">RSD TDEE</label>
        <input type="number" name="RsdTDEE" step="0.0001" value="{{.RsdTDEE}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Random daily variation in metabolism</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">RSD Obs Cal</label>
        <input type="number" name="RsdObsCal" step="0.0001" value="{{.RsdObsCal}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Accuracy of calorie counting</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">RSD Obs Weight</label>
        <input type="number" name="RsdObsWeight" step="0.0001" value="{{.RsdObsWeight}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Accuracy of weight measurement</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">RSD Weight Drift</label>
        <input type="number" name="RsdWeight" step="0.0001" value="{{.RsdWeight}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Random daily variation in true weight</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">PF Variance Boost</label>
        <input type="number" name="PfVarianceBoost" step="0.0001" value="{{.PfVarianceBoost}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Particle filter variance scaling</p>
      </div>
      
      <div>
        <label class="block text-sm font-medium text-gray-700 mb-1">Goal Weight (kg)</label>
        <input type="number" name="GoalWeight" step="0.01" value="{{.GoalWeight}}" 
          class="w-full px-3 py-2 border border-gray-300 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500">
        <p class="mt-1 text-xs text-gray-500">Set to -1 to disable goal advice</p>
      </div>
      
      <button type="submit" 
        class="w-full bg-blue-600 text-white py-2 px-4 rounded-md hover:bg-blue-700 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 transition duration-150">
        Save Settings
      </button>
    </form>
  </div>
</div>
</body>
</html>
`))

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

	tmplSettings.Execute(w, p)
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

func main() {
	const addr = "http://localhost:8080"

	http.HandleFunc("/", handleLog)
	http.HandleFunc("/style.css", serveCSS)
	http.HandleFunc("/chart.min.js", serveChartJs)
	http.HandleFunc("/settings", handleSettings)
	go func() {
		time.Sleep(500 * time.Millisecond) // Small delay to ensure server starts
		openBrowser(addr)
	}()

	fmt.Println("Serving at", addr)
	log.Fatal(http.ListenAndServe(":8080", nil))
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
