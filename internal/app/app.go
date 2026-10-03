// Package app wires decimal parsing and binary16 rounding into the
// JSON batch command line used by the "half" service.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"halfconv/internal/decimal"
	"halfconv/internal/half"
)

// MinBatch / MaxBatch bound the number of decimals per request.
const (
	MinBatch = 1
	MaxBatch = 1000
)

// Item is one element of the response.
type Item struct {
	Index int     `json:"index"`
	Input string  `json:"input"`
	OK    bool    `json:"ok"`
	Error *string `json:"error,omitempty"`
	Hex   *string `json:"hex,omitempty"`
	Class *string `json:"class,omitempty"`
	// RoundingError is the reduced fraction value(result)-value(input);
	// it is null for infinities and absent for rejected inputs.
	RoundingError json.RawMessage `json:"rounding_error,omitempty"`
}

// Response is the batch result envelope.
type Response struct {
	Count  int    `json:"count"`
	Result []Item `json:"results"`
}

// Request is the accepted JSON envelope: {"values": ["1", "2"]}.
type Request struct {
	Values []string `json:"values"`
}

var (
	// ErrEmptyBatch is returned when no decimal strings were supplied.
	ErrEmptyBatch = errors.New("batch must contain between 1 and 1000 decimal strings")
	// ErrBatchTooLarge is returned for more than 1000 entries.
	ErrBatchTooLarge = errors.New("batch must contain between 1 and 1000 decimal strings")
)

// ConvertOne parses and rounds a single decimal string. A rejected
// input is reported with ok=false and is not an error at the Go level:
// batch processing keeps going.
func ConvertOne(index int, input string) Item {
	item := Item{Index: index, Input: input, OK: false}
	neg, r, err := decimal.Parse(input)
	if err != nil {
		msg := err.Error()
		item.Error = &msg
		return item
	}

	b := half.Convert(neg, r)
	hex := fmt.Sprintf("%04X", b.Pattern())
	class := string(b.Class)
	item.OK = true
	item.Hex = &hex
	item.Class = &class

	if b.IsInf() {
		item.RoundingError = json.RawMessage("null")
	} else {
		e := half.Error(b, r)
		frac := fmt.Sprintf("%d/%d", e.Num(), e.Denom())
		raw, _ := json.Marshal(frac)
		item.RoundingError = raw
	}
	return item
}

// Run validates the batch and converts every entry.
func Run(req Request) (Response, error) {
	n := len(req.Values)
	if n < MinBatch {
		return Response{}, ErrEmptyBatch
	}
	if n > MaxBatch {
		return Response{}, ErrBatchTooLarge
	}
	resp := Response{Count: n, Result: make([]Item, n)}
	for i, v := range req.Values {
		resp.Result[i] = ConvertOne(i, v)
	}
	return resp, nil
}

// DecodeRequest accepts either {"values": [...]} or a bare JSON array.
// Non-string elements and malformed JSON produce an error.
func DecodeRequest(data []byte) (Request, error) {
	var req Request
	trimmed := leftTrimSpace(data)
	if len(trimmed) == 0 {
		return req, errors.New("empty JSON input")
	}
	if trimmed[0] == '[' {
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return req, fmt.Errorf("invalid JSON array: %w", err)
		}
		req.Values = make([]string, len(raw))
		for i, el := range raw {
			if err := json.Unmarshal(el, &req.Values[i]); err != nil {
				return req, fmt.Errorf("values[%d] must be a JSON string: %w", i, err)
			}
		}
		return req, nil
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("invalid JSON request: %w", err)
	}
	return req, nil
}

func leftTrimSpace(b []byte) []byte {
	for len(b) > 0 && isSpace(b[0]) {
		b = b[1:]
	}
	return b
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// Execute reads a request from in, converts it and writes the indented
// JSON response to out. The returned int is the process exit code.
func Execute(in io.Reader, out io.Writer) int {
	data, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintf(out, `{"ok":false,"error":%q}`+"\n", "cannot read input: "+err.Error())
		return 1
	}
	req, err := DecodeRequest(data)
	if err != nil {
		fmt.Fprintf(out, `{"ok":false,"error":%q}`+"\n", err.Error())
		return 1
	}
	resp, err := Run(req)
	if err != nil {
		fmt.Fprintf(out, `{"ok":false,"error":%q}`+"\n", err.Error())
		return 1
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
	return 0
}
