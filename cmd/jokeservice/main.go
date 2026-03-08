// JokeService is a deliberately flaky HTTP server used as a chaos target for GopherGuard.
//
// Behaviour (tunable via env vars):
//   - ERROR_RATE   (float, 0–1, default 0.20): fraction of requests that return HTTP 500.
//   - PANIC_RATE   (float, 0–1, default 0.05): fraction of requests that panic (crash the process).
//   - OOM_RATE     (float, 0–1, default 0.02): fraction of requests that allocate ~500 MB and
//     hold it, simulating an OOM situation on memory-constrained pods.
//   - PORT         (int, default 8080): listening port.
//
// In a real cluster the Deployment restartPolicy keeps the pod respawning, triggering
// CrashLoopBackOff which GopherGuard will detect and diagnose.
package main

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"
)

var (
	errorRate float64 = 0.20
	panicRate float64 = 0.05
	oomRate   float64 = 0.02

	// oomTrap holds allocated slices so the GC cannot reclaim them.
	oomTrap [][]byte
)

func init() {
	errorRate = envFloat("ERROR_RATE", errorRate)
	panicRate = envFloat("PANIC_RATE", panicRate)
	oomRate = envFloat("OOM_RATE", oomRate)
}

func main() {
	port := envString("PORT", "8080")
	slog.Info("JokeService starting",
		"port", port,
		"errorRate", errorRate,
		"panicRate", panicRate,
		"oomRate", oomRate,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleJoke)
	mux.HandleFunc("/healthz", handleHealthz)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func handleJoke(w http.ResponseWriter, r *http.Request) {
	roll := rand.Float64()

	switch {
	case roll < panicRate:
		// Simulate a fatal crash — Kubernetes will restart the pod.
		slog.Error("💥 deliberate panic — GopherGuard, catch me!")
		panic("JokeService: deliberate crash for chaos demo")

	case roll < panicRate+oomRate:
		// Simulate memory exhaustion — allocate ~500 MB and hold it.
		slog.Warn("🧠 OOM simulation — allocating 500 MB")
		block := make([]byte, 500<<20)
		oomTrap = append(oomTrap, block)
		runtime.GC() // force GC so we definitely OOM on constrained pods
		http.Error(w, "out of memory (simulated)", http.StatusInternalServerError)

	case roll < panicRate+oomRate+errorRate:
		slog.Warn("❌ returning HTTP 500")
		http.Error(w, `{"error":"something went wrong, lol"}`, http.StatusInternalServerError)

	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"joke":"%s"}`, randomJoke())
	}
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// randomJoke returns one of a handful of nerdy jokes.
// GopherGuard will diagnose the pod when it crashes mid-joke.
func randomJoke() string {
	jokes := []string{
		"Why do Java developers wear glasses? Because they don't C#.",
		"A SQL query walks into a bar, walks up to two tables and asks... 'Can I join you?'",
		"Why do programmers prefer dark mode? Because light attracts bugs.",
		"I would tell you a UDP joke, but you might not get it.",
		"There are 10 types of people: those who understand binary, and those who don't.",
	}
	return jokes[rand.Intn(len(jokes))]
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("invalid env var, using default", "key", key, "value", v)
		return fallback
	}
	return f
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
