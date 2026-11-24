package server

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

const maxComputeJSONInteger int64 = 1<<53 - 1

// computeSlurmRate is a published customer tariff for the sampled node shape.
// The source amount is expressed in millionths of one CNY per rate period.
type computeSlurmRate struct {
	CNYMicrosPerNode int64     `json:"cny_micros_per_node_period"`
	PeriodS          int64     `json:"rate_period_s"`
	IncrementS       int64     `json:"billing_increment_s"`
	ValidUntil       time.Time `json:"valid_until"`
}

type computePreviewRate struct {
	UnitsPerNodePeriod int64     `json:"units_per_node_period"`
	PeriodS            int64     `json:"period_s"`
	IncrementS         int64     `json:"billing_increment_s"`
	ValidUntil         time.Time `json:"valid_until"`
}

func estimateSlurmCredits(task computePreviewTask, rate *computeSlurmRate, now time.Time) computePreviewQuote {
	quote := computePreviewQuote{State: "unpriced", Reason: "slurm_rate_unavailable"}
	// One fen is 10,000 micro-CNY and buys one credit in the machine ledger.
	const unitsPerMicroCNY = commercialTopupUnitsPerFen / 10_000
	if rate == nil || !now.Before(rate.ValidUntil) ||
		rate.CNYMicrosPerNode <= 0 || rate.CNYMicrosPerNode > maxComputeJSONInteger/unitsPerMicroCNY ||
		rate.PeriodS <= 0 || rate.PeriodS > maxComputeJSONInteger ||
		rate.IncrementS <= 0 || rate.IncrementS > maxComputeJSONInteger {
		return quote
	}

	unitsPerCredit := commercialTopupUnitsPerFen
	quote.State, quote.Reason = "rate_only", "task_billable_time_missing"
	quote.UnitsPerCredit = &unitsPerCredit
	quote.Rate = &computePreviewRate{
		UnitsPerNodePeriod: rate.CNYMicrosPerNode * unitsPerMicroCNY,
		PeriodS:            rate.PeriodS,
		IncrementS:         rate.IncrementS,
		ValidUntil:         rate.ValidUntil,
	}
	if task.EstimatedBillableS <= 0 {
		return quote
	}

	quote.Reason = "estimate_out_of_range"
	if task.EstimatedBillableS > maxComputeJSONInteger {
		return quote
	}
	billed := task.EstimatedBillableS
	if remainder := billed % rate.IncrementS; remainder > 0 {
		billed += rate.IncrementS - remainder
	}
	if billed > maxComputeJSONInteger {
		return quote
	}
	workUnits := int64(max(task.Shards, task.Nodes))
	// Round time for each work unit, then round the total at ledger precision.
	total := new(big.Int).Mul(big.NewInt(billed), big.NewInt(workUnits))
	total.Mul(total, big.NewInt(quote.Rate.UnitsPerNodePeriod))
	total.Add(total, big.NewInt(rate.PeriodS-1))
	total.Quo(total, big.NewInt(rate.PeriodS))
	if !total.IsInt64() || total.Int64() > maxComputeJSONInteger {
		return quote
	}

	estimate := total.Int64()
	quote.State, quote.Reason = "estimated", ""
	quote.Estimate, quote.BilledS = &estimate, billed
	return quote
}

func computeCreditText(units int64) string {
	whole, remainder := units/commercialTopupUnitsPerFen, units%commercialTopupUnitsPerFen
	if remainder == 0 {
		return fmt.Sprint(whole)
	}
	return strings.TrimRight(fmt.Sprintf("%d.%06d", whole, remainder), "0")
}
