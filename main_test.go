package main

import (
	"math"
	"testing"
	"time"

	"math/rand"
)

func TestKalmanFilter(t *testing.T) {
	startWeight := 60.0
	finalWeight := 45.0
	trueTDEE := 2500.0
	data := []LogEntry{{
		Date:   time.Now(),
		Weight: startWeight,
		Cals:   startWeight * 28.0,
	}}
	P := loadParams()
	goodWeight := 0
	goodTDEE := 0
	for _ = range 1000 {
		last := data[len(data)-1]
		est := pf_m(data, P)
		cal, _ := goalAdvice(last.Weight, finalWeight, est[len(est)-1].TDEE, P.CalPerFatKg)
		new_w := last.Weight + (cal+rand.NormFloat64()*50-trueTDEE)/P.CalPerFatKg + rand.NormFloat64()*0.02
		data = append(data, LogEntry{
			Date:   last.Date.Add(time.Hour * 24),
			Weight: new_w,
			Cals:   cal,
		})
		if math.Abs(new_w-finalWeight) < 1 {
			goodWeight += 1
		}
		if math.Abs(est[len(est)-1].TDEE-trueTDEE) < 50 {
			goodTDEE += 1
		}
		//fmt.Println(i, goodWeight, goodTDEE, last.Weight, est[len(est)-1].TDEE)
	}
	if goodTDEE < 830 || goodWeight < 700 {
		t.Fail()
	}
}
