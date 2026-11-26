package dynamic

import "testing"

func TestNormalizeRoutePath(t *testing.T) {
	if got := normalizeRoutePath("pages/index/index"); got != "/pages/index/index" {
		t.Fatalf("normalize=%q", got)
	}
	if got := normalizeRoutePath("/pages/index/index"); got != "/pages/index/index" {
		t.Fatalf("normalize=%q", got)
	}
}

func TestClassifyRoute(t *testing.T) {
	cases := map[string]string{
		"pages/login/index":   "auth",
		"pages/goods/detail":  "detail",
		"pages/order/list":    "order",
		"pages/setting/about": "setting",
	}
	for path, want := range cases {
		if got := classifyRoute(path); got != want {
			t.Fatalf("classifyRoute(%q)=%q, want %q", path, got, want)
		}
	}
}
