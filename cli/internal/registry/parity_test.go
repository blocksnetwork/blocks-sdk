package registry

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

func TestMinPriceParityWithBackend(t *testing.T) {
	tsPath := filepath.Join("..", "..", "..", "..", "afui_mvp_backend", "src", "modules", "billing", "billing.constants.ts")
	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("cannot read billing.constants.ts: %v", err)
	}
	src := string(data)

	taskRe := regexp.MustCompile(`MIN_PRICE_PER_TASK\s*=\s*'([^']+)'`)
	minuteRe := regexp.MustCompile(`MIN_PRICE_PER_MINUTE\s*=\s*'([^']+)'`)

	taskMatch := taskRe.FindStringSubmatch(src)
	if taskMatch == nil {
		t.Fatal("MIN_PRICE_PER_TASK not found in billing.constants.ts")
	}
	minuteMatch := minuteRe.FindStringSubmatch(src)
	if minuteMatch == nil {
		t.Fatal("MIN_PRICE_PER_MINUTE not found in billing.constants.ts")
	}

	if taskMatch[1] != MinPricePerTask {
		t.Errorf("MinPricePerTask = %q, backend = %q", MinPricePerTask, taskMatch[1])
	}
	if minuteMatch[1] != MinPricePerMinute {
		t.Errorf("MinPricePerMinute = %q, backend = %q", MinPricePerMinute, minuteMatch[1])
	}
}

func TestMaxPriceAndFreeCapParityWithBackend(t *testing.T) {
	tsPath := filepath.Join("..", "..", "..", "..", "afui_mvp_backend", "src", "modules", "billing", "pricing-limits.service.ts")
	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("cannot read pricing-limits.service.ts: %v", err)
	}
	src := string(data)

	maxTaskRe := regexp.MustCompile(`FALLBACK_MAX_PRICE_PER_TASK\s*=\s*'([^']+)'`)
	maxMinuteRe := regexp.MustCompile(`FALLBACK_MAX_PRICE_PER_MINUTE\s*=\s*'([^']+)'`)
	maxFreeTasksRe := regexp.MustCompile(`FALLBACK_MAX_FREE_TASKS\s*=\s*(\d+)`)
	maxFreeMinutesRe := regexp.MustCompile(`FALLBACK_MAX_FREE_MINUTES\s*=\s*(\d+)`)

	if m := maxTaskRe.FindStringSubmatch(src); m != nil {
		beVal, _ := decimal.NewFromString(m[1])
		cliVal, _ := decimal.NewFromString(MaxPricePerTask)
		if !beVal.Equal(cliVal) {
			t.Errorf("MaxPricePerTask = %q, backend fallback = %q", MaxPricePerTask, m[1])
		}
	} else {
		t.Fatal("FALLBACK_MAX_PRICE_PER_TASK not found in pricing-limits.service.ts")
	}

	if m := maxMinuteRe.FindStringSubmatch(src); m != nil {
		beVal, _ := decimal.NewFromString(m[1])
		cliVal, _ := decimal.NewFromString(MaxPricePerMinute)
		if !beVal.Equal(cliVal) {
			t.Errorf("MaxPricePerMinute = %q, backend fallback = %q", MaxPricePerMinute, m[1])
		}
	} else {
		t.Fatal("FALLBACK_MAX_PRICE_PER_MINUTE not found in pricing-limits.service.ts")
	}

	if m := maxFreeTasksRe.FindStringSubmatch(src); m != nil {
		if m[1] != strconv.Itoa(MaxFreeTasksPerConsumer) {
			t.Errorf("MaxFreeTasksPerConsumer = %d, backend fallback = %s", MaxFreeTasksPerConsumer, m[1])
		}
	} else {
		t.Fatal("FALLBACK_MAX_FREE_TASKS not found in pricing-limits.service.ts")
	}

	if m := maxFreeMinutesRe.FindStringSubmatch(src); m != nil {
		if m[1] != strconv.Itoa(MaxFreeMinutesPerConsumer) {
			t.Errorf("MaxFreeMinutesPerConsumer = %d, backend fallback = %s", MaxFreeMinutesPerConsumer, m[1])
		}
	} else {
		t.Fatal("FALLBACK_MAX_FREE_MINUTES not found in pricing-limits.service.ts")
	}
}
