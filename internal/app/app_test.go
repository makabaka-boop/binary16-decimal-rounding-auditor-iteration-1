package app_test

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"halfconv/internal/app"
	"halfconv/internal/decimal"
	"halfconv/internal/half"
)

func decodeResponse(t *testing.T, out *bytes.Buffer) app.Response {
	t.Helper()
	var resp app.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON (%v): %s", err, out.String())
	}
	return resp
}

func execute(t *testing.T, body string) (app.Response, int) {
	t.Helper()
	var out bytes.Buffer
	code := app.Execute(strings.NewReader(body), &out)
	return decodeResponse(t, &out), code
}

func TestBatchEndToEnd(t *testing.T) {
	body := `{"values": ["0.1", "-0", "65520", "NaN", "1e-50", "2050"]}`
	resp, code := execute(t, body)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if resp.Count != 6 || len(resp.Result) != 6 {
		t.Fatalf("count = %d, want 6", resp.Count)
	}

	want := []struct {
		ok    bool
		hex   string
		class string
		err   json.RawMessage
	}{
		{true, "2E66", "normal", json.RawMessage(`"-1/40960"`)},
		{true, "8000", "zero", json.RawMessage(`"0/1"`)},
		{true, "7C00", "infinity", json.RawMessage(`null`)},
		{false, "", "", nil},
		{true, "0000", "zero", nil}, // error checked below
		{true, "6801", "normal", json.RawMessage(`"0/1"`)},
	}
	for i, w := range want {
		it := resp.Result[i]
		if it.OK != w.ok {
			t.Fatalf("item %d: ok=%v, want %v (%+v)", i, it.OK, w.ok, it)
		}
		if !w.ok {
			if it.Error == nil || *it.Error == "" {
				t.Fatalf("item %d: missing rejection reason", i)
			}
			if it.Hex != nil || it.Class != nil || it.RoundingError != nil {
				t.Fatalf("item %d: rejected item must not carry results", i)
			}
			continue
		}
		if *it.Hex != w.hex {
			t.Fatalf("item %d: hex=%v, want %s", i, *it.Hex, w.hex)
		}
		if *it.Class != w.class {
			t.Fatalf("item %d: class=%v, want %s", i, *it.Class, w.class)
		}
		if w.class == "infinity" {
			if string(it.RoundingError) != "null" {
				t.Fatalf("item %d: rounding_error=%s, want null", i, it.RoundingError)
			}
		}
	}

	// 1e-50: rounds to +0, error = -1/10^50, reduced denominator fully written.
	tinyErr := string(resp.Result[4].RoundingError)
	if !strings.Contains(tinyErr, "1/100000000000000000000000000000000000000000000000000") {
		t.Fatalf("1e-50 error = %s, want -1/10^50", tinyErr)
	}
}

func TestBareArrayEnvelope(t *testing.T) {
	resp, code := execute(t, `["1", "2.5"]`)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if resp.Count != 2 {
		t.Fatalf("count = %d", resp.Count)
	}
	if got := *resp.Result[0].Hex; got != "3C00" {
		t.Fatalf("item 0 hex = %s, want 3C00", got)
	}
	if got := *resp.Result[1].Hex; got != "4100" {
		t.Fatalf("item 1 hex = %s, want 4100 (2.5)", got)
	}
}

func TestBatchSizeLimits(t *testing.T) {
	run := func(n int) (*bytes.Buffer, int) {
		vals := make([]string, n)
		for i := range vals {
			vals[i] = "1"
		}
		body, _ := json.Marshal(app.Request{Values: vals})
		var out bytes.Buffer
		code := app.Execute(bytes.NewReader(body), &out)
		return &out, code
	}

	if _, code := run(1000); code != 0 {
		t.Fatalf("batch of 1000 should succeed, code=%d", code)
	}
	if _, code := run(1001); code != 1 {
		t.Fatalf("batch of 1001 should fail, code=%d", code)
	}

	var out bytes.Buffer
	if code := app.Execute(strings.NewReader(`{"values": []}`), &out); code != 1 {
		t.Fatalf("empty batch code=%d, want 1", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("1 and 1000")) {
		t.Fatalf("empty batch message: %s", out.String())
	}
}

func TestMalformedRequests(t *testing.T) {
	bodies := []string{
		``, `not json`, `{"values": [1, 2]}`, `{"values": "1"}`,
		`[`, `{"values": [NaN]}`,
	}
	for _, b := range bodies {
		var out bytes.Buffer
		code := app.Execute(strings.NewReader(b), &out)
		if code == 0 {
			t.Errorf("body %q: expected non-zero exit", b)
		}
		if !bytes.Contains(out.Bytes(), []byte(`"ok":false`)) {
			t.Errorf("body %q: expected error envelope, got %s", b, out.String())
		}
	}
}

func TestPerItemRejectionsDoNotAbortBatch(t *testing.T) {
	resp, code := execute(t, `{"values": ["Infinity", "1e51", "x", "1.0"]}`)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for i := 0; i < 3; i++ {
		if resp.Result[i].OK {
			t.Fatalf("item %d unexpectedly ok", i)
		}
	}
	if !resp.Result[3].OK || *resp.Result[3].Hex != "3C00" {
		t.Fatalf("item 3: %+v", resp.Result[3])
	}
}

func TestIntervalCertificateOptional(t *testing.T) {
	var noCert bytes.Buffer
	if code := app.Execute(strings.NewReader(`{"values":["0.1"]}`), &noCert); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if bytes.Contains(noCert.Bytes(), []byte("interval_certificate")) {
		t.Fatalf("default response must omit certificates: %s", noCert.String())
	}

	var bare bytes.Buffer
	if code := app.Execute(strings.NewReader(`["0.1"]`), &bare); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if bytes.Contains(bare.Bytes(), []byte("interval_certificate")) {
		t.Fatalf("bare array response must omit certificates: %s", bare.String())
	}
}

func TestIntervalCertificatesBatch(t *testing.T) {
	body := `{
		"include_interval_certificate": true,
		"values": [
			"0", "-0", "0.000000059604644775390625",
			"0.000060975551605224609375",
			"0.00006103515625", "1", "65504", "65520", "NaN"
		]
	}`
	resp, code := execute(t, body)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}

	wantCerts := []struct {
		hex            string
		lower          *string
		upper          *string
		lowerInclusive bool
		upperInclusive bool
	}{
		{"0000", strPtr("0/1"), strPtr("1/33554432"), true, true},
		{"8000", strPtr("-1/33554432"), strPtr("0/1"), true, true},
		// The tie with even zero is excluded from the smallest subnormal.
		{"0001", strPtr("1/33554432"), strPtr("3/33554432"), false, false},
		// F=1023 is odd, so both midpoints belong to even neighbors.
		{"03FF", strPtr("2045/33554432"), strPtr("2047/33554432"), false, false},
		// F=0 is even and owns the asymmetric subnormal/normal midpoint.
		{"0400", strPtr("2047/33554432"), strPtr("2049/33554432"), true, true},
		// A normal power of two has a smaller half-ULP on its lower side.
		{"3C00", strPtr("4095/4096"), strPtr("2049/2048"), true, true},
		// The largest finite value loses both ties: 65488 and 65520.
		{"7BFF", strPtr("65488/1"), strPtr("65520/1"), false, false},
		// Positive infinity is the inclusive [65520, +infinity) tail.
		{"7C00", strPtr("65520/1"), nil, true, false},
	}

	for i, want := range wantCerts {
		item := resp.Result[i]
		if !item.OK || item.Hex == nil || *item.Hex != want.hex {
			t.Fatalf("item %d: %+v", i, item)
		}
		if item.IntervalCertificate == nil {
			t.Fatalf("item %d missing certificate", i)
		}
		cert := item.IntervalCertificate
		if !sameStringPtr(cert.Lower, want.lower) || !sameStringPtr(cert.Upper, want.upper) ||
			cert.LowerInclusive != want.lowerInclusive || cert.UpperInclusive != want.upperInclusive {
			t.Fatalf("item %d cert = %+v, want %+v", i, cert, want)
		}
	}
	if resp.Result[8].OK || resp.Result[8].IntervalCertificate != nil {
		t.Fatalf("invalid item must still fail and have no certificate: %+v", resp.Result[8])
	}

	// Render every finite endpoint as a legal decimal and independently
	// ask the converter whether that decimal encodes this bit pattern.
	for i, want := range wantCerts {
		endpoints := []struct {
			value     *string
			inclusive bool
		}{
			{want.lower, want.lowerInclusive},
			{want.upper, want.upperInclusive},
		}
		for _, ep := range endpoints {
			if ep.value == nil {
				continue
			}
			q, ok := new(big.Rat).SetString(*ep.value)
			if !ok {
				t.Fatalf("bad rational endpoint %q", *ep.value)
			}
			s := exactDecimal(q)
			if q.Sign() == 0 && want.hex == "8000" {
				s = "-0"
			}
			item := app.ConvertOne(i, s, false)
			if !item.OK || item.Hex == nil {
				t.Fatalf("endpoint %s (%s): conversion failed: %+v", *ep.value, s, item)
			}
			if got := *item.Hex == want.hex; got != ep.inclusive {
				t.Fatalf("endpoint %s membership in %s = %v, want inclusive=%v (got %s)",
					*ep.value, want.hex, got, ep.inclusive, *item.Hex)
			}
			neg, parsed, err := decimal.Parse(s)
			if err != nil {
				t.Fatalf("Parse(%s): %v", s, err)
			}
			gotBits := half.Convert(neg, parsed)
			if half.Certificate(gotBits).Contains(neg, parsed) != true {
				t.Fatalf("endpoint %s converted to %04X but is outside its certificate", s, gotBits.Pattern())
			}
		}
	}
}

func exactDecimal(q *big.Rat) string {
	s := q.FloatString(30)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func strPtr(s string) *string { return &s }

func sameStringPtr(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}
