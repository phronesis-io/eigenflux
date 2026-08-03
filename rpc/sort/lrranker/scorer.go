package lrranker

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

// Result is the LR score for one candidate.
type Result struct {
	Probability  float64
	Logit        float64
	ModelVersion string
}

// scorer is an immutable, concurrency-safe scoring function over a loaded model.
// Instances are swapped atomically by the Manager, never mutated in place.
type scorer struct {
	version     string
	createdAtMS int64
	intercept   float64
	terms       []modelTerm
	selfTests   []selfTestCaseJSON
}

// Score computes P(followup) for one candidate. It ports model.py
// transform_rows + logits: build the raw vector, standardize per term, dot with
// coefficients, then a numerically stable sigmoid.
func (s *scorer) Score(in Input) Result {
	raw := buildRaw(in)
	logit := s.intercept
	for i := range s.terms {
		t := &s.terms[i]
		std := (rawTermValue(t, raw) - t.mean) / t.scale
		logit += std * t.coef
	}
	return Result{Probability: stableSigmoid(logit), Logit: logit, ModelVersion: s.version}
}

// standardizedVector returns the standardized feature vector (used only by the
// best-effort self-test hash comparison).
func (s *scorer) standardizedVector(in Input) []float64 {
	raw := buildRaw(in)
	vec := make([]float64, len(s.terms))
	for i := range s.terms {
		t := &s.terms[i]
		vec[i] = (rawTermValue(t, raw) - t.mean) / t.scale
	}
	return vec
}

// rawTermValue expands one term's raw scalar from the raw feature map, matching
// features.vectorize_raw: numeric (None->0, clip, optional log1p), bool, equals,
// contains.
func rawTermValue(t *modelTerm, raw map[string]any) float64 {
	switch t.kind {
	case kindNumeric:
		num := 0.0
		if p, _ := raw[t.source].(*float64); p != nil {
			num = *p
		}
		if num < t.clipMin {
			num = t.clipMin
		}
		if num > t.clipMax {
			num = t.clipMax
		}
		if t.transform == transformLog1p {
			num = math.Log1p(num)
		}
		return num
	case kindBool:
		if b, _ := raw[t.source].(bool); b {
			return 1
		}
		return 0
	case kindEquals:
		if s, _ := raw[t.source].(string); s == t.value {
			return 1
		}
		return 0
	case kindContains:
		if set, _ := raw[t.source].(map[string]bool); set[t.value] {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// stableSigmoid ports model.py stable_sigmoid: the branch avoids overflow for
// large-magnitude logits.
func stableSigmoid(x float64) float64 {
	if x >= 0 {
		return 1.0 / (1.0 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1.0 + e)
}

// selfTest validates the model against its embedded self_test_cases. Parity is
// judged by logit and probability within tolerance (1e-9), NOT by the vector
// SHA256: cross-language log1p/float rounding can differ by 1 ULP, which would
// change the standardized-vector bytes (and thus the hash) while leaving the
// logit far inside tolerance. The hash is only compared best-effort and its
// mismatch is returned as a non-fatal note for logging.
func (s *scorer) selfTest(tolerance float64) (hashNote string, err error) {
	for _, c := range s.selfTests {
		in := DecodeSelfTestInput(c.Input)
		res := s.Score(in)
		if math.Abs(res.Logit-c.ExpectedLogit) > tolerance {
			return "", fmt.Errorf("self-test %q logit mismatch: got %g want %g", c.Name, res.Logit, c.ExpectedLogit)
		}
		if math.Abs(res.Probability-c.ExpectedProbability) > tolerance {
			return "", fmt.Errorf("self-test %q probability mismatch: got %g want %g", c.Name, res.Probability, c.ExpectedProbability)
		}
		if c.ExpectedVectorSHA256 != "" {
			if got := vectorSHA256(s.standardizedVector(in)); got != c.ExpectedVectorSHA256 {
				hashNote = fmt.Sprintf("self-test %q vector sha256 differs (go=%s bundle=%s); tolerated (cross-language float rounding)", c.Name, got, c.ExpectedVectorSHA256)
			}
		}
	}
	return hashNote, nil
}

// vectorSHA256 hashes the little-endian float64 bytes of the standardized
// vector, matching numpy vector.astype("<f8").tobytes() in model.py.
func vectorSHA256(vec []float64) string {
	buf := make([]byte, 8*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}
