package quote

import (
	"encoding/json"
	"strings"
	"testing"
)

// The frontend requires lines and charges on every detail response. A new
// request has zero charges, so this pins that they encode as [] (not
// omitted, not null). No database needed.
func TestDetailDTOAlwaysHasLinesAndCharges(t *testing.T) {
	r := Request{
		ID: "r1", Status: StatusPending, Currency: "ETB", LineCount: 1,
		Lines: []Line{{ID: "l1", ProductName: "Chair", ProductSlug: "chair", Quantity: 1, ListUnit: 100}},
		// Charges deliberately nil, as the store returns for an unquoted request.
	}

	detail, err := json.Marshal(toDetailDTO(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"charges":[]`, `"lines":[{`, `"options":{}`, `"id":"r1"`, `"status":"pending"`} {
		if !strings.Contains(string(detail), want) {
			t.Errorf("detail JSON lacks %s: %s", want, detail)
		}
	}

	summary, err := json.Marshal(toSummaryDTO(r))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`"lines"`, `"charges"`} {
		if strings.Contains(string(summary), unwanted) {
			t.Errorf("summary JSON must not contain %s: %s", unwanted, summary)
		}
	}
}
