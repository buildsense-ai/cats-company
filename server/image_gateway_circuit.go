package server

import "time"

// image2CircuitOpen reports whether automatic requests should bypass Image2
// and whether the admitted request claimed the half-open probe.
// Explicit provider=image2 requests remain available for diagnostics and
// controlled recovery tests.
func (h *ImageGenerationProxyHandler) image2CircuitOpen(now time.Time) (bool, bool) {
	h.image2CircuitMu.Lock()
	defer h.image2CircuitMu.Unlock()

	if h.image2CircuitOpenUntil.IsZero() {
		return false, false
	}
	if now.Before(h.image2CircuitOpenUntil) {
		return true, false
	}
	if h.image2CircuitProbeInFlight {
		return true, false
	}

	// Half-open: allow one ordinary race after the cooldown. A success resets
	// the breaker; another eligible failure immediately opens it again. Keep
	// openUntil set while the probe is running so concurrent callers bypass.
	h.image2CircuitProbeInFlight = true
	h.image2CircuitFailures = h.circuitFailureThreshold - 1
	if h.image2CircuitFailures < 0 {
		h.image2CircuitFailures = 0
	}
	return false, true
}

func (h *ImageGenerationProxyHandler) recordImage2RaceOutcome(outcome imageRaceOutcome, now time.Time, halfOpenProbe bool) {
	h.image2CircuitMu.Lock()
	defer h.image2CircuitMu.Unlock()
	if halfOpenProbe {
		h.image2CircuitProbeInFlight = false
	}

	switch outcome {
	case imageRaceCompleted:
		h.image2CircuitFailures = 0
		h.image2CircuitOpenUntil = time.Time{}
	case imageRaceExhausted, imageRaceProvidersUnavailable:
		h.image2CircuitFailures++
		if h.image2CircuitFailures >= h.circuitFailureThreshold {
			h.image2CircuitOpenUntil = now.Add(h.circuitCooldown)
		}
	}
}
