package catalog

import (
	"context"
	"testing"
)

func TestNormalizeOnlySearchResultsAndValidateInput(t *testing.T) {
	response := map[string]any{"ret": float64(0), "data": map[string]any{"offset": float64(0), "size": float64(3), "total": float64(3), "components": []any{map[string]any{"cardId": "SearchResult", "data": map[string]any{"itemData": []any{map[string]any{"pkg_name": "wx141bfb9b73c970a9", "game_type": float64(2), "name": "fixture", "status": float64(1)}}}}}}}
	got, err := Normalize(response, "missing", 30)
	if err != nil {
		t.Fatal(err)
	}
	if got["hits"] != 1 || got["name_match_count"] != 0 || got["catalog_exhaustive"] != false {
		t.Fatal(got)
	}
	if _, err = NewOnline(nil).Search(context.Background(), "", 30); err == nil {
		t.Fatal("accepted empty search")
	}
	delete(response, "ret")
	if _, err = Normalize(response, "x", 30); err == nil {
		t.Fatal("accepted missing ret")
	}
}
